package httpapi

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	resultVerificationMigration = "000020_blind_result_verification"
	retiredTableCheckCount      = 7
)

var (
	resultVerificationSQLComment    = regexp.MustCompile(`--[^\n]*`)
	resultVerificationAlteredTables = regexp.MustCompile(`(?m)^ALTER TABLE (\w+)`)
)

func resultVerificationMigrationPath(direction string) string {
	return filepath.Join("..", "..", "migrations", resultVerificationMigration+"."+direction+".sql")
}

// resultVerificationMigrationStatements reads a 000020 file without its SQL
// comments, so prose about a clause can never satisfy or break a pin.
func resultVerificationMigrationStatements(t *testing.T, direction string) string {
	t.Helper()
	contents := readSourceFile(t, resultVerificationMigrationPath(direction))
	return resultVerificationSQLComment.ReplaceAllString(contents, "")
}

// resultVerificationMigrationSection returns the text from the first start up
// to and including the first end after it, so a fragment is pinned to one
// statement or constraint rather than to the whole file.
func resultVerificationMigrationSection(t *testing.T, contents, start, end string) string {
	t.Helper()
	from := strings.Index(contents, start)
	if from < 0 {
		t.Fatalf("migration does not contain %q", start)
	}
	length := strings.Index(contents[from:], end)
	if length < 0 {
		t.Fatalf("migration has no %q after %q", end, start)
	}
	return contents[from : from+length+len(end)]
}

func TestResultVerificationMigrationShape(t *testing.T) {
	up := resultVerificationMigrationPath("up")
	contents := readSourceFile(t, up)
	if !strings.HasPrefix(contents, "BEGIN;") || !strings.HasSuffix(strings.TrimSpace(contents), "COMMIT;") {
		t.Fatal("the up migration must be wrapped in BEGIN; ... COMMIT; for the runner")
	}
	statements := resultVerificationMigrationStatements(t, "up")
	if count := strings.Count(statements, "_retired_chk CHECK (false) NOT VALID"); count != retiredTableCheckCount {
		t.Fatalf("retired tables frozen = %d, want %d", count, retiredTableCheckCount)
	}
	// Only the frozen tables skip validation; every new CHECK is validated.
	if count := strings.Count(statements, "NOT VALID"); count != retiredTableCheckCount {
		t.Fatalf("NOT VALID constraints = %d, want only the %d retired-table CHECKs", count, retiredTableCheckCount)
	}
	assertFileOmits(t, up, "CONCURRENTLY")
	// The original auto-generated names are dropped without IF EXISTS so a
	// wrong name fails loudly instead of leaving two CHECKs behind.
	assertFileContains(t, up,
		"DROP CONSTRAINT organization_members_role_check",
		"ADD CONSTRAINT organization_members_role_v2_chk CHECK (role IN ('owner', 'admin', 'analyst'))",
		"DROP CONSTRAINT matches_completion_reason_check",
		"ADD CONSTRAINT matches_completion_reason_v2_chk",
		"DROP CONSTRAINT progression_events_cause_check",
		"ADD CONSTRAINT progression_events_cause_v2_chk",
		"DROP CONSTRAINT match_progression_applications_cause_check",
		"ADD CONSTRAINT match_progression_applications_cause_v2_chk",
		"DROP CONSTRAINT evidence_uploads_bound_kind_v2_chk",
		"ADD CONSTRAINT evidence_uploads_bound_kind_v3_chk",
		"result_submissions_origin_chk",
		"result_submissions_canonical_status_chk",
	)
	for _, table := range []string{
		"disputes", "dispute_events", "dispute_evidence_requests", "dispute_evidence", "dispute_appeals",
		"result_confirmations", "result_confirmation_evidence",
	} {
		assertFileContains(t, up, "ADD CONSTRAINT "+table+"_retired_chk CHECK (false) NOT VALID")
	}
	assertFileContains(t, up,
		"CREATE INDEX match_result_verifications_reminder_idx",
		"CREATE INDEX match_result_verifications_report_deadline_idx",
		"CREATE INDEX match_result_verifications_response_deadline_idx",
		"CREATE INDEX match_result_reviews_queue_idx",
		"CREATE INDEX match_result_reviews_decided_idx",
		"CREATE INDEX match_result_reviews_competition_idx",
		"CREATE INDEX player_strikes_active_user_idx",
		"CREATE INDEX player_strikes_user_history_idx",
		"CREATE INDEX player_strikes_feed_idx",
		"CREATE INDEX competition_entry_removals_competition_idx",
	)
	// Referee members are audited before they are converted; the audit INSERT
	// selects them by their old role.
	assertFileOrder(t, up,
		"cannot apply 000020_blind_result_verification",
		"INSERT INTO audit_events",
		"UPDATE organization_members SET role = 'analyst' WHERE role = 'referee'",
		"organization_members_role_v2_chk",
	)
}

