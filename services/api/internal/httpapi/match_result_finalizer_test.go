package httpapi

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// resultReportsFunctionSource returns one top-level function's source, from its
// declaration to the next top-level declaration.
func resultReportsFunctionSource(t *testing.T, path, declaration string) string {
	t.Helper()
	contents := readSourceFile(t, path)
	start := strings.Index(contents, declaration)
	if start < 0 {
		t.Fatalf("%s does not declare %q", path, declaration)
	}
	body := contents[start:]
	if end := strings.Index(body[len(declaration):], "\n}\n"); end >= 0 {
		body = body[:len(declaration)+end+3]
	}
	return body
}

func TestScoreReportHandlersTakeTheGateBeforeTheMatch(t *testing.T) {
	record := resultReportsFunctionSource(t, "match_result_reports.go", "func (s *Server) recordScoreReport(")
	assertOrder(t, "recordScoreReport", record,
		"hashRequest(input)", "beginIdempotentRequest(", "lockScoreReportState(", "apply(ctx, tx, state,",
		"finishIdempotentRequest(", "tx.Commit(ctx)",
		"s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), state.Match.CompetitionID)")
	locks := resultReportsFunctionSource(t, "match_result_reports.go", "func lockScoreReportState(")
	assertOrder(t, "lockScoreReportState", locks,
		"lookupMatchCompetition(", "lockCompetitionProgressionGate(", "lockResultMatch(", "ActorEntryID == \"\"",
		"lockResultVerification(", "lockResultReports(")
	for _, handler := range []string{"func (s *Server) createScoreReport(", "func (s *Server) createFinalScoreReport("} {
		source := resultReportsFunctionSource(t, "match_result_reports.go", handler)
		assertOrder(t, handler, source, "readIdempotencyKey(", "uuidPattern.MatchString", "decodeJSON(", "s.recordScoreReport(")
	}
	initial := resultReportsFunctionSource(t, "match_result_reports.go", "func applyInitialScoreReport(")
	assertOrder(t, "applyInitialScoreReport", initial,
		"normalizeScoreClaim(", "planScoreReport(", "openResultVerification(", "insertScoreReport(", "finalizeMatchResolution(")
	final := resultReportsFunctionSource(t, "match_result_reports.go", "func applyFinalScoreReport(")
	assertOrder(t, "applyFinalScoreReport", final,
		"normalizeScoreClaim(", "planFinalReport(", "lockCompletedEvidence(", "insertScoreReport(",
		"attachFinalReportEvidence(", "finalizeMatchResolution(", "queueResultReview(", "bumpDisputedMatch(")
}

func TestFinalizeMatchResolutionOrder(t *testing.T) {
	source := resultReportsFunctionSource(t, "match_result_finalizer.go", "func finalizeMatchResolution(")
	assertOrder(t, "finalizeMatchResolution", source,
		"validateMatchResolution(", "UPDATE matches SET state=$1", "WHERE id=$5 AND competition_id=$6 AND version=$7 AND state=$8",
		"removeEntriesFromTournament(", "insertCanonicalResult(", "UPDATE match_result_verifications SET phase='resolved'",
		"WHERE match_id=$1 AND version=$5 AND phase=$6", "applyMatchProgression(", "applyConfirmedResultRatings(",
		"writeResolutionAuditAndOutbox(")
	if !strings.Contains(source, "if actor.Kind == \"staff\" {\n\t\tprogression.ActorUserID = actor.UserID") {
		t.Error("progression must record only a staff actor")
	}
	removal := resultReportsFunctionSource(t, "match_result_finalizer.go", "func removeEntriesFromTournament(")
	assertOrder(t, "removeEntriesFromTournament", removal,
		"actor.removalActor()", "lockRemovedEntries(", "disqualifyEntry(", "\"competition.entry_removed\"",
		"insertProgressionOutbox(", "AND id<>$2", "errEntryRemovalInvariant")
	assertFileContains(t, "match_result_finalizer.go",
		"ORDER BY id FOR UPDATE",
		"status IN ('registered','checked_in','accepted','withdrawal_pending')",
		"ON CONFLICT (entry_id) DO NOTHING",
		"ORDER BY entry_id,kind FOR UPDATE",
		"FROM match_result_verifications WHERE match_id=$1 FOR UPDATE")
}

