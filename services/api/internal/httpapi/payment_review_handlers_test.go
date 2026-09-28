package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestPaymentReviewDecisionStateMachine(t *testing.T) {
	retry, err := planPaymentReviewDecision("review", "retry_query", true, 0, false, "")
	if err != nil || retry.NextStatus != "pending" || !retry.ResetQueryAttempts || retry.Terminal || retry.ReplayCallbacks {
		t.Fatalf("retry plan mismatch: plan=%+v err=%v", retry, err)
	}
	replay, err := planPaymentReviewDecision("review", "replay_callbacks", true, 2, true, "")
	if err != nil || replay.NextStatus != "review" || !replay.ReplayCallbacks || replay.Terminal ||
		replay.EventType != "payment.review_callback_replay_requested" {
		t.Fatalf("replay plan mismatch: plan=%+v err=%v", replay, err)
	}
	failure, err := planPaymentReviewDecision("review", "mark_failed", false, 0, false, "Provider investigation found no collection.")
	if err != nil || failure.NextStatus != "failed" || !failure.Terminal || failure.ResetQueryAttempts || failure.ReplayCallbacks {
		t.Fatalf("failure plan mismatch: plan=%+v err=%v", failure, err)
	}
}

func TestPaymentReviewIdempotencyScopeSeparatesOperators(t *testing.T) {
	paymentID := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	first := paymentReviewDecisionScope("550e8400-e29b-41d4-a716-446655440000", paymentID)
	second := paymentReviewDecisionScope("650e8400-e29b-41d4-a716-446655440000", paymentID)
	if first == second || !strings.Contains(first, paymentID) {
		t.Fatalf("operator-scoped idempotency keys collided: %q %q", first, second)
	}
}

func TestPaymentReviewDecisionRejectsUnsafeTransitions(t *testing.T) {
	tests := []struct {
		name, status, decision, note string
		checkout, success            bool
		pending                      int
	}{
		{name: "succeeded immutable", status: "succeeded", decision: "retry_query", checkout: true},
		{name: "query without checkout", status: "review", decision: "retry_query"},
		{name: "query outside review", status: "failed", decision: "retry_query", checkout: true},
		{name: "callback absent", status: "review", decision: "replay_callbacks", checkout: true},
		{name: "failure without note", status: "review", decision: "mark_failed"},
		{name: "failure with callback pending", status: "review", decision: "mark_failed", note: "Investigated", pending: 1},
		{name: "failure with success signal", status: "review", decision: "mark_failed", note: "Investigated", success: true},
		{name: "manual success forbidden", status: "review", decision: "mark_succeeded", note: "Unsafe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := planPaymentReviewDecision(test.status, test.decision, test.checkout,
				test.pending, test.success, test.note); err == nil {
				t.Fatal("unsafe transition was accepted")
			}
		})
	}
}

func TestVerifiedLatePaymentDisposition(t *testing.T) {
	registered := "registered"
	tests := []struct {
		name              string
		competitionStatus string
		draw, full        bool
		existing          *string
		existingInDraw    bool
		wantStatus        string
		wantReason        string
	}{
		{name: "normal registration", competitionStatus: "registration_open", wantStatus: "registered"},
		{name: "frozen draw", competitionStatus: "check_in", draw: true, wantStatus: "withdrawal_pending", wantReason: "operations_adjustment"},
		{name: "cancelled", competitionStatus: "cancelled", wantStatus: "withdrawal_pending", wantReason: "competition_cancelled"},
		{name: "capacity filled", competitionStatus: "registration_open", full: true, wantStatus: "withdrawal_pending", wantReason: "operations_adjustment"},
		{name: "already in frozen draw", competitionStatus: "running", draw: true, existing: &registered, existingInDraw: true, wantStatus: "registered"},
		{name: "existing entry does not consume new capacity", competitionStatus: "check_in", full: true, existing: &registered, wantStatus: "registered"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := planVerifiedPaymentRegistration(test.competitionStatus, test.draw, test.full,
				test.existing, test.existingInDraw)
			if plan.EntryStatus != test.wantStatus || plan.RefundReason != test.wantReason {
				t.Fatalf("plan=%+v, want status=%s reason=%s", plan, test.wantStatus, test.wantReason)
			}
			if test.wantReason != "" && plan.RefundNote == "" {
				t.Fatal("refund disposition omitted its durable reason note")
			}
		})
	}
}

func TestInvalidProviderTimestampCannotDemoteTerminalPayment(t *testing.T) {
	for _, required := range []string{
		"status IN ('pending','callback_received','review')",
		"completed_at=NULL",
	} {
		if !strings.Contains(invalidTransactionTimestampReviewSQL, required) {
			t.Fatalf("invalid-timestamp update omitted terminal-state guard %q", required)
		}
	}
	if strings.Contains(invalidTransactionTimestampReviewSQL, "'succeeded'") ||
		strings.Contains(invalidTransactionTimestampReviewSQL, "'failed'") {
		t.Fatal("invalid-timestamp update can demote a terminal payment")
	}
}