// Legacy writers must finish before the preflight or fail after it, so every
// table the migration alters is locked up front, and nothing else is.
func TestResultVerificationMigrationLocksAlteredTablesBeforePreflight(t *testing.T) {
	statements := resultVerificationMigrationStatements(t, "up")
	lockAt, preflightAt := strings.Index(statements, "LOCK TABLE "), strings.Index(statements, "DO $$")
	timeoutAt := strings.Index(statements, "SET LOCAL lock_timeout = '30s';")
	if timeoutAt < 0 || lockAt < 0 || preflightAt < 0 || timeoutAt > lockAt || lockAt > preflightAt {
		t.Fatalf("lock_timeout at %d, LOCK TABLE at %d, preflight at %d: want timeout < lock < preflight",
			timeoutAt, lockAt, preflightAt)
	}
	lock := resultVerificationMigrationSection(t, statements, "LOCK TABLE ", ";")
	if !strings.HasSuffix(lock, "IN ACCESS EXCLUSIVE MODE;") {
		t.Fatalf("tables are not locked in ACCESS EXCLUSIVE mode: %q", lock)
	}
	tableList := strings.TrimSuffix(strings.TrimPrefix(lock, "LOCK TABLE "), "IN ACCESS EXCLUSIVE MODE;")
	locked := strings.FieldsFunc(tableList, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	var altered []string
	for _, match := range resultVerificationAlteredTables.FindAllStringSubmatch(statements, -1) {
		if !slices.Contains(altered, match[1]) {
			altered = append(altered, match[1])
		}
	}
	slices.Sort(locked)
	slices.Sort(altered)
	if len(altered) == 0 || !slices.Equal(locked, altered) {
		t.Fatalf("locked tables %v, want exactly the altered tables %v", locked, altered)
	}
}

func TestResultVerificationMigrationConstraints(t *testing.T) {
	statements := resultVerificationMigrationStatements(t, "up")
	cases := []struct {
		start, end string
		fragments  []string
	}{
		{
			start: "ADD CONSTRAINT result_submissions_canonical_status_chk", end: ";",
			fragments: []string{"CHECK (status NOT IN ('pending_confirmation', 'disputed'));"},
		},
		{
			start: "ADD CONSTRAINT matches_completion_reason_v2_chk", end: ";",
			fragments: []string{
				"'report_timeout', 'response_timeout', 'no_result_reported', 'platform_review'",
				"'competition_cancelled'",
			},
		},
		{
			start: "resolution text CHECK (resolution IN", end: ")),",
			fragments: []string{"'agreed', 'report_timeout', 'response_timeout', 'platform_review', 'competition_cancelled'"},
		},
		// A CHECK that evaluates to NULL passes, so every penalties branch names
		// both tiebreak scores NOT NULL (000006 pattern).
		{
			start: "CONSTRAINT match_result_reports_tiebreak_chk", end: "CONSTRAINT match_result_reports_games_array_chk",
			fragments: []string{"AND home_tiebreak_score IS NOT NULL AND away_tiebreak_score IS NOT NULL"},
		},
		{
			start: "CREATE TABLE match_result_reviews", end: ";",
			fragments: []string{
				"CHECK (status IN ('queued', 'decided', 'closed'))",
				"reason text NOT NULL CHECK (reason IN ('reports_differ', 'evidence_unavailable'))",
			},
		},
		{
			start: "CONSTRAINT match_result_reviews_decision_state_chk", end: "CONSTRAINT match_result_reviews_decider_chk",
			fragments: []string{
				"OR (status = 'closed' AND decision IS NULL AND decider_kind IS NULL AND decided_by IS NULL",
				"AND decider_ref IS NULL AND decided_at IS NOT NULL)",
			},
		},
		{
			start: "CONSTRAINT match_result_reviews_corrected_chk", end: "CONSTRAINT match_result_reviews_corrected_tiebreak_chk",
			fragments: []string{"AND corrected_away_score IS NOT NULL AND corrected_game_results IS NOT NULL"},
		},
		{
			start: "CONSTRAINT match_result_reviews_corrected_tiebreak_chk", end: ";",
			fragments: []string{"AND corrected_home_tiebreak_score IS NOT NULL AND corrected_away_tiebreak_score IS NOT NULL"},
		},
		// Only staff record strikes, so an automated decider can never ban.
		{
			start: "CREATE TABLE player_strikes", end: ";",
			fragments: []string{
				"created_by_kind text NOT NULL CHECK (created_by_kind = 'staff')",
				"AND revoke_reason IS NOT NULL",
			},
		},
		{
			start: "CREATE TABLE competition_entry_removals", end: ";",
			fragments: []string{"('registered', 'checked_in', 'accepted', 'withdrawal_pending')"},
		},
	}
	for _, testCase := range cases {
		section := resultVerificationMigrationSection(t, statements, testCase.start, testCase.end)
		for _, fragment := range testCase.fragments {
			if !strings.Contains(section, fragment) {
				t.Errorf("%s does not contain %q", testCase.start, fragment)
			}
		}
	}
}

// Step 5b settles legacy data after the vocabulary allows the new completion
// reason and before any new table exists.
func TestResultVerificationMigrationSettlesLegacyData(t *testing.T) {
	up := resultVerificationMigrationPath("up")
	assertFileOrder(t, up,
		"ADD CONSTRAINT matches_completion_reason_v2_chk",
		"WHERE c.status = 'cancelled' AND m.state IN ('pending', 'ready', 'in_progress')",
		"completion_reason = 'competition_cancelled'",
		"'match.cancelled'",
		"WHERE m.state = 'in_progress' AND m.result_due_at IS NOT NULL AND m.result_due_at <= now()",
		"result_due_at = now() + interval '24 hours'",
		"'match.result_deadline_extended'",
		"UPDATE notifications",
		"SET action_url = '/matches/' || (data ->> 'matchId'), data = data - 'caseId'",
		"WHERE action_url LIKE '/referee-cases/%' AND data ? 'matchId'",
		"CREATE TABLE match_result_verifications",
	)
	// The referee conversion and both match rewrites are audited under the
	// migration's request id.
	if count := strings.Count(resultVerificationMigrationStatements(t, "up"), "'migration:000020'"); count != 3 {
		t.Fatalf("audited migration statements = %d, want 3", count)
	}
}

func TestResultVerificationMigrationDownGuard(t *testing.T) {
	down := resultVerificationMigrationPath("down")
	assertFileContains(t, down,
		"Step 5b of the up migration is not reversed",
		"ADD CONSTRAINT organization_members_role_check CHECK (role IN ('owner', 'admin', 'referee', 'analyst'))",
		"ADD CONSTRAINT matches_completion_reason_check",
		"ADD CONSTRAINT progression_events_cause_check",
		"ADD CONSTRAINT match_progression_applications_cause_check",
		"ADD CONSTRAINT evidence_uploads_bound_kind_v2_chk",
		"DROP COLUMN IF EXISTS origin",
	)
	statements := resultVerificationMigrationStatements(t, "down")
	guard := resultVerificationMigrationSection(t, statements, "DO $$", "$$;")
	for _, fragment := range []string{
		"cannot roll back 000020_blind_result_verification",
		"'report_timeout', 'response_timeout', 'no_result_reported', 'platform_review'",
		"'competition_cancelled'",
		"cause = 'platform_review'",
		"origin <> 'legacy'",
	} {
		if !strings.Contains(guard, fragment) {
			t.Errorf("rollback guard does not contain %q", fragment)
		}
	}
	if count := strings.Count(statements, "_retired_chk"); count != retiredTableCheckCount {
		t.Fatalf("down migration unfreezes %d tables, want %d", count, retiredTableCheckCount)
	}
}

func TestResultVerificationMigrationIsEmbedded(t *testing.T) {
	if _, err := fs.Stat(migrations.FS, resultVerificationMigration+".up.sql"); err != nil {
		t.Fatalf("up migration is not embedded: %v", err)
	}
	if _, err := fs.Stat(migrations.FS, resultVerificationMigration+".down.sql"); err == nil {
		t.Fatal("down migrations are operator artifacts and must not be embedded")
	}
}

// resultVerificationLegacyFixture is pre-000020 data that step 5b must settle.
type resultVerificationLegacyFixture struct {
	OverdueMatchID         string
	OverdueMatchVersion    int
	CancelledCompetitionID string
	NotificationID         string
}

// Upgrades a 000019 schema that holds referee data and legacy matches, then
// rolls 000020 back and forward again. Needs GAMICS_TEST_DATABASE_URL.
func TestIntegrationResultVerificationMigration(t *testing.T) {
	pool := openIntegrationSchema(t)
	applyEmbeddedMigrationsBefore(t, pool, resultVerificationMigration)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	matchID := readyIntegrationMatches(t, pool, seeded.ID)[0]
	referee, player := seeded.Entries[1].UserID, seeded.Entries[0].UserID
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_members(organization_id,user_id,role)
		VALUES ($1,$2,'referee')`, seeded.OrganizationID, referee); err != nil {
		t.Fatal(err)
	}
	var disputeID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO disputes(match_id,opened_by,reason_code)
		VALUES ($1,$2,'score_mismatch') RETURNING id`, matchID, player).Scan(&disputeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO dispute_evidence_requests
		(dispute_id,requested_from,requested_by,message,due_at) VALUES ($1,$2,$3,'Upload the score screen.',now()+interval '1 hour')`,
		disputeID, player, referee); err != nil {
		t.Fatal(err)
	}
	legacy := seedResultVerificationLegacyFixture(t, pool, seeded, matchID, disputeID)

	up := readSourceFile(t, resultVerificationMigrationPath("up"))
	down := readSourceFile(t, resultVerificationMigrationPath("down"))
	if err := execMigrationSQL(t.Context(), pool, up); err == nil || !strings.Contains(err.Error(), "referee cases or appeals are active") {
		t.Fatalf("migration ran over an open referee case: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE disputes SET status='resolved',resolved_at=now() WHERE id=$1`, disputeID); err != nil {
		t.Fatal(err)
	}
	if err := execMigrationSQL(t.Context(), pool, up); err != nil {
		t.Fatalf("apply %s: %v", resultVerificationMigration, err)
	}
	assertRefereeRetired(t, pool, seeded, referee, disputeID)
	assertResultVerificationLegacySettled(t, pool, legacy)

	if _, err := pool.Exec(t.Context(), `INSERT INTO match_result_verifications
		(match_id,competition_id,phase,first_report_entry_id,first_reported_at,report_window_seconds,
		 reminder_lead_seconds,response_window_seconds,report_deadline_at,reminder_at)
		VALUES ($1,$2,'awaiting_second_report',$3,now(),600,180,600,now()+interval '600 seconds',now()+interval '420 seconds')`,
		matchID, seeded.ID, seeded.Entries[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := execMigrationSQL(t.Context(), pool, down); err == nil || !strings.Contains(err.Error(), "cannot roll back 000020_blind_result_verification") {
		t.Fatalf("rollback discarded blind-report data: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM match_result_verifications WHERE match_id=$1`, matchID); err != nil {
		t.Fatal(err)
	}
	if err := execMigrationSQL(t.Context(), pool, down); err == nil || !strings.Contains(err.Error(), "cannot roll back 000020_blind_result_verification") {
		t.Fatalf("rollback discarded matches cancelled by the migration: %v", err)
	}
	// Clearing the new completion reason stands in for the operator archiving
	// the matches step 5b cancelled.
	if _, err := pool.Exec(t.Context(), `UPDATE matches SET completion_reason=NULL
		WHERE competition_id=$1 AND completion_reason='competition_cancelled'`, legacy.CancelledCompetitionID); err != nil {
		t.Fatal(err)
	}
	if err := execMigrationSQL(t.Context(), pool, down); err != nil {
		t.Fatalf("roll back %s: %v", resultVerificationMigration, err)
	}
	var tableExists, legacyRoleCheck bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('match_result_verifications') IS NOT NULL,
		EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='organization_members'::regclass
			AND conname='organization_members_role_check')`).Scan(&tableExists, &legacyRoleCheck); err != nil {
		t.Fatal(err)
	}
	if tableExists || !legacyRoleCheck {
		t.Fatalf("rollback left tables=%v restored role check=%v", tableExists, legacyRoleCheck)
	}
	if err := execMigrationSQL(t.Context(), pool, up); err != nil {
		t.Fatalf("re-apply %s after rollback: %v", resultVerificationMigration, err)
	}
}

// seedResultVerificationLegacyFixture leaves the match in progress past its
// result deadline, cancels a second competition that still has live matches,
// and projects a referee-case inbox row for the dispute.
func seedResultVerificationLegacyFixture(t *testing.T, pool *pgxpool.Pool, seeded integrationCompetition,
	matchID, disputeID string) resultVerificationLegacyFixture {
	t.Helper()
	ctx := t.Context()
	fixture := resultVerificationLegacyFixture{OverdueMatchID: matchID}
	checkInBoth(t, pool, matchID)
	shiftResultDue(t, pool, matchID, 2*time.Hour)
	if err := pool.QueryRow(ctx, `SELECT version FROM matches WHERE id=$1`, matchID).
		Scan(&fixture.OverdueMatchVersion); err != nil {
		t.Fatal(err)
	}
	cancelled := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	fixture.CancelledCompetitionID = cancelled.ID
	if _, err := pool.Exec(ctx, `UPDATE competitions SET status='cancelled' WHERE id=$1`, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO notifications(user_id,category,title,body,data,action_url)
		VALUES ($1,'result','Referee decision recorded','A decision is available for your disputed match.',
			jsonb_build_object('matchId',$2::text,'caseId',$3::text),'/referee-cases/' || $3::text)
		RETURNING id`, seeded.Entries[0].UserID, matchID, disputeID).Scan(&fixture.NotificationID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertResultVerificationLegacySettled(t *testing.T, pool *pgxpool.Pool, fixture resultVerificationLegacyFixture) {
	t.Helper()
	ctx := t.Context()
	var version int
	var extended, extensionAudited bool
	if err := pool.QueryRow(ctx, `SELECT match.version,match.result_due_at>now()+interval '23 hours',
		EXISTS (SELECT 1 FROM audit_events WHERE action='match.result_deadline_extended'
			AND subject_id=match.id::text AND request_id='migration:000020')
		FROM matches match WHERE match.id=$1 AND match.state='in_progress'`, fixture.OverdueMatchID).
		Scan(&version, &extended, &extensionAudited); err != nil {
		t.Fatal(err)
	}
	if version != fixture.OverdueMatchVersion+1 || !extended || !extensionAudited {
		t.Fatalf("overdue match version=%d (was %d) extended=%v audited=%v",
			version, fixture.OverdueMatchVersion, extended, extensionAudited)
	}
	var total, cancelled, audited int
	if err := pool.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE state='cancelled' AND completion_reason='competition_cancelled'
			AND winner_entry_id IS NULL AND completed_at IS NOT NULL),
		(SELECT count(*) FROM audit_events WHERE action='match.cancelled' AND request_id='migration:000020'
			AND subject_id IN (SELECT id::text FROM matches WHERE competition_id=$1))
		FROM matches WHERE competition_id=$1`, fixture.CancelledCompetitionID).Scan(&total, &cancelled, &audited); err != nil {
		t.Fatal(err)
	}
	if total == 0 || cancelled != total || audited != total {
		t.Fatalf("cancelled competition matches=%d cancelled=%d audited=%d", total, cancelled, audited)
	}
	var actionURL string
	var keepsCase bool
	if err := pool.QueryRow(ctx, `SELECT action_url,data ? 'caseId' FROM notifications WHERE id=$1`,
		fixture.NotificationID).Scan(&actionURL, &keepsCase); err != nil {
		t.Fatal(err)
	}
	if actionURL != "/matches/"+fixture.OverdueMatchID || keepsCase {
		t.Fatalf("referee-case inbox row action_url=%s keeps caseId=%v", actionURL, keepsCase)
	}
}

func assertRefereeRetired(t *testing.T, pool *pgxpool.Pool, seeded integrationCompetition, referee, disputeID string) {
	t.Helper()
	var role, requestStatus string
	var audited bool
	if err := pool.QueryRow(t.Context(), `SELECT member.role,
		(SELECT status FROM dispute_evidence_requests WHERE dispute_id=$3),
		EXISTS (SELECT 1 FROM audit_events WHERE action='organization.member_role_changed'
			AND subject_id=member.user_id::text AND request_id='migration:000020' AND after_state->>'role'='analyst')
		FROM organization_members member WHERE member.organization_id=$1 AND member.user_id=$2`,
		seeded.OrganizationID, referee, disputeID).Scan(&role, &requestStatus, &audited); err != nil {
		t.Fatal(err)
	}
	if role != "analyst" || !audited || requestStatus != "cancelled" {
		t.Fatalf("referee retirement role=%s audited=%v evidence request=%s", role, audited, requestStatus)
	}
	_, err := pool.Exec(t.Context(), `UPDATE disputes SET description='reopened' WHERE id=$1`, disputeID)
	assertCheckViolation(t, err, "disputes_retired_chk")
	_, err = pool.Exec(t.Context(), `INSERT INTO organization_members(organization_id,user_id,role) VALUES ($1,$2,'referee')`,
		seeded.OrganizationID, seeded.Entries[0].UserID)
	assertCheckViolation(t, err, "organization_members_role_v2_chk")
}