func TestLockResultMatchReadsTheClockAfterTheGate(t *testing.T) {
	source := resultReportsFunctionSource(t, "match_result_finalizer.go", "func lockResultMatch(")
	for _, fragment := range []string{"statement_timestamp()", "FOR UPDATE OF match", "competition.status",
		"member.roster_role IN ('starter','substitute')", "WHERE match.id=$1 AND match.competition_id=$2"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("lockResultMatch does not contain %q", fragment)
		}
	}
	if strings.Contains(source, "now()") {
		t.Error("lockResultMatch must not use the transaction-start clock")
	}
	probe := resultReportsFunctionSource(t, "match_result_finalizer.go", "func lookupMatchCompetition(")
	if strings.Contains(probe, "FOR UPDATE") || !strings.Contains(probe, "member.roster_role IN ('starter','substitute')") {
		t.Error("the membership probe must be unlocked and exclude coaches")
	}
}

func TestResultWritesAreVersionAndPhaseGuarded(t *testing.T) {
	for _, check := range []struct {
		path, declaration string
		fragments         []string
	}{
		{"match_result_reports.go", "func startScoreReportWindow(", []string{"AND version=$3 AND state='in_progress'"}},
		{"match_result_reports.go", "func openScoreMismatch(", []string{
			"WHERE match_id=$1 AND phase='awaiting_second_report' AND version=$4",
			"AND version=$3 AND state='awaiting_confirmation'"}},
		{"match_result_finalizer.go", "func bumpDisputedMatch(", []string{"AND version=$3 AND state='disputed'"}},
		{"match_result_finalizer.go", "func queueResultReview(", []string{
			"WHERE match_id=$1 AND phase='awaiting_responses' AND version=$2", "bumpDisputedMatch("}},
		{"match_result_verification_worker.go", "func sendScoreReportReminder(", []string{
			"AND phase='awaiting_second_report' AND reminder_sent_at IS NULL", "AND report_deadline_at>$2",
			"command.RowsAffected() != 1"}},
		{"match_result_reports.go", "func attachFinalReportEvidence(", []string{
			"WHERE id=$2 AND bound_id IS NULL AND status='completed'", "command.RowsAffected() != 1"}},
	} {
		source := resultReportsFunctionSource(t, check.path, check.declaration)
		for _, fragment := range check.fragments {
			if !strings.Contains(source, fragment) {
				t.Errorf("%s does not contain %q", check.declaration, fragment)
			}
		}
	}
	guard := resultReportsFunctionSource(t, "match_result_finalizer.go", "func updateResultVersion(")
	if !strings.Contains(guard, "errors.Is(err, pgx.ErrNoRows)") || !strings.Contains(guard, "errMatchResolutionChanged") {
		t.Error("a guarded update that matches no row must fail with errMatchResolutionChanged")
	}
}

func TestReviewersCanOpenTheUploadsBehindAnEvidenceUnavailableReview(t *testing.T) {
	source := resultReportsFunctionSource(t, "result_handlers.go", "func (s *Server) getEvidenceAccess(")
	if !strings.Contains(source, `reviewWindowUploadClause("evidence", "$2")`) {
		t.Fatal("getEvidenceAccess does not reach the unbound uploads behind an evidence_unavailable review")
	}
	clause := reviewWindowUploadClause("evidence", "$2")
	for _, fragment := range []string{
		"evidence.bound_id IS NULL",
		"window_review.reason='evidence_unavailable'",
		"evidence.created_at >= window_verification.mismatch_at",
		"evidence.created_at < window_verification.response_deadline_at",
		"window_member.roster_role IN ('starter','substitute')",
		"window_final.kind='final'",
		"AND NOT " + resultReviewConflictClause("window_match", "$2"),
	} {
		if !strings.Contains(clause, fragment) {
			t.Errorf("reviewWindowUploadClause does not contain %q", fragment)
		}
	}
	if strings.Contains(clause, "{") {
		t.Error("reviewWindowUploadClause left a placeholder unreplaced")
	}
	listing := resultReportsFunctionSource(t, "match_result_finalizer.go", "func loadResponseWindowUploads(")
	for _, fragment := range []string{"evidence.bound_id IS NULL", "evidence.created_at >= $2 AND evidence.created_at < $3",
		"member.roster_role IN ('starter','substitute')"} {
		if !strings.Contains(listing, fragment) {
			t.Errorf("the review listing and the access rule disagree on %q", fragment)
		}
	}
}

