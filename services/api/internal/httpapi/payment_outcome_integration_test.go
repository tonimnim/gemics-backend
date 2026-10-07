package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/mpesa"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These DB-gated tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They drive paid entry through
// POST /v1/payments/mpesa/stk-push with a fake Daraja client and the provider
// callback, then check what GET /v1/payments/{id}, GET /v1/me/payments and the
// eligibility preflight tell the payer about the place and any refund.

const (
	paymentFlowCallbackToken = "payment-flow-callback-token-0123456789"
	paymentFlowPhone         = "0712345678"
	paymentFlowFeeMinor      = 10_000
)

// paymentFlowMPesa stands in for Daraja: it accepts every prompt and every
// STK query confirms success.
type paymentFlowMPesa struct {
	mu        sync.Mutex
	initiated []mpesa.InitiateRequest
}

func (provider *paymentFlowMPesa) Initiate(_ context.Context, request mpesa.InitiateRequest) (mpesa.InitiateResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.initiated = append(provider.initiated, request)
	suffix := strconv.Itoa(len(provider.initiated)) + "-" + strings.ToLower(rand.Text())[:10]
	return mpesa.InitiateResponse{MerchantRequestID: "merchant-" + suffix, CheckoutRequestID: "checkout-" + suffix,
		ResponseCode: "0", ResponseDescription: "Success. Request accepted for processing"}, nil
}

func (provider *paymentFlowMPesa) Query(_ context.Context, checkoutRequestID string) (mpesa.QueryResponse, error) {
	return mpesa.QueryResponse{CheckoutRequestID: checkoutRequestID, ResponseCode: "0", ResultCode: "0",
		ResultDesc: "The service request is processed successfully."}, nil
}

func (provider *paymentFlowMPesa) prompts() []mpesa.InitiateRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]mpesa.InitiateRequest(nil), provider.initiated...)
}

type paymentFlowHarness struct {
	Server *Server
	Pool   *pgxpool.Pool
	MPesa  *paymentFlowMPesa
	// Seeded lends its organization, organizer and game to the paid
	// competitions each test opens.
	Seeded integrationCompetition
}

func paymentFlowSetup(t *testing.T) paymentFlowHarness {
	t.Helper()
	pool := openMigratedIntegrationDatabase(t)
	provider := &paymentFlowMPesa{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{AccessTokenSecret: "payment-flow-secret", RequestTimeout: 30 * time.Second,
		MPesaCallbackToken: paymentFlowCallbackToken, MPesaMaxAmountMinor: 1_000_000,
		MPesaUserLimit: 10, MPesaPhoneLimit: 10, MPesaIPLimit: 100,
		MPesaUserWindow: time.Hour, MPesaPhoneWindow: time.Hour, MPesaIPWindow: time.Hour}, logger, "test",
		Dependencies{Database: &database.Cluster{Writer: pool, Reader: pool}, MPesa: provider})
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	return paymentFlowHarness{Server: server, Pool: pool, MPesa: provider, Seeded: seeded}
}

// paymentFlowInsertCompetition opens a paid competition with maxEntries places.
func paymentFlowInsertCompetition(t *testing.T, h paymentFlowHarness, maxEntries int) string {
	t.Helper()
	suffix := strings.ToLower(rand.Text())[:12]
	var competitionID string
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO competitions(organization_id,game_id,name,slug,format,status,
		max_entries,entry_fee_minor,fee_purpose,registration_opens_at,registration_closes_at,starts_at,created_by)
		VALUES ($1,$2,$3,$4,'single_elimination','registration_open',$5,$6,'administration',now()-interval '1 hour',
		 now()+interval '1 hour',now()+interval '2 hours',$7) RETURNING id::text`,
		h.Seeded.OrganizationID, h.Seeded.GameID, "Paid cup "+suffix, "paid-"+suffix, maxEntries, paymentFlowFeeMinor,
		h.Seeded.OrganizerID).Scan(&competitionID); err != nil {
		t.Fatal(err)
	}
	return competitionID
}

// paymentFlowInsertPlayer inserts an onboarded player with an eFootball
// account.
func paymentFlowInsertPlayer(t *testing.T, h paymentFlowHarness, label string) (string, string) {
	t.Helper()
	handle := label + "_" + strings.ToLower(rand.Text())[:10]
	var userID, gameAccountID string
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO users(email,display_name,status,birth_date,
		terms_accepted_at,privacy_accepted_at)
		VALUES ($1||'@gamics.test',$1,'active','2000-01-01',now(),now())
		RETURNING id::text`, handle).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	resultFlowExec(t, h.Pool, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, handle)
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO game_accounts(user_id,game_id,platform,in_game_name)
		VALUES ($1,$2,'android',$3) RETURNING id::text`, userID, h.Seeded.GameID, handle).Scan(&gameAccountID); err != nil {
		t.Fatal(err)
	}
	return userID, gameAccountID
}

// paymentFlowInsertEntry registers a player directly, as an organizer-added
// entry would, taking a place without a payment.
func paymentFlowInsertEntry(t *testing.T, h paymentFlowHarness, competitionID string) {
	t.Helper()
	userID, gameAccountID := paymentFlowInsertPlayer(t, h, "added")
	var entryID string
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO competition_entries(competition_id,display_name,captain_user_id)
		VALUES ($1,'Added player',$2) RETURNING id::text`, competitionID, userID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	resultFlowExec(t, h.Pool, `INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
		VALUES ($1,$2,$3,$4)`, entryID, competitionID, userID, gameAccountID)
}

