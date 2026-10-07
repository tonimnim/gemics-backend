package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestRefundTransition(t *testing.T) {
	tests := []struct {
		current, decision, want string
		terminal, ok            bool
	}{
		{"requested", "approve", "approved", false, true},
		{"approved", "mark_succeeded", "succeeded", true, true},
		{"manual_review", "mark_failed", "failed", true, true},
		{"requested", "mark_succeeded", "succeeded", true, false},
		{"succeeded", "reject", "rejected", true, false},
	}
	for _, test := range tests {
		got, terminal, ok := refundTransition(test.current, test.decision)
		if got != test.want || terminal != test.terminal || ok != test.ok {
			t.Fatalf("%s/%s => %s,%v,%v; want %s,%v,%v", test.current, test.decision,
				got, terminal, ok, test.want, test.terminal, test.ok)
		}
	}
}

func TestMandatoryRefundCannotBeRejected(t *testing.T) {
	if _, _, ok := refundDecisionTransition("requested", "reject", true); ok {
		t.Fatal("mandatory automatic refund was rejectable")
	}
	if next, terminal, ok := refundDecisionTransition("requested", "reject", false); !ok || !terminal || next != "rejected" {
		t.Fatalf("voluntary refund rejection changed: next=%s terminal=%v ok=%v", next, terminal, ok)
	}
	if next, terminal, ok := refundDecisionTransition("requested", "approve", true); !ok || terminal || next != "approved" {
		t.Fatalf("mandatory refund could not proceed: next=%s terminal=%v ok=%v", next, terminal, ok)
	}
}

func TestStaffDecisionIdempotencyScopesSeparateOperators(t *testing.T) {
	firstActor := "550e8400-e29b-41d4-a716-446655440000"
	secondActor := "650e8400-e29b-41d4-a716-446655440000"
	resourceID := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	if refundDecisionScope(firstActor, resourceID) == refundDecisionScope(secondActor, resourceID) {
		t.Fatal("refund decision idempotency scope is reusable across operators")
	}
	if gameAccountVerificationDecisionScope(firstActor, resourceID) ==
		gameAccountVerificationDecisionScope(secondActor, resourceID) {
		t.Fatal("verification decision idempotency scope is reusable across operators")
	}
}