func TestEvidenceAccessIsOwnerOrUnconflictedReviewer(t *testing.T) {
	source := resultReportsFunctionSource(t, "result_handlers.go", "func (s *Server) getEvidenceAccess(")
	for _, fragment := range []string{"evidence.owner_user_id=$2", "evidence.media_kind='image'",
		`resultReviewConflictClause("bound_match", "$2")`, "s.db.Writer.QueryRow"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("getEvidenceAccess does not contain %q", fragment)
		}
	}
	for _, fragment := range []string{"organization_members", "referee", "disputes", "roster_role"} {
		if strings.Contains(source, fragment) {
			t.Errorf("getEvidenceAccess still grants access through %q", fragment)
		}
	}
	roles := regexp.MustCompile(`staff\.role IN \(([^)]*)\)`).FindStringSubmatch(source)
	if roles == nil {
		t.Fatal("getEvidenceAccess has no staff role list")
	}
	granted := make([]string, 0, 2)
	for _, role := range strings.Split(roles[1], ",") {
		granted = append(granted, strings.Trim(strings.TrimSpace(role), "'"))
	}
	slices.Sort(granted)
	want := make([]string, 0, 2)
	for _, role := range []string{"admin", "analyst", "owner", "reviewer", "support"} {
		if platformRoleCan(role, platformResultReviewManage) {
			want = append(want, role)
		}
	}
	if !slices.Equal(granted, want) {
		t.Fatalf("evidence staff roles %v differ from the result review grants %v", granted, want)
	}
}

func TestResultReviewConflictClauseCoversPlayersCaptainsAndOrganizers(t *testing.T) {
	clause := resultReviewConflictClause("m", "$7")
	for _, fragment := range []string{
		"conflict_member.entry_id IN (m.home_entry_id,m.away_entry_id)", "conflict_member.user_id=$7",
		"conflict_entry.id IN (m.home_entry_id,m.away_entry_id)", "conflict_entry.captain_user_id=$7",
		"conflict_competition.id=m.competition_id", "conflict_org_member.user_id=$7",
	} {
		if !strings.Contains(clause, fragment) {
			t.Errorf("conflict clause does not contain %q", fragment)
		}
	}
	if strings.Contains(clause, "{") || !strings.HasPrefix(clause, "(") || !strings.HasSuffix(clause, ")") {
		t.Fatalf("conflict clause is not a closed expression: %s", clause)
	}
}

func TestOnlyOneEvidenceLockerAndRatingsKeepTheirSequence(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	lockers := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		lockers += strings.Count(string(contents), "func lockCompletedEvidence(")
		if strings.Contains(string(contents), "lockReportEvidence") || strings.Contains(string(contents), "lockRequestedEvidence") {
			t.Errorf("%s declares a second evidence locker", file)
		}
	}
	if lockers != 1 {
		t.Fatalf("found %d evidence lockers, want exactly one", lockers)
	}
	assertFileContains(t, "result_handlers.go",
		"rating_version=nextval('player_game_rating_change_seq')",
		"ORDER BY user_id FOR UPDATE",
		"WHERE id = ANY($1::text[]::uuid[]) AND owner_user_id=$2 ORDER BY id FOR UPDATE")
	assertFileOmits(t, "result_handlers.go", "lockedSubmission", "resultSubmissionView", "resultMatchStateView", "submitResultInput")
}

func TestMatchRoomQueryIsBlindToTheOtherEntry(t *testing.T) {
	// Only booleans may leave the other entry's reports; every other report
	// read is filtered to the viewer's own entry.
	if got := strings.Count(matchSelectColumns, "other."); got != 4 {
		t.Fatalf("matchSelectColumns reads other.* %d times, want 4", got)
	}
	for _, fragment := range []string{
		"COALESCE(bool_or(other.kind='initial'),false) AS reported",
		"COALESCE(bool_or(other.kind='final'),false) AS responded",
		"FROM match_result_reports other WHERE other.match_id=m.id AND other.entry_id<>mine.entry_id",
		"WHERE report.match_id=m.id AND report.entry_id=mine.entry_id",
		"WHERE m.state='completed' AND submission.match_id=m.id AND submission.status='confirmed'",
	} {
		if !strings.Contains(matchSelectColumns, fragment) {
			t.Errorf("matchSelectColumns does not contain %q", fragment)
		}
	}
	if got := strings.Count(matchSelectColumns, "FROM match_result_reports "); got != 2 {
		t.Fatalf("matchSelectColumns reads match_result_reports %d times, want 2", got)
	}
}