func paymentFlowRequest(t *testing.T, method, target, userID string, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	return request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
}

func paymentFlowSTKPush(t *testing.T, h paymentFlowHarness, userID, key, competitionID, gameAccountID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"competitionId": competitionID, "gameAccountId": gameAccountID,
		"phoneNumber": paymentFlowPhone})
	if err != nil {
		t.Fatal(err)
	}
	request := paymentFlowRequest(t, http.MethodPost, "/v1/payments/mpesa/stk-push", userID, body)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	h.Server.initiateMPesa(recorder, request)
	return recorder
}

func paymentFlowDecodePayment(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) paymentIntent {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var payment paymentIntent
	if err := json.Unmarshal(recorder.Body.Bytes(), &payment); err != nil {
		t.Fatal(err)
	}
	return payment
}

func paymentFlowStart(t *testing.T, h paymentFlowHarness, userID, gameAccountID, competitionID string) paymentIntent {
	t.Helper()
	return paymentFlowDecodePayment(t, paymentFlowSTKPush(t, h, userID, "stk-"+rand.Text(), competitionID, gameAccountID),
		http.StatusAccepted)
}

func paymentFlowGet(t *testing.T, h paymentFlowHarness, userID, paymentID string) paymentIntent {
	t.Helper()
	return paymentFlowDecodePayment(t, resultReportsGet(t, h.Server.getPayment, userID, "id", paymentID), http.StatusOK)
}

