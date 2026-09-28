package httpapi

import (
	"strings"
	"testing"
)

func TestNormalizeKenyanPhone(t *testing.T) {
	for _, input := range []string{"0712 345 678", "+254712345678", "254112345678"} {
		value, ok := normalizeKenyanPhone(input)
		if !ok || len(value) != 12 {
			t.Fatalf("expected %q to normalize, got %q %v", input, value, ok)
		}
	}
	for _, input := range []string{"", "254212345678", "07123"} {
		if _, ok := normalizeKenyanPhone(input); ok {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestNumericMPesaCallbackMetadataPreservesDigits(t *testing.T) {
	raw := []byte(`{"Body":{"stkCallback":{"MerchantRequestID":"merchant-1","CheckoutRequestID":"checkout-1","ResultCode":0,"ResultDesc":"Success","CallbackMetadata":{"Item":[{"Name":"Amount","Value":250.00},{"Name":"MpesaReceiptNumber","Value":"ABC123"},{"Name":"TransactionDate","Value":20260809112233},{"Name":"PhoneNumber","Value":254712345678}]}}}}`)
	var callback callbackEnvelope
	if err := decodeMPesaJSON(raw, &callback); err != nil {
		t.Fatal(err)
	}
	metadata := callbackMetadata(callback.Body.STKCallback.CallbackMetadata.Items)
	if metadata["PhoneNumber"] != "254712345678" || metadata["TransactionDate"] != "20260809112233" || metadata["Amount"] != "250.00" {
		t.Fatalf("numeric metadata lost precision: %+v", metadata)
	}
	minor, err := parseKESMinor(metadata["Amount"])
	if err != nil || minor != 25_000 {
		t.Fatalf("unexpected parsed amount: %d %v", minor, err)
	}
}

func TestPaymentQueryBackoffIsBounded(t *testing.T) {
	if paymentQueryBackoff(0).Seconds() != 10 || paymentQueryBackoff(100).Minutes() != 5 {
		t.Fatal("unexpected query backoff bounds")
	}
}

func TestPaymentVelocityFallbackCountsCurrentIntent(t *testing.T) {
	if !paymentVelocityCountAllowed(5, 5) {
		t.Fatal("the configured Nth request should be allowed")
	}
	if paymentVelocityCountAllowed(6, 5) || paymentVelocityCountAllowed(0, 0) {
		t.Fatal("request N+1 and non-positive limits must be rejected")
	}
}

func TestConductSuspendedPaymentRefundsNewEntriesOnly(t *testing.T) {
	registered, disqualified := "registered", "disqualified"
	tests := []struct {
		name       string
		status     string
		draw, full bool
		existing   *string
		inDraw     bool
		suspended  bool
		wantStatus string
		wantReason string
		wantNote   string
	}{
		{name: "new entry at the strike limit", status: "registration_open", suspended: true,
			wantStatus: "withdrawal_pending", wantReason: "operations_adjustment", wantNote: "conduct strike limit"},
		{name: "new entry below the strike limit", status: "registration_open", wantStatus: "registered"},
		{name: "existing entry keeps its place", status: "registration_open", existing: &registered, suspended: true,
			wantStatus: "registered"},
		{name: "drawn entry keeps its place", status: "running", draw: true, existing: &registered, inDraw: true,
			suspended: true, wantStatus: "registered"},
		{name: "removed entry is refunded as inactive", status: "running", draw: true, existing: &disqualified,
			inDraw: true, suspended: true, wantStatus: "withdrawal_pending", wantReason: "operations_adjustment",
			wantNote: "inactive entry"},
		{name: "cancellation keeps its mandatory reason", status: "cancelled", suspended: true,
			wantStatus: "withdrawal_pending", wantReason: "competition_cancelled", wantNote: "cancelled"},
		{name: "capacity refund keeps its reason", status: "registration_open", full: true, suspended: true,
			wantStatus: "withdrawal_pending", wantReason: "operations_adjustment", wantNote: "capacity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := planConductSuspendedPayment(
				planVerifiedPaymentRegistration(test.status, test.draw, test.full, test.existing, test.inDraw),
				test.existing, test.suspended)
			if plan.EntryStatus != test.wantStatus || plan.RefundReason != test.wantReason ||
				!strings.Contains(plan.RefundNote, test.wantNote) {
				t.Fatalf("plan=%+v, want status=%s reason=%s note~%q", plan, test.wantStatus, test.wantReason, test.wantNote)
			}
		})
	}
}

// Writers that lock a drawn competition's entries or competition row take the
// competition gate first, or they could deadlock with a result finalizer that
// removes an entry (gate -> match -> ... -> entries -> competition).
func TestPaymentWritersTakeTheCompetitionGateFirst(t *testing.T) {
	complete := resultReportsFunctionSource(t, "payment_handlers.go", "func (s *Server) completePayment(")
	assertOrder(t, "completePayment", complete,
		"`SELECT competition_id FROM payment_intents WHERE id=$1`", "lockCompetitionProgressionGate(ctx, tx, gateCompetitionID)",
		"FROM payment_intents WHERE id=$1 FOR UPDATE", "competitionID != gateCompetitionID",
		"FROM competitions WHERE id=$1 FOR UPDATE", "FROM competition_entries",
		"activePlayerStrikesSQL", "planConductSuspendedPayment(")
	if !strings.Contains(complete, "SET status='withdrawal_pending',updated_at=now()\n\t\t\tWHERE id=$1 AND status NOT IN ('withdrawn','disqualified')") {
		t.Fatal("a late payment can revive a withdrawn or removed entry")
	}
	withdrawal := resultReportsFunctionSource(t, "payment_lifecycle_handlers.go", "func (s *Server) requestPaidWithdrawal(")
	assertOrder(t, "requestPaidWithdrawal", withdrawal,
		"pg_advisory_xact_lock(", "beginIdempotentRequest(", "probePaidCompetitionEntry(r.Context(), tx, competitionID, userID)",
		"lockCompetitionProgressionGate(r.Context(), tx, competitionID)", "FOR UPDATE OF entry,competition,payment")
	probe := resultReportsFunctionSource(t, "payment_lifecycle_handlers.go", "func probePaidCompetitionEntry(")
	if strings.Contains(probe, "FOR UPDATE") || !strings.Contains(probe, "payment.status='succeeded'") {
		t.Fatal("the paid withdrawal probe must be an unlocked paid-entry check (D28)")
	}
	refund := resultReportsFunctionSource(t, "payment_lifecycle_handlers.go", "func (s *Server) decideRefund(")
	assertOrder(t, "decideRefund", refund,
		"beginIdempotentRequest(", "SELECT payment.competition_id::text FROM payment_refunds refund",
		"lockCompetitionProgressionGate(r.Context(), tx, gateCompetitionID)", "WHERE refund.id=$1 FOR UPDATE",
		"competitionID != gateCompetitionID", "SET status='withdrawn',updated_at=now()\n\t\t\tWHERE id=$1 AND status<>'disqualified'")
}
