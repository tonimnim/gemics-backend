package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Shared helpers for DB-gated integration tests. They need a disposable
// PostgreSQL database in GAMICS_TEST_DATABASE_URL and skip without it. Every
// test creates and drops only its own random schema; never point them at a
// production database.

const integrationDatabaseURLEnv = "GAMICS_TEST_DATABASE_URL"

// integrationSeedOptions describes the draw built by seedIntegrationCompetition.
// Zero GroupCount and BestOf mean one group and best-of-one.
type integrationSeedOptions struct {
	Format           string
	Entries          int
	GroupCount       int
	DoubleRoundRobin bool
	ThirdPlace       bool
	BestOf           int
}

type integrationEntry struct {
	ID     string
	UserID string
}

// integrationCompetition is a running competition with a persisted draw.
// Entries are in seed order, so Entries[0] holds seed 1.
type integrationCompetition struct {
	ID             string
	OrganizationID string
	OrganizerID    string
	GameID         string
	StageID        string
	Format         string
	Entries        []integrationEntry
}

// openIntegrationSchema returns a pool bound to a fresh, empty schema. Shared
// extensions are installed in public first, because a CREATE EXTENSION run by
// a migration would otherwise land in, and be dropped with, one test's schema.
func openIntegrationSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv(integrationDatabaseURLEnv)
	if raw == "" {
		t.Skip(integrationDatabaseURLEnv + " not configured")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	t.Cleanup(admin.Close)
	if err = installIntegrationExtensions(ctx, admin); err != nil {
		t.Fatalf("install test database extensions: %v", err)
	}
	quoted := pgx.Identifier{"gamics_it_" + strings.ToLower(rand.Text())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only the schema created by this test.
		if _, dropErr := admin.Exec(dropCtx, "DROP SCHEMA "+quoted+" CASCADE"); dropErr != nil {
			t.Errorf("drop integration schema: %v", dropErr)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func installIntegrationExtensions(ctx context.Context, admin *pgxpool.Pool) error {
	tx, err := admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Serializes concurrent test binaries; CREATE EXTENSION IF NOT EXISTS is not
	// safe against a concurrent creator.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gamics:integration-extensions', 0))`); err != nil {
		return err
	}
	for _, extension := range []string{"pgcrypto", "pg_trgm"} {
		if _, err = tx.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+extension+" WITH SCHEMA public"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// openMigratedIntegrationDatabase returns a pool bound to a fresh schema with
// every embedded migration applied by the production runner.
func openMigratedIntegrationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openIntegrationSchema(t)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	return pool
}

// applyEmbeddedMigrationsBefore applies, in order, every embedded up migration
// whose name sorts before stop. Migration tests use it to build the schema a
// new migration upgrades.
func applyEmbeddedMigrationsBefore(t *testing.T, pool *pgxpool.Pool, stop string) {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() < stop {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, readErr := migrations.FS.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err = execMigrationSQL(t.Context(), pool, string(raw)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

// sameJSON reports whether two response bodies hold the same JSON value. An
// idempotent replay is served from the stored jsonb response, which PostgreSQL
// re-serializes with its own whitespace and key order, so replays are compared
// as values rather than byte for byte.
func sameJSON(t *testing.T, left, right []byte) bool {
	t.Helper()
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// execMigrationSQL runs one migration file the way database.Migrate does: the
// file's own BEGIN/COMMIT are replaced by a single transaction.
func execMigrationSQL(ctx context.Context, pool *pgxpool.Pool, raw string) error {
	sql := strings.TrimSpace(raw)
	sql = strings.TrimSpace(strings.TrimPrefix(sql, "BEGIN;"))
	sql = strings.TrimSpace(strings.TrimSuffix(sql, "COMMIT;"))
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, sql); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// seedIntegrationCompetition inserts an organizer, a running competition and
// its players, then persists the draw through the production planner. Times
// come from the database clock, so the first round's result windows are open.
func seedIntegrationCompetition(t *testing.T, pool *pgxpool.Pool, options integrationSeedOptions) integrationCompetition {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC()
	suffix := strings.ToLower(rand.Text())[:12]
	seeded := integrationCompetition{GameID: "efootball-mobile", Format: options.Format}
	if seeded.OrganizerID, err = insertIntegrationUser(ctx, tx, "organizer-"+suffix, "Organizer"); err != nil {
		t.Fatalf("seed organizer: %v", err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO organizations(name,slug) VALUES ($1,$2) RETURNING id`,
		"Integration "+suffix, "integration-"+suffix).Scan(&seeded.OrganizationID); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_members(organization_id,user_id,role) VALUES ($1,$2,'owner')`,
		seeded.OrganizationID, seeded.OrganizerID); err != nil {
		t.Fatalf("seed organization owner: %v", err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO competitions(organization_id,game_id,name,slug,format,status,max_entries,
		registration_opens_at,registration_closes_at,starts_at,created_by)
		VALUES ($1,$2,$3,$4,$5,'running',$6,$7,$8,$9,$10) RETURNING id`,
		seeded.OrganizationID, seeded.GameID, "Integration cup "+suffix, "cup-"+suffix, options.Format,
		max(2, options.Entries), now.Add(-2*time.Hour), now.Add(-time.Hour), now, seeded.OrganizerID).
		Scan(&seeded.ID); err != nil {
		t.Fatalf("seed competition: %v", err)
	}
	for index := range options.Entries {
		entry, entryErr := insertIntegrationEntry(ctx, tx, seeded, suffix, index+1)
		if entryErr != nil {
			t.Fatalf("seed entry %d: %v", index+1, entryErr)
		}
		seeded.Entries = append(seeded.Entries, entry)
	}
	seeded.StageID = persistIntegrationDraw(t, tx, seeded, options, now)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return seeded
}

func insertIntegrationUser(ctx context.Context, tx pgx.Tx, handle, displayName string) (string, error) {
	var userID string
	err := tx.QueryRow(ctx, `INSERT INTO users(email,display_name,status) VALUES ($1,$2,'active') RETURNING id`,
		handle+"@gamics.test", displayName).Scan(&userID)
	return userID, err
}

func insertIntegrationEntry(ctx context.Context, tx pgx.Tx, seeded integrationCompetition, suffix string, seed int) (integrationEntry, error) {
	handle := fmt.Sprintf("p%d_%s", seed, suffix)
	userID, err := insertIntegrationUser(ctx, tx, handle, fmt.Sprintf("Player %d", seed))
	if err != nil {
		return integrationEntry{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, handle); err != nil {
		return integrationEntry{}, err
	}
	var gameAccountID string
	if err = tx.QueryRow(ctx, `INSERT INTO game_accounts(user_id,game_id,platform,in_game_name)
		VALUES ($1,$2,'android',$3) RETURNING id`, userID, seeded.GameID, handle).Scan(&gameAccountID); err != nil {
		return integrationEntry{}, err
	}
	entry := integrationEntry{UserID: userID}
	if err = tx.QueryRow(ctx, `INSERT INTO competition_entries(competition_id,display_name,captain_user_id,seed,status)
		VALUES ($1,$2,$3,$4,'accepted') RETURNING id`, seeded.ID, fmt.Sprintf("Player %d", seed), userID, seed).
		Scan(&entry.ID); err != nil {
		return integrationEntry{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
		VALUES ($1,$2,$3,$4)`, entry.ID, seeded.ID, userID, gameAccountID)
	return entry, err
}

func persistIntegrationDraw(t *testing.T, tx pgx.Tx, seeded integrationCompetition,
	options integrationSeedOptions, now time.Time) string {
	t.Helper()
	candidates, err := loadFrozenDrawCandidates(t.Context(), tx, seeded.ID, seeded.GameID)
	if err != nil {
		t.Fatalf("load draw candidates: %v", err)
	}
	input := normalizedDrawRequest{SeedingPolicy: "seeded", ExpectedStatus: "check_in", Config: normalizedDrawConfigView{
		BestOf: max(1, options.BestOf), ThirdPlace: options.ThirdPlace, GroupCount: max(1, options.GroupCount),
		DoubleRoundRobin: options.DoubleRoundRobin, CheckInLeadMinutes: 15, CheckInGraceMinutes: 10,
		ResultWindowMinutes: 60, RoundIntervalMinutes: 90,
	}}
	plan, problem := buildOrganizerDrawPlan(seeded.ID, options.Format, now, now, input, candidates, nil)
	if problem != nil {
		t.Fatalf("build draw: %s", problem.Code)
	}
	if err = persistOrganizerDraw(t.Context(), tx, plan, input.SeedingPolicy, seeded.OrganizerID); err != nil {
		t.Fatalf("persist draw: %v", err)
	}
	return plan.StageID
}

// readyIntegrationMatches lists the competition's ready matches in bracket,
// round and match-number order.
func readyIntegrationMatches(t *testing.T, pool *pgxpool.Pool, competitionID string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT id::text FROM matches
		WHERE competition_id=$1 AND state='ready' ORDER BY bracket,round_number,match_number`, competitionID)
	if err != nil {
		t.Fatal(err)
	}
	matchIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return matchIDs
}

// checkInBoth checks in both captains of a ready match in the same two steps
// the check-in handler takes, leaving it in_progress two versions later.
func checkInBoth(t *testing.T, pool *pgxpool.Pool, matchID string) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var homeEntryID, awayEntryID, homeUserID, awayUserID string
	var version int
	if err = tx.QueryRow(ctx, `SELECT match.home_entry_id::text,match.away_entry_id::text,
		home.captain_user_id::text,away.captain_user_id::text,match.version
		FROM matches match
		JOIN competition_entries home ON home.id=match.home_entry_id
		JOIN competition_entries away ON away.id=match.away_entry_id
		WHERE match.id=$1 AND match.state='ready' FOR UPDATE OF match`, matchID).
		Scan(&homeEntryID, &awayEntryID, &homeUserID, &awayUserID, &version); err != nil {
		t.Fatalf("load ready match %s: %v", matchID, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO match_check_ins(match_id,entry_id,checked_in_by,match_version,idempotency_key)
		VALUES ($1,$2,$3,$4,$5),($1,$6,$7,$8,$9)`, matchID, homeEntryID, homeUserID, version, "it-check-in-"+rand.Text(),
		awayEntryID, awayUserID, version+1, "it-check-in-"+rand.Text()); err != nil {
		t.Fatalf("check in match %s: %v", matchID, err)
	}
	tag, err := tx.Exec(ctx, `UPDATE matches SET state='in_progress',version=version+2,updated_at=now()
		WHERE id=$1 AND state='ready' AND version=$2`, matchID, version)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("start match %s: rows=%d err=%v", matchID, tag.RowsAffected(), err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// shiftVerificationClock moves every verification timestamp of the match d into
// the past, as if d had elapsed. The deadlines move together, so the row's
// ordering CHECKs keep holding.
func shiftVerificationClock(t *testing.T, pool *pgxpool.Pool, matchID string, d time.Duration) {
	t.Helper()
	tag, err := pool.Exec(t.Context(), `UPDATE match_result_verifications SET
		first_reported_at=first_reported_at-make_interval(secs => $2),
		reminder_at=reminder_at-make_interval(secs => $2),
		report_deadline_at=report_deadline_at-make_interval(secs => $2),
		mismatch_at=mismatch_at-make_interval(secs => $2),
		response_deadline_at=response_deadline_at-make_interval(secs => $2)
		WHERE match_id=$1`, matchID, d.Seconds())
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("shift verification clock of %s: rows=%d err=%v", matchID, tag.RowsAffected(), err)
	}
}

// shiftResultDue moves the match's result deadline d into the past.
func shiftResultDue(t *testing.T, pool *pgxpool.Pool, matchID string, d time.Duration) {
	t.Helper()
	tag, err := pool.Exec(t.Context(), `UPDATE matches SET result_due_at=result_due_at-make_interval(secs => $2)
		WHERE id=$1 AND result_due_at IS NOT NULL`, matchID, d.Seconds())
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("shift result deadline of %s: rows=%d err=%v", matchID, tag.RowsAffected(), err)
	}
}

// assertCheckViolation requires err to be a CHECK violation of the named
// constraint.
func assertCheckViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != constraint {
		t.Fatalf("expected a %s check violation, got %v", constraint, err)
	}
}

// The harness itself is exercised against every format so later flow tests can
// trust it.
func TestIntegrationHarnessSeedsEveryFormat(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	cases := []integrationSeedOptions{
		{Format: "single_elimination", Entries: 8, ThirdPlace: true},
		{Format: "double_elimination", Entries: 4},
		{Format: "round_robin", Entries: 5},
		{Format: "round_robin", Entries: 8, GroupCount: 2},
		{Format: "round_robin", Entries: 4, DoubleRoundRobin: true},
	}
	for _, options := range cases {
		seeded := seedIntegrationCompetition(t, pool, options)
		config := normalizedDrawConfigView{GroupCount: max(1, options.GroupCount),
			DoubleRoundRobin: options.DoubleRoundRobin, ThirdPlace: options.ThirdPlace}
		var matchCount int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM matches WHERE competition_id=$1`, seeded.ID).
			Scan(&matchCount); err != nil {
			t.Fatal(err)
		}
		if want := estimatedDrawMatches(options.Format, options.Entries, config); matchCount != want {
			t.Fatalf("%+v persisted %d matches, want %d", options, matchCount, want)
		}
		ready := readyIntegrationMatches(t, pool, seeded.ID)
		if len(seeded.Entries) != options.Entries || len(ready) == 0 {
			t.Fatalf("%+v seeded %d entries and %d ready matches", options, len(seeded.Entries), len(ready))
		}
		checkInBoth(t, pool, ready[0])
		// The first round is scheduled one check-in lead after the draw and its
		// result window is an hour, so two hours puts the deadline behind now.
		shiftResultDue(t, pool, ready[0], 2*time.Hour)
		var state string
		var overdue bool
		if err := pool.QueryRow(t.Context(), `SELECT state,result_due_at<=now() FROM matches WHERE id=$1`, ready[0]).
			Scan(&state, &overdue); err != nil {
			t.Fatal(err)
		}
		if state != "in_progress" || !overdue {
			t.Fatalf("%+v first match state=%s overdue=%v", options, state, overdue)
		}
	}
}

func TestIntegrationHarnessShiftsVerificationClock(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	matchID := readyIntegrationMatches(t, pool, seeded.ID)[0]
	checkInBoth(t, pool, matchID)
	if _, err := pool.Exec(t.Context(), `INSERT INTO match_result_verifications
		(match_id,competition_id,phase,first_report_entry_id,first_reported_at,report_window_seconds,
		 reminder_lead_seconds,response_window_seconds,report_deadline_at,reminder_at,mismatch_at,response_deadline_at)
		VALUES ($1,$2,'awaiting_responses',$3,now(),600,180,600,now()+interval '600 seconds',
		 now()+interval '420 seconds',now()+interval '1 second',now()+interval '601 seconds')`,
		matchID, seeded.ID, seeded.Entries[0].ID); err != nil {
		t.Fatal(err)
	}
	shiftVerificationClock(t, pool, matchID, 20*time.Minute)
	var reportExpired, responseExpired bool
	if err := pool.QueryRow(t.Context(), `SELECT report_deadline_at<=now(),response_deadline_at<=now()
		FROM match_result_verifications WHERE match_id=$1`, matchID).Scan(&reportExpired, &responseExpired); err != nil {
		t.Fatal(err)
	}
	if !reportExpired || !responseExpired {
		t.Fatalf("verification deadlines did not elapse: report=%v response=%v", reportExpired, responseExpired)
	}
}