// paymentFlowHistory returns the payer's history row for paymentID.
func paymentFlowHistory(t *testing.T, h paymentFlowHarness, userID, paymentID string) paymentIntent {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.Server.listMyPayments(recorder, paymentFlowRequest(t, http.MethodGet, "/v1/me/payments", userID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("payment history: %d %s", recorder.Code, recorder.Body.String())
	}
	var page paymentHistoryPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, payment := range page.Data {
		if payment.ID == paymentID {
			return payment
		}
	}
	t.Fatalf("payment %s is missing from the payer's history", paymentID)
	return paymentIntent{}
}

func paymentFlowEligibility(t *testing.T, h paymentFlowHarness, userID, competitionID, gameAccountID string) competitionEligibilityResult {
	t.Helper()
	request := paymentFlowRequest(t, http.MethodGet,
		"/v1/competitions/"+competitionID+"/eligibility?gameAccountId="+gameAccountID, userID, nil)
	request.SetPathValue("id", competitionID)
	recorder := httptest.NewRecorder()
	h.Server.getCompetitionEligibility(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("eligibility: %d %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data competitionEligibilityResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

// paymentFlowCallback delivers Daraja's success callback for the payment's
// checkout, with the amount and phone the payment was created with.
func paymentFlowCallback(t *testing.T, h paymentFlowHarness, paymentID string) {
	t.Helper()
	var merchantID, checkoutID, phone string
	var amountMinor int64
	if err := h.Pool.QueryRow(t.Context(), `SELECT merchant_request_id,checkout_request_id,phone_e164,amount_minor
		FROM payment_intents WHERE id=$1`, paymentID).Scan(&merchantID, &checkoutID, &phone, &amountMinor); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"Body":{"stkCallback":{"MerchantRequestID":%q,"CheckoutRequestID":%q,"ResultCode":0,`+
		`"ResultDesc":"The service request is processed successfully.","CallbackMetadata":{"Item":[`+
		`{"Name":"Amount","Value":%d},{"Name":"MpesaReceiptNumber","Value":%q},`+
		`{"Name":"TransactionDate","Value":20260929120000},{"Name":"PhoneNumber","Value":%s}]}}}}`,
		merchantID, checkoutID, amountMinor/100, resultFlowReceipt(), phone)
	request := httptest.NewRequest(http.MethodPost, "/v1/payments/callbacks/stk/token", strings.NewReader(body))
	request.SetPathValue("token", paymentFlowCallbackToken)
	recorder := httptest.NewRecorder()
	h.Server.mpesaCallback(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", recorder.Code, recorder.Body.String())
	}
}

func paymentFlowDecideRefund(t *testing.T, h paymentFlowHarness, refundID, body string) {
	t.Helper()
	request := paymentFlowRequest(t, http.MethodPost, "/v1/admin/refunds/"+refundID+"/decisions",
		h.Seeded.OrganizerID, []byte(body))
	request.SetPathValue("id", refundID)
	request.Header.Set("Idempotency-Key", "refund-"+rand.Text())
	recorder := httptest.NewRecorder()
	h.Server.decideRefund(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("refund decision %s: %d %s", body, recorder.Code, recorder.Body.String())
	}
}

// The first STK push of a player reaches Daraja (it failed with 503 while the
// capacity query had no bind argument), and the verified callback registers
// the payer, which the polled payment and the history both report.
func TestIntegrationPaymentSTKPushRegistersThePayer(t *testing.T) {
	h := paymentFlowSetup(t)
	competitionID := paymentFlowInsertCompetition(t, h, 4)
	payer, gameAccountID := paymentFlowInsertPlayer(t, h, "payer")

	created := paymentFlowDecodePayment(t, paymentFlowSTKPush(t, h, payer, "stk-push-key-1", competitionID, gameAccountID),
		http.StatusAccepted)
	if created.Status != "pending" || created.RegistrationStatus != paymentRegistrationPending ||
		created.EntryID != nil || created.Refund != nil || created.MaskedPhone != "2547*****678" {
		t.Fatalf("unexpected STK response %+v", created)
	}
	if prompts := h.MPesa.prompts(); len(prompts) != 1 || prompts[0].AmountKES != paymentFlowFeeMinor/100 ||
		prompts[0].PhoneNumber != "254712345678" {
		t.Fatalf("unexpected Daraja prompts %+v", prompts)
	}
	replay := paymentFlowDecodePayment(t, paymentFlowSTKPush(t, h, payer, "stk-push-key-1", competitionID, gameAccountID),
		http.StatusOK)
	if replay.ID != created.ID || len(h.MPesa.prompts()) != 1 {
		t.Fatalf("the idempotent replay created another prompt: %+v", replay)
	}
	if polled := paymentFlowGet(t, h, payer, created.ID); polled.Status != "pending" ||
		polled.RegistrationStatus != paymentRegistrationPending {
		t.Fatalf("unexpected pending payment %+v", polled)
	}
	eligibility := paymentFlowEligibility(t, h, payer, competitionID, gameAccountID)
	if eligibility.RequiredAction != "poll_payment" || eligibility.PaymentID == nil || *eligibility.PaymentID != created.ID {
		t.Fatalf("the preflight does not name the payment to poll: %+v", eligibility)
	}

	paymentFlowCallback(t, h, created.ID)
	paid := paymentFlowGet(t, h, payer, created.ID)
	if paid.Status != "succeeded" || paid.RegistrationStatus != paymentRegistrationRegistered ||
		paid.EntryID == nil || paid.Refund != nil || paid.ProviderReceipt == nil {
		t.Fatalf("unexpected paid payment %+v", paid)
	}
	if registered := resultReportsCount(t, h.Pool, `SELECT count(*) FROM competition_entries
		WHERE id=$1 AND competition_id=$2 AND captain_user_id=$3 AND status='registered'`,
		*paid.EntryID, competitionID, payer); registered != 1 {
		t.Fatal("the verified payment did not register the payer")
	}
	if history := paymentFlowHistory(t, h, payer, created.ID); history.RegistrationStatus != paymentRegistrationRegistered ||
		history.Refund != nil {
		t.Fatalf("the history disagrees with the payment: %+v", history)
	}
	other, _ := paymentFlowInsertPlayer(t, h, "other")
	if hidden := resultReportsGet(t, h.Server.getPayment, other, "id", created.ID); hidden.Code != http.StatusNotFound {
		t.Fatalf("another player read the payment: %d %s", hidden.Code, hidden.Body.String())
	}
}

// Unfinished payments hold their places, so a payment that could not fit is
// refused before Daraja is contacted.
func TestIntegrationPaymentSTKPushHoldsPlacesForUnfinishedPayments(t *testing.T) {
	h := paymentFlowSetup(t)
	competitionID := paymentFlowInsertCompetition(t, h, 2)
	for _, label := range []string{"first", "second"} {
		userID, gameAccountID := paymentFlowInsertPlayer(t, h, label)
		paymentFlowStart(t, h, userID, gameAccountID, competitionID)
	}
	late, lateAccount := paymentFlowInsertPlayer(t, h, "late")
	refused, _ := entryFlowIneligible(t, paymentFlowSTKPush(t, h, late, "stk-push-late", competitionID, lateAccount))
	if refused.Issue != entryIssueCompetitionFull || refused.Eligibility.firstBlockingIssue() == nil {
		t.Fatalf("a payment beyond capacity was refused as %+v", refused)
	}
	if prompts := len(h.MPesa.prompts()); prompts != 2 {
		t.Fatalf("%d Daraja prompts, want 2", prompts)
	}
	if intents := resultReportsCount(t, h.Pool, `SELECT count(*) FROM payment_intents WHERE user_id=$1`, late); intents != 0 {
		t.Fatal("the refused payment left an intent behind")
	}
}

// A verified payment that completes after its place is gone still reads
// succeeded, and registrationStatus and refund say it is being returned, then
// that it was.
func TestIntegrationPaymentLateCompletionReportsTheRefund(t *testing.T) {
	h := paymentFlowSetup(t)
	tests := []struct {
		name       string
		after      func(t *testing.T, competitionID string)
		wantReason string
		wantNote   string
	}{
		{"capacity filled", func(t *testing.T, competitionID string) {
			paymentFlowInsertEntry(t, h, competitionID)
			paymentFlowInsertEntry(t, h, competitionID)
		}, "operations_adjustment", "capacity"},
		{"competition cancelled", func(t *testing.T, competitionID string) {
			resultFlowExec(t, h.Pool, `UPDATE competitions SET status='cancelled' WHERE id=$1`, competitionID)
		}, "competition_cancelled", "cancelled"},
		{"registration over", func(t *testing.T, competitionID string) {
			resultFlowExec(t, h.Pool, `UPDATE competitions SET status='running' WHERE id=$1`, competitionID)
		}, "operations_adjustment", "registration could join"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			competitionID := paymentFlowInsertCompetition(t, h, 2)
			payer, gameAccountID := paymentFlowInsertPlayer(t, h, "payer")
			created := paymentFlowStart(t, h, payer, gameAccountID, competitionID)
			test.after(t, competitionID)
			paymentFlowCallback(t, h, created.ID)

			owed := paymentFlowGet(t, h, payer, created.ID)
			if owed.Status != "succeeded" || owed.RegistrationStatus != paymentRegistrationRefundPending ||
				owed.EntryID == nil || owed.Refund == nil {
				t.Fatalf("a late payment reads as %+v", owed)
			}
			refund := owed.Refund
			if !refund.Mandatory || refund.Status != "requested" || refund.ReasonCode != test.wantReason ||
				refund.AmountMinor != paymentFlowFeeMinor || refund.PaymentID != created.ID ||
				refund.EntryID != *owed.EntryID || refund.CompletedAt != nil ||
				!strings.Contains(refund.PlayerNote, test.wantNote) {
				t.Fatalf("unexpected refund %+v", refund)
			}
			if pending := resultReportsCount(t, h.Pool, `SELECT count(*) FROM competition_entries
				WHERE id=$1 AND status='withdrawal_pending'`, *owed.EntryID); pending != 1 {
				t.Fatal("the late payment's entry is not withdrawal_pending")
			}
			if history := paymentFlowHistory(t, h, payer, created.ID); history.RegistrationStatus != paymentRegistrationRefundPending ||
				history.Refund == nil || history.Refund.ID != refund.ID {
				t.Fatalf("the history disagrees with the payment: %+v", history)
			}

			paymentFlowDecideRefund(t, h, refund.ID, `{"decision":"approve"}`)
			if approved := paymentFlowGet(t, h, payer, created.ID); approved.RegistrationStatus != paymentRegistrationRefundPending ||
				approved.Refund == nil || approved.Refund.Status != "approved" {
				t.Fatalf("an approved refund reads as %+v", approved)
			}
			paymentFlowDecideRefund(t, h, refund.ID, `{"decision":"mark_succeeded","providerReceipt":"`+resultFlowReceipt()+`"}`)
			refunded := paymentFlowGet(t, h, payer, created.ID)
			if refunded.Status != "succeeded" || refunded.RegistrationStatus != paymentRegistrationRefunded ||
				refunded.Refund == nil || refunded.Refund.Status != "succeeded" || refunded.Refund.CompletedAt == nil {
				t.Fatalf("a paid-out refund reads as %+v", refunded)
			}
		})
	}
}