func TestMandatoryRefundSchemaAndContract(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000005_payments.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile(filepath.Join("..", "..", "openapi", "payment-account-verification.paths.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []struct {
		contents []byte
		text     string
	}{
		{migration, "mandatory boolean DEFAULT false NOT NULL"},
		{migration, "((NOT mandatory) OR (status <> 'rejected'::text))"},
		{migration, "((reason_code <> 'competition_cancelled'::text) OR mandatory)"},
		{contract, "mandatory: { type: boolean"},
	} {
		if !strings.Contains(string(required.contents), required.text) {
			t.Fatalf("mandatory-refund contract omitted %q", required.text)
		}
	}
}

func TestPaidRefundCacheInvalidationIsPostCommit(t *testing.T) {
	source, err := os.ReadFile("payment_lifecycle_handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, bounds := range []struct {
		name, start, end string
	}{
		{"paid withdrawal", "func (s *Server) requestPaidWithdrawal", "const refundSelect"},
		{"refund decision", "func (s *Server) decideRefund", "func validRefundDecision"},
	} {
		start := strings.Index(text, bounds.start)
		end := strings.Index(text, bounds.end)
		if start < 0 || end <= start {
			t.Fatalf("%s handler bounds not found", bounds.name)
		}
		handler := text[start:end]
		commit := strings.LastIndex(handler, "tx.Commit(r.Context())")
		invalidate := strings.LastIndex(handler, "invalidateCompetitionCachesContext(context.WithoutCancel(r.Context()), competitionID)")
		if commit < 0 || invalidate < 0 || commit > invalidate {
			t.Fatalf("%s cache invalidation is not post-commit: commit=%d invalidate=%d", bounds.name, commit, invalidate)
		}
	}
}

func TestRefundDecisionPayloadValidation(t *testing.T) {
	valid := []refundDecisionInput{
		{Decision: "approve"},
		{Decision: "reject", Note: "Outside the refund policy"},
		{Decision: "mark_failed", Note: "Provider request failed"},
		{Decision: "mark_succeeded", ProviderReceipt: "RF123456789"},
	}
	for _, input := range valid {
		if !validRefundDecisionInput(input) {
			t.Fatalf("valid decision rejected: %+v", input)
		}
	}
	invalid := []refundDecisionInput{
		{Decision: "reject"},
		{Decision: "mark_failed"},
		{Decision: "approve", ProviderReceipt: "not-yet-succeeded"},
		{Decision: "mark_succeeded"},
	}
	for _, input := range invalid {
		if validRefundDecisionInput(input) {
			t.Fatalf("invalid decision accepted: %+v", input)
		}
	}
}

func TestPaymentCursorIsBoundToPlayerAndStatus(t *testing.T) {
	now := time.Now().UTC()
	server := &Server{config: config.Config{AccessTokenSecret: "payment-cursor-test-secret"}}
	userID := "550e8400-e29b-41d4-a716-446655440000"
	token, err := encodePublicCursor(publicCursor{
		Kind: "my-payments", ExpiresAt: now.Add(time.Hour).Unix(), Query: userID, Scope: "succeeded",
		SortTime: now.UnixNano(), ID: "123e4567-e89b-42d3-a456-426614174000",
	}, server.config.AccessTokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/me/payments?cursor="+token, nil)
	recorder := httptest.NewRecorder()
	if _, _, ok := server.paymentPageInput(recorder, request, "my-payments", userID, "succeeded"); !ok {
		t.Fatalf("valid cursor rejected: %s", recorder.Body.String())
	}
	for _, test := range []struct{ userID, status string }{
		{"650e8400-e29b-41d4-a716-446655440000", "succeeded"},
		{userID, "failed"},
	} {
		recorder = httptest.NewRecorder()
		if _, _, ok := server.paymentPageInput(recorder, request, "my-payments", test.userID, test.status); ok {
			t.Fatalf("cursor escaped binding for user=%s status=%s", test.userID, test.status)
		}
	}
}

func TestStaffQueueCursorAndLimitValidation(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "staff-queue-cursor-test-secret"}}
	actorID := "550e8400-e29b-41d4-a716-446655440000"
	token, err := server.encodeStaffQueueCursor("refund-queue", "requested", actorID, time.Now().UTC(),
		"123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/refunds?limit=100&cursor="+token, nil)
	request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: actorID}))
	recorder := httptest.NewRecorder()
	limit, cursor, ok := server.staffQueuePageInput(recorder, request, "refund-queue", "requested")
	if !ok || limit != 100 || cursor == nil {
		t.Fatalf("valid staff cursor rejected: limit=%d cursor=%+v body=%s", limit, cursor, recorder.Body.String())
	}

	badLimit := httptest.NewRequest(http.MethodGet, "/v1/admin/refunds?limit=many", nil)
	badLimit = badLimit.WithContext(context.WithValue(badLimit.Context(), identityContextKey{}, identity{UserID: actorID}))
	recorder = httptest.NewRecorder()
	if _, _, ok := server.staffQueuePageInput(recorder, badLimit, "refund-queue", "requested"); ok || recorder.Code != http.StatusBadRequest {
		t.Fatal("non-numeric staff queue limit must be rejected")
	}

	otherActor := httptest.NewRequest(http.MethodGet, "/v1/admin/refunds?cursor="+token, nil)
	otherActor = otherActor.WithContext(context.WithValue(otherActor.Context(), identityContextKey{}, identity{
		UserID: "650e8400-e29b-41d4-a716-446655440000",
	}))
	recorder = httptest.NewRecorder()
	if _, _, ok := server.staffQueuePageInput(recorder, otherActor, "refund-queue", "requested"); ok {
		t.Fatal("staff cursor must not be reusable by another operator")
	}
}

var errCountOnlyScanner = errors.New("count-only scanner")

type countOnlyScanner struct {
	t    *testing.T
	want int
}

func (scanner countOnlyScanner) Scan(destinations ...any) error {
	scanner.t.Helper()
	if len(destinations) != scanner.want {
		scanner.t.Fatalf("scan target count=%d, want %d", len(destinations), scanner.want)
	}
	return errCountOnlyScanner
}

func TestPaymentAndVerificationScanColumnCounts(t *testing.T) {
	if _, err := scanPaymentIntent(countOnlyScanner{t, 33}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("unexpected payment scan error: %v", err)
	}
	if _, err := scanRefund(countOnlyScanner{t, 15}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("unexpected refund scan error: %v", err)
	}
	if _, err := scanGameAccountVerification(countOnlyScanner{t, 12}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("unexpected verification scan error: %v", err)
	}
	if _, err := scanGameAccount(countOnlyScanner{t, 11}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("unexpected game-account scan error: %v", err)
	}
}
