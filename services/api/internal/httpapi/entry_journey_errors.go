package httpapi

import "net/http"

// The entry journey reports a blocked entry in one shape. Free registration
// and M-Pesa checkout both answer 409 competition_ineligible with the first
// blocking issue, worded as the eligibility preflight words it, and the full
// decision, so a client switches on issue.code alone.

type competitionIneligibleError struct {
	Error       string                       `json:"error"`
	Message     string                       `json:"message"`
	Issue       eligibilityIssue             `json:"issue"`
	Eligibility competitionEligibilityResult `json:"eligibility"`
}

// paymentInProgressError names the payer's existing payment, so a client that
// lost it resumes polling instead of starting another.
type paymentInProgressError struct {
	Error         string `json:"error"`
	Message       string `json:"message"`
	PaymentID     string `json:"paymentId"`
	PaymentStatus string `json:"paymentStatus"`
}

// Issues a writer can meet again in its own statements after the shared
// decision passed, worded exactly as assessCompetitionEligibility words them.
var (
	entryIssueProfileIncomplete = eligibilityIssue{Code: "profile_incomplete", Category: "profile",
		Severity: "blocking", Message: "Accept the current terms and privacy notice to enter competitions."}
	entryIssueRegistrationNotReusable = eligibilityIssue{Code: "registration_not_reusable", Category: "registration",
		Severity: "blocking", Message: "This player cannot create another entry for this competition."}
	entryIssueCompetitionFull = eligibilityIssue{Code: "competition_full", Category: "capacity",
		Severity: "blocking", Message: "This competition has no available entries."}
	entryIssueGameAccountRequired = eligibilityIssue{Code: "game_account_required", Category: "game_account",
		Severity: "blocking", Message: "Choose a connected game account."}
	entryIssueGameAccountMismatch = eligibilityIssue{Code: "game_account_game_mismatch", Category: "game_account",
		Severity: "blocking", Message: "Choose a game account for this competition's game."}
)

// writeCompetitionIneligible answers with the blocking issue that decided an
// ineligible entry.
func writeCompetitionIneligible(w http.ResponseWriter, eligibility competitionEligibilityResult, issue eligibilityIssue) {
	writeJSON(w, http.StatusConflict, competitionIneligibleError{Error: "competition_ineligible", Message: issue.Message,
		Issue: issue, Eligibility: eligibility})
}

// writeBlockedEntry reports an issue a writer found after the shared decision
// passed, with the decision updated to carry it.
func writeBlockedEntry(w http.ResponseWriter, eligibility competitionEligibilityResult, issue eligibilityIssue) {
	writeCompetitionIneligible(w, eligibility.blockedBy(issue), issue)
}

// blockedBy returns the decision with issue added as a blocking issue. As in
// assessCompetitionEligibility, an ineligible decision asks for no action
// except polling a payment that has yet to settle.
func (result competitionEligibilityResult) blockedBy(issue eligibilityIssue) competitionEligibilityResult {
	action := "none"
	issues := make([]eligibilityIssue, 0, len(result.Issues)+1)
	for _, existing := range result.Issues {
		switch {
		case existing.Severity == "blocking":
			issues = append(issues, existing)
		case existing.Code == "payment_pending":
			action = "poll_payment"
		}
	}
	result.Issues = append(issues, issue)
	result.Status, result.Eligible = eligibilityStatusIneligible, false
	result.CanRegisterNow, result.RequiredAction = false, action
	return result
}

func writePaymentInProgress(w http.ResponseWriter, paymentID, paymentStatus string) {
	writeJSON(w, http.StatusConflict, paymentInProgressError{Error: "payment_in_progress",
		Message: "A payment already exists for this competition.", PaymentID: paymentID, PaymentStatus: paymentStatus})
}
