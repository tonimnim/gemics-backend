package httpapi

import (
	"strings"
	"testing"
)

// Cancelling a competition ends its live matches in the same transaction and
// under the competition gate, before the cancellation refunds rewrite entries.
func TestCompetitionCancellationCancelsMatchesUnderTheGate(t *testing.T) {
	transition := resultReportsFunctionSource(t, "organizer_competition_handlers.go",
		"func (s *Server) transitionOrganizerCompetition(")
	assertOrder(t, "transitionOrganizerCompetition", transition,
		"probeOrganizerCompetition(r.Context(), tx, membership.OrganizationID, competitionID)",
		"lockCompetitionProgressionGate(r.Context(), tx, competitionID)", "lockOrganizerCompetition(",
		"UPDATE competitions SET status=$2", "target == competition.StatusCancelled",
		"cancelCompetitionMatches(", "createCompetitionCancellationRefunds(", "tx.Commit(r.Context())",
		"s.invalidateCompetitionCaches(r, competitionID)")
}

func TestCancelCompetitionMatchesIsNeutral(t *testing.T) {
	cancel := resultReportsFunctionSource(t, "competition_cancellation.go", "func cancelCompetitionMatches(")
	assertOrder(t, "cancelCompetitionMatches", cancel,
		"SELECT statement_timestamp()", "lockLiveCompetitionMatches(",
		"completion_reason='competition_cancelled',completed_at=$2", "id = ANY($3::text[]::uuid[])",
		"command.RowsAffected() != int64(len(matchIDs))",
		"resolution='competition_cancelled',resolved_at=$2", "WHERE competition_id=$1 AND match_id = ANY($3::text[]::uuid[]) AND phase<>'resolved'",
		"SET status='closed',decided_at=$2", "WHERE competition_id=$1 AND status='queued'",
		`"competition.matches_cancelled"`)
	// No removal, strike, rating, progression or per-match push (T17).
	for _, forbidden := range []string{"removeEntriesFromTournament", "player_strikes", "applyConfirmedResultRatings",
		"applyMatchProgression", "insertProgressionOutbox", "finalizeMatchResolution"} {
		if strings.Contains(cancel, forbidden) {
			t.Fatalf("competition cancellation calls %s", forbidden)
		}
	}
	locks := resultReportsFunctionSource(t, "competition_cancellation.go", "func lockLiveCompetitionMatches(")
	if !strings.Contains(locks, "state IN ('pending','ready','in_progress','awaiting_confirmation','disputed')") ||
		!strings.Contains(locks, "ORDER BY id FOR UPDATE") {
		t.Fatal("cancellation must lock every live match in id order")
	}
}

// Free withdrawal locks the entry and the competition row, so it takes the
// competition gate first like every result finalizer, but only after an
// unlocked probe proves the caller has an entry (D28).
func TestFreeWithdrawalTakesTheCompetitionGateFirst(t *testing.T) {
	withdrawal := resultReportsFunctionSource(t, "competition_handlers.go", "func (s *Server) withdrawRegistration(")
	assertOrder(t, "withdrawRegistration", withdrawal,
		"s.db.Writer.Begin(", "probeCompetitionEntry(r.Context(), tx, competitionID, userID)",
		"lockCompetitionProgressionGate(r.Context(), tx, competitionID)", "FOR UPDATE OF entry,competition")
	probe := resultReportsFunctionSource(t, "competition_handlers.go", "func probeCompetitionEntry(")
	if strings.Contains(probe, "FOR UPDATE") || !strings.Contains(probe, "captain_user_id=$2") {
		t.Fatal("the withdrawal probe must be an unlocked membership check")
	}
}

// A transition from another organization is refused before it can queue on
// the competition gate (D28).
func TestCompetitionTransitionProbesOwnershipBeforeTheGate(t *testing.T) {
	probe := resultReportsFunctionSource(t, "organizer_competition_handlers.go", "func probeOrganizerCompetition(")
	if strings.Contains(probe, "FOR UPDATE") || !strings.Contains(probe, "organization_id=$2") {
		t.Fatal("the transition probe must be an unlocked ownership check")
	}
}