func TestWithdrawalPendingEntriesStillReserveCapacity(t *testing.T) {
	if !strings.Contains(competitionCapacityEntriesSQL, "NOT IN ('withdrawn','disqualified')") {
		t.Fatal("capacity query no longer excludes only terminal non-playing entries")
	}
	if strings.Contains(competitionCapacityEntriesSQL, "withdrawal_pending") {
		t.Fatal("withdrawal_pending entry stopped reserving capacity before its refund decision")
	}
}

func TestPaymentReviewCursorIsBoundToOperatorStatusAndFilters(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "payment-review-cursor-test-secret"}}
	actorID := "550e8400-e29b-41d4-a716-446655440000"
	competitionID := "123e4567-e89b-42d3-a456-426614174000"
	filters := paymentReviewFilters{Status: "review", CompetitionID: competitionID, Checkout: "present", MinAttempts: 4}
	now := time.Now().UTC()
	token, err := encodePublicCursor(publicCursor{
		Kind: paymentReviewCursorKind, ExpiresAt: now.Add(time.Hour).Unix(), Query: actorID,
		Scope: filters.scope(), SortTime: now.UnixNano(), ID: "4d3e5536-bf3c-4dba-a643-575e43f56970",
	}, server.config.AccessTokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	requestFor := func(actor, query string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/v1/admin/payment-reviews?"+query+"&cursor="+token, nil)
		return request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: actor}))
	}
	query := "status=review&competitionId=" + competitionID + "&checkout=present&minAttempts=4&limit=100"
	recorder := httptest.NewRecorder()
	got, limit, cursor, ok := server.paymentReviewPageInput(recorder, requestFor(actorID, query))
	if !ok || limit != 100 || cursor == nil || got.scope() != filters.scope() {
		t.Fatalf("valid cursor rejected: filters=%+v limit=%d cursor=%+v body=%s", got, limit, cursor, recorder.Body.String())
	}
	for name, test := range map[string][2]string{
		"operator": {"650e8400-e29b-41d4-a716-446655440000", query},
		"status":   {actorID, strings.Replace(query, "status=review", "status=failed", 1)},
		"checkout": {actorID, strings.Replace(query, "checkout=present", "checkout=missing", 1)},
	} {
		actor, altered := test[0], test[1]
		recorder = httptest.NewRecorder()
		if _, _, _, ok := server.paymentReviewPageInput(recorder, requestFor(actor, altered)); ok {
			t.Fatalf("cursor escaped %s binding", name)
		}
	}
}

func TestPaymentReviewFiltersAreBounded(t *testing.T) {
	server := &Server{}
	actorID := "550e8400-e29b-41d4-a716-446655440000"
	for _, query := range []string{"status=unknown", "checkout=yes", "minAttempts=13", "minAttempts=-1", "limit=101", "competitionId=nope"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/admin/payment-reviews?"+query, nil)
		request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: actorID}))
		recorder := httptest.NewRecorder()
		if _, _, _, ok := server.paymentReviewPageInput(recorder, request); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid filter %q accepted", query)
		}
	}
}

func TestPaymentReviewRoutesAreAdminProtected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	mux := http.NewServeMux()
	server.registerPaymentReviewRoutes(mux)
	id := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/payment-reviews"},
		{http.MethodGet, "/v1/admin/payment-reviews/" + id},
		{http.MethodPost, "/v1/admin/payment-reviews/" + id + "/decisions"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s not auth protected: %d", route.method, route.path, recorder.Code)
		}
	}
}

func TestPaymentReviewProjectionNeverExposesFullPhone(t *testing.T) {
	if masked := maskPhone("254712345678"); masked != "2547*****678" {
		t.Fatalf("unexpected masked phone: %s", masked)
	}
	if _, err := scanPaymentReview(countOnlyScanner{t: t, want: 23}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("payment review scan target mismatch: %v", err)
	}
}

func TestPaymentReviewAuditHistoryRedactsSensitiveValues(t *testing.T) {
	value := sanitizePaymentReviewAuditValue(map[string]any{
		"phone_e164":      "254712345678",
		"note":            "Customer called from +254112345678.",
		"callbackPayload": map[string]any{"PhoneNumber": "254712345678"},
		"requestIp":       "192.0.2.1",
	})
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"254712345678", "254112345678", "192.0.2.1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("safe audit history exposed %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"2547*****678", "2541*****678", "[redacted]"} {
		if !strings.Contains(text, required) {
			t.Fatalf("safe audit history omitted redaction %q: %s", required, text)
		}
	}
}

func TestPaymentReviewOpenAPIForbidsManualSuccess(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "openapi", "payment-review.paths.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contract := string(contents)
	for _, required := range []string{
		"/v1/admin/payment-reviews:",
		"/v1/admin/payment-reviews/{id}/decisions:",
		"enum: [retry_query, replay_callbacks, mark_failed]",
		"full phone number is never returned",
		"Idempotency-Key",
		"callbackReplayRequested",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("payment review contract missing %q", required)
		}
	}
	if strings.Contains(contract, "mark_succeeded") {
		t.Fatal("payment review contract exposes an unsafe manual-success action")
	}
}
