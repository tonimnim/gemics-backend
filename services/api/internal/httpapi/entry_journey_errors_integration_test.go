package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// These DB-gated tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They check that free
// registration and M-Pesa checkout report a blocked entry with one body.

func entryFlowFreeRegistration(t *testing.T, h paymentFlowHarness, userID, competitionID, gameAccountID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"gameAccountId": gameAccountID})
	if err != nil {
		t.Fatal(err)
	}
	request := paymentFlowRequest(t, http.MethodPost, "/v1/competitions/"+competitionID+"/registrations", userID, body)
	request.SetPathValue("id", competitionID)
	request.Header.Set("Idempotency-Key", "free-"+rand.Text())
	recorder := httptest.NewRecorder()
	h.Server.createFreeRegistration(recorder, request)
	return recorder
}

// entryFlowIneligible decodes a competition_ineligible body and returns it
// with its sorted top-level keys.
func entryFlowIneligible(t *testing.T, recorder *httptest.ResponseRecorder) (competitionIneligibleError, []string) {
	t.Helper()
	fields := entryJourneyErrorFields(t, recorder)
	var body competitionIneligibleError
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "competition_ineligible" || body.Message != body.Issue.Message || body.Eligibility.Eligible {
		t.Fatalf("unexpected ineligible body %s", recorder.Body.String())
	}
	return body, slices.Sorted(maps.Keys(fields))
}

func TestIntegrationFreeAndPaidEntryReportIneligibilityAlike(t *testing.T) {
	h := paymentFlowSetup(t)
	player, gameAccountID := paymentFlowInsertPlayer(t, h, "blocked")
	tests := []struct {
		name, block, wantCode string
	}{
		{"country not allowed", `UPDATE competitions SET rules_snapshot='{"eligibility":{"allowedCountries":["UG"]}}'::jsonb
			WHERE id::text=ANY($1::text[])`, "country_not_allowed"},
		{"registration closed", `UPDATE competitions SET registration_closes_at=now()-interval '1 minute'
			WHERE id::text=ANY($1::text[])`, "registration_closed"},
		{"competition running", `UPDATE competitions SET status='running' WHERE id::text=ANY($1::text[])`,
			"registration_closed"},
		{"competition cancelled", `UPDATE competitions SET status='cancelled' WHERE id::text=ANY($1::text[])`,
			"competition_cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			free := paymentFlowInsertCompetition(t, h, 4)
			resultFlowExec(t, h.Pool, `UPDATE competitions SET entry_fee_minor=0,fee_purpose='none' WHERE id=$1`, free)
			paid := paymentFlowInsertCompetition(t, h, 4)
			resultFlowExec(t, h.Pool, test.block, []string{free, paid})

			freeBody, freeKeys := entryFlowIneligible(t, entryFlowFreeRegistration(t, h, player, free, gameAccountID))
			paidBody, paidKeys := entryFlowIneligible(t,
				paymentFlowSTKPush(t, h, player, "stk-"+rand.Text(), paid, gameAccountID))
			if !slices.Equal(freeKeys, paidKeys) || freeBody.Issue != paidBody.Issue || freeBody.Issue.Code != test.wantCode {
				t.Fatalf("free %v %+v and paid %v %+v report the entry differently", freeKeys, freeBody.Issue,
					paidKeys, paidBody.Issue)
			}
			if freeBody.Eligibility.CompetitionID != free || paidBody.Eligibility.CompetitionID != paid {
				t.Fatal("the decision is not the competition's own")
			}
		})
	}
	if prompts := len(h.MPesa.prompts()); prompts != 0 {
		t.Fatalf("an ineligible player reached Daraja %d times", prompts)
	}
	if intents := resultReportsCount(t, h.Pool, `SELECT count(*) FROM payment_intents WHERE user_id=$1`, player); intents != 0 {
		t.Fatal("an ineligible payment left an intent behind")
	}
}

// A second payment attempt names the payment in progress, and a retry with the
// first key replays it even after registration has closed.
func TestIntegrationPaymentInProgressNamesThePayment(t *testing.T) {
	h := paymentFlowSetup(t)
	competitionID := paymentFlowInsertCompetition(t, h, 4)
	payer, gameAccountID := paymentFlowInsertPlayer(t, h, "payer")
	created := paymentFlowDecodePayment(t, paymentFlowSTKPush(t, h, payer, "stk-first-key", competitionID, gameAccountID),
		http.StatusAccepted)

	again := paymentFlowSTKPush(t, h, payer, "stk-second-key", competitionID, gameAccountID)
	var inProgress paymentInProgressError
	if err := json.Unmarshal(again.Body.Bytes(), &inProgress); err != nil {
		t.Fatal(err)
	}
	if again.Code != http.StatusConflict || inProgress.Error != "payment_in_progress" ||
		inProgress.PaymentID != created.ID || inProgress.PaymentStatus != "pending" {
		t.Fatalf("a second attempt reads as %d %s", again.Code, again.Body.String())
	}

	resultFlowExec(t, h.Pool, `UPDATE competitions SET registration_closes_at=now()-interval '1 minute' WHERE id=$1`, competitionID)
	replay := paymentFlowDecodePayment(t, paymentFlowSTKPush(t, h, payer, "stk-first-key", competitionID, gameAccountID),
		http.StatusOK)
	if replay.ID != created.ID || len(h.MPesa.prompts()) != 1 {
		t.Fatalf("the retry did not replay the payment: %+v", replay)
	}
}