func TestValidateMatchResolution(t *testing.T) {
	winner := resultReportsHomeEntry
	loser := resultReportsAwayEntry
	author := resultReportsHomeUser
	claim := resultReportsClaim(2, 1)
	valid := matchResolution{FinalState: "forfeit", WinnerEntryID: &winner, CompletionReason: "report_timeout",
		Cause: progressionCauseTimeoutForfeit, RemoveEntryIDs: []string{loser}, RemovalReason: "report_timeout",
		Resolution: "report_timeout"}
	closed := resultReportsMatch("awaiting_confirmation", "", "")
	closed.CompetitionStatus = "cancelled"
	tests := []struct {
		name   string
		match  lockedResultMatch
		mutate func(*matchResolution)
		want   error
	}{
		{"valid forfeit", resultReportsMatch("awaiting_confirmation", "", ""), func(*matchResolution) {}, nil},
		{"closed competition", closed, func(*matchResolution) {}, errCompetitionClosed},
		{"winner is removed", resultReportsMatch("awaiting_confirmation", "", ""),
			func(r *matchResolution) { r.RemoveEntryIDs = []string{winner} }, errResultVerificationInvariant},
		{"removals out of order", resultReportsMatch("awaiting_confirmation", "", ""), func(r *matchResolution) {
			r.FinalState, r.WinnerEntryID, r.RemoveEntryIDs = "cancelled", nil, []string{loser, winner}
		}, errResultVerificationInvariant},
		{"cancelled with a winner", resultReportsMatch("awaiting_confirmation", "", ""),
			func(r *matchResolution) { r.FinalState = "cancelled" }, errResultVerificationInvariant},
		{"completed without a claim", resultReportsMatch("awaiting_confirmation", "", ""),
			func(r *matchResolution) { r.FinalState, r.RemoveEntryIDs = "completed", nil }, errResultVerificationInvariant},
		{"completed with a claim", resultReportsMatch("awaiting_confirmation", "", ""), func(r *matchResolution) {
			r.FinalState, r.RemoveEntryIDs, r.Claim, r.ClaimAuthorID, r.Origin = "completed", nil, &claim, &author, "agreed_reports"
		}, nil},
		{"ratings on a forfeit", resultReportsMatch("awaiting_confirmation", "", ""),
			func(r *matchResolution) { r.ApplyRatings = true }, errResultVerificationInvariant},
		{"winner outside the match", resultReportsMatch("awaiting_confirmation", "", ""), func(r *matchResolution) {
			outsider := resultReportsOutsider
			r.WinnerEntryID = &outsider
		}, errResultVerificationInvariant},
		{"removal without a reason", resultReportsMatch("awaiting_confirmation", "", ""),
			func(r *matchResolution) { r.RemovalReason = "" }, errResultVerificationInvariant},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := valid
			resolution.RemoveEntryIDs = slices.Clone(valid.RemoveEntryIDs)
			test.mutate(&resolution)
			if err := validateMatchResolution(test.match, resolution); !errors.Is(err, test.want) || (test.want == nil) != (err == nil) {
				t.Fatalf("validateMatchResolution = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOnlyDeadlinesAndGamicsRemoveEntries(t *testing.T) {
	staff := resultReportsHomeUser
	tests := []struct {
		actor    resolutionActor
		wantKind string
		wantUser bool
	}{
		{resolutionActor{Kind: "worker"}, "worker", false},
		{resolutionActor{Kind: "system"}, "system", false},
		{resolutionActor{Kind: "staff", UserID: &staff}, "staff", true},
		{resolutionActor{Kind: "player", UserID: &staff}, "", false},
		{resolutionActor{Kind: "staff"}, "", false},
		{resolutionActor{Kind: "worker", UserID: &staff}, "", false},
	}
	for _, test := range tests {
		kind, userID, err := test.actor.removalActor()
		if test.wantKind == "" {
			if !errors.Is(err, errEntryRemovalInvariant) {
				t.Fatalf("%+v may not remove entries: %v", test.actor, err)
			}
			continue
		}
		if err != nil || kind != test.wantKind || (userID != nil) != test.wantUser {
			t.Fatalf("%+v maps to %q %v %v", test.actor, kind, userID, err)
		}
	}
}

// A stored JSON column that no longer decodes is a corrupt row: an invariant
// logged at Error with 500, never a database outage answered with 503.
func TestCorruptStoredJSONIsAnInvariant(t *testing.T) {
	var games []gameScoreInput
	err := decodeStoredJSON([]byte(`[{"homeScore":`), &games, "report games")
	if !errors.Is(err, errResultVerificationInvariant) || resultVerificationErrorClass(err) != "plan_invariant" {
		t.Fatalf("corrupt games classified as %s: %v", resultVerificationErrorClass(err), err)
	}
	if err = decodeStoredJSON([]byte(`[{"homeScore":2,"awayScore":1}]`), &games, "report games"); err != nil ||
		len(games) != 1 || games[0].HomeScore != 2 || games[0].AwayScore != 1 {
		t.Fatalf("valid games = %+v, %v", games, err)
	}
}
