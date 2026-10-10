package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These DB-gated tests drive the progression engine directly. Each ready match
// is finalized the way the result finalizer does it: a guarded terminal update,
// the removed entries disqualified in the same transaction, then
// applyMatchProgression. Every draw is played to completion.

type progressionRemovalOutcome uint8

const (
	progressionRemovalHomeWins progressionRemovalOutcome = iota
	progressionRemovalDraw
	progressionRemovalRemoveHome
	progressionRemovalRemoveAway
	progressionRemovalRemoveBoth
	progressionRemovalReviewAwayWins
	progressionRemovalReviewRemoveBoth
)

// progressionRemovalMaxSteps bounds a run; the largest draw here has 15 matches.
const progressionRemovalMaxSteps = 200

// progressionRemovalRule decides one ready match. Number 0 matches the next
// ready match of the bracket and round, whichever fixture that is.
type progressionRemovalRule struct {
	Bracket string
	Round   int
	Number  int
	Outcome progressionRemovalOutcome
}

func (rule progressionRemovalRule) matches(match progressionRemovalMatch) bool {
	return rule.Bracket == match.Bracket && rule.Round == match.Round && (rule.Number == 0 || rule.Number == match.Number)
}

type progressionRemovalMatch struct {
	ID          string
	Bracket     string
	Round       int
	Number      int
	Version     int
	HomeEntryID string
	AwayEntryID string
}

func (match progressionRemovalMatch) String() string {
	return fmt.Sprintf("%s-R%d-M%d", match.Bracket, match.Round, match.Number)
}

// progressionRemovalFinal is the terminal write and progression input for one
// outcome.
type progressionRemovalFinal struct {
	State            string
	CompletionReason string
	Cause            string
	WinnerEntryID    *string
	HomeScore        *int
	AwayScore        *int
	RemovedEntryIDs  []string
}

func (outcome progressionRemovalOutcome) final(match progressionRemovalMatch) progressionRemovalFinal {
	home, away := match.HomeEntryID, match.AwayEntryID
	one, two := 1, 2
	switch outcome {
	case progressionRemovalDraw:
		return progressionRemovalFinal{State: "completed", CompletionReason: "played",
			Cause: progressionCausePlayerConfirmation, HomeScore: &one, AwayScore: &one}
	case progressionRemovalRemoveHome:
		return progressionRemovalFinal{State: "forfeit", CompletionReason: "response_timeout",
			Cause: progressionCauseTimeoutForfeit, WinnerEntryID: &away, RemovedEntryIDs: []string{home}}
	case progressionRemovalRemoveAway:
		return progressionRemovalFinal{State: "forfeit", CompletionReason: "response_timeout",
			Cause: progressionCauseTimeoutForfeit, WinnerEntryID: &home, RemovedEntryIDs: []string{away}}
	case progressionRemovalRemoveBoth:
		return progressionRemovalFinal{State: "cancelled", CompletionReason: "no_result_reported",
			Cause: progressionCauseTimeoutForfeit, RemovedEntryIDs: []string{home, away}}
	case progressionRemovalReviewAwayWins:
		return progressionRemovalFinal{State: "completed", CompletionReason: "platform_review",
			Cause: progressionCausePlatformReview, WinnerEntryID: &away, HomeScore: &one, AwayScore: &two}
	case progressionRemovalReviewRemoveBoth:
		return progressionRemovalFinal{State: "cancelled", CompletionReason: "platform_review",
			Cause: progressionCausePlatformReview, RemovedEntryIDs: []string{home, away}}
	default:
		return progressionRemovalFinal{State: "completed", CompletionReason: "played",
			Cause: progressionCausePlayerConfirmation, WinnerEntryID: &home, HomeScore: &two, AwayScore: &one}
	}
}

type progressionRemovalStep struct {
	Match   progressionRemovalMatch
	Outcome progressionRemovalOutcome
	Result  matchProgressionResult
}

type progressionRemovalRun struct {
	Competition integrationCompetition
	Steps       []progressionRemovalStep
	// RemovedBy maps each removed entry to the match that removed it.
	RemovedBy map[string]progressionRemovalMatch
}

type progressionRemovalScenario struct {
	Name    string
	Options integrationSeedOptions
	Rules   []progressionRemovalRule
	Check   func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun)
}

func progressionRemovalRunScenarios(t *testing.T, scenarios []progressionRemovalScenario) {
	t.Helper()
	pool := openMigratedIntegrationDatabase(t)
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			run := progressionRemovalPlay(t, pool, scenario)
			progressionRemovalAssertSettled(t, pool, run)
			if scenario.Check != nil {
				scenario.Check(t, pool, run)
			}
		})
	}
}

// progressionRemovalPlay finalizes ready matches one at a time until none is
// left. Every rule must fire exactly once.
func progressionRemovalPlay(t *testing.T, pool *pgxpool.Pool, scenario progressionRemovalScenario) progressionRemovalRun {
	t.Helper()
	run := progressionRemovalRun{
		Competition: seedIntegrationCompetition(t, pool, scenario.Options),
		RemovedBy:   map[string]progressionRemovalMatch{},
	}
	fired := make([]bool, len(scenario.Rules))
	for {
		ready := progressionRemovalReadyMatches(t, pool, run.Competition.ID)
		if len(ready) == 0 {
			break
		}
		if len(run.Steps) == progressionRemovalMaxSteps {
			t.Fatalf("draw did not finish within %d matches", progressionRemovalMaxSteps)
		}
		match, outcome := ready[0], progressionRemovalHomeWins
		for index, rule := range scenario.Rules {
			if !fired[index] && rule.matches(match) {
				fired[index], outcome = true, rule.Outcome
				break
			}
		}
		final := outcome.final(match)
		input, result := progressionRemovalFinalize(t, pool, run.Competition.ID, match, final)
		progressionRemovalAssertReplay(t, pool, input, result)
		progressionRemovalAssertNoLiveReference(t, pool, run.Competition.ID, match)
		for _, entryID := range final.RemovedEntryIDs {
			run.RemovedBy[entryID] = match
		}
		run.Steps = append(run.Steps, progressionRemovalStep{Match: match, Outcome: outcome, Result: result})
	}
	for index, rule := range scenario.Rules {
		if !fired[index] {
			t.Fatalf("rule %+v never matched a ready match", rule)
		}
	}
	return run
}

func progressionRemovalReadyMatches(t *testing.T, pool *pgxpool.Pool, competitionID string) []progressionRemovalMatch {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT id::text,bracket,round_number,match_number,version,
		home_entry_id::text,away_entry_id::text FROM matches
		WHERE competition_id=$1 AND state='ready' ORDER BY round_number,bracket,match_number,id`, competitionID)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (progressionRemovalMatch, error) {
		var match progressionRemovalMatch
		err := row.Scan(&match.ID, &match.Bracket, &match.Round, &match.Number, &match.Version,
			&match.HomeEntryID, &match.AwayEntryID)
		return match, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// progressionRemovalFinalize writes the terminal state first, then the
// removals, then progression, all in one gated transaction.
func progressionRemovalFinalize(t *testing.T, pool *pgxpool.Pool, competitionID string, match progressionRemovalMatch,
	final progressionRemovalFinal) (matchProgressionInput, matchProgressionResult) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err = lockCompetitionProgressionGate(ctx, tx, competitionID); err != nil {
		t.Fatal(err)
	}
	input := matchProgressionInput{
		MatchID: match.ID, CompetitionID: competitionID, FinalState: final.State, WinnerEntryID: final.WinnerEntryID,
		HomeScore: final.HomeScore, AwayScore: final.AwayScore, Cause: final.Cause,
	}
	if err = tx.QueryRow(ctx, `UPDATE matches SET state=$1,winner_entry_id=$2,completion_reason=$3,
		completed_at=statement_timestamp(),version=version+1,updated_at=now()
		WHERE id=$4 AND competition_id=$5 AND version=$6 AND state='ready' RETURNING version`,
		final.State, final.WinnerEntryID, final.CompletionReason, match.ID, competitionID, match.Version).
		Scan(&input.FinalizedVersion); err != nil {
		t.Fatalf("finalize %s: %v", match, err)
	}
	if len(final.RemovedEntryIDs) > 0 {
		tag, execErr := tx.Exec(ctx, `UPDATE competition_entries SET status='disqualified',updated_at=now()
			WHERE competition_id=$1 AND id = ANY($2::text[]::uuid[])
			  AND status IN ('registered','checked_in','accepted','withdrawal_pending')`,
			competitionID, final.RemovedEntryIDs)
		if execErr != nil || tag.RowsAffected() != int64(len(final.RemovedEntryIDs)) {
			t.Fatalf("remove entries of %s: rows=%d err=%v", match, tag.RowsAffected(), execErr)
		}
	}
	result, err := applyMatchProgression(ctx, tx, input)
	if err != nil {
		t.Fatalf("progress %s: %v", match, err)
	}
	if result.Replayed {
		t.Fatalf("first progression of %s was a replay", match)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return input, result
}

// progressionRemovalAssertReplay re-applies the same finalized version and
// requires the stored result back, without any write.
func progressionRemovalAssertReplay(t *testing.T, pool *pgxpool.Pool, input matchProgressionInput,
	applied matchProgressionResult) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	replay, err := applyMatchProgression(ctx, tx, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay of %s v%d: replayed=%v err=%v", input.MatchID, input.FinalizedVersion, replay.Replayed, err)
	}
	replay.Replayed = false
	want, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replay result differs:\n got %s\nwant %s", got, want)
	}
}

// progressionRemovalAssertNoLiveReference requires that no playable match, and
// no knockout edge into an undecided match, carries a disqualified entry.
// Unreleased round-robin fixtures are exempt until their round is released.
func progressionRemovalAssertNoLiveReference(t *testing.T, pool *pgxpool.Pool, competitionID string,
	after progressionRemovalMatch) {
	t.Helper()
	var playable, carried int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM matches match
		 JOIN competition_entries entry ON entry.id IN (match.home_entry_id, match.away_entry_id)
		 WHERE match.competition_id=$1 AND entry.status='disqualified'
		   AND match.state IN ('ready','in_progress','awaiting_confirmation','disputed')),
		(SELECT count(*) FROM match_slots slot
		 JOIN matches match ON match.id=slot.match_id
		 JOIN competition_entries entry ON entry.id=slot.resolved_entry_id
		 WHERE match.competition_id=$1 AND slot.source_kind<>'entry' AND entry.status='disqualified'
		   AND match.state NOT IN ('completed','forfeit','cancelled'))`,
		competitionID).Scan(&playable, &carried); err != nil {
		t.Fatal(err)
	}
	if playable != 0 || carried != 0 {
		t.Fatalf("after %s: %d playable matches and %d progression slots hold a disqualified entry",
			after, playable, carried)
	}
}

// progressionRemovalAssertSettled checks the end state every scenario shares.
func progressionRemovalAssertSettled(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
	t.Helper()
	ctx := t.Context()
	competition := run.Competition
	var competitionStatus, stageStatus string
	if err := pool.QueryRow(ctx, `SELECT competition.status,stage.status FROM competitions competition
		JOIN competition_stages stage ON stage.competition_id=competition.id
		WHERE competition.id=$1 AND stage.id=$2`, competition.ID, competition.StageID).
		Scan(&competitionStatus, &stageStatus); err != nil {
		t.Fatal(err)
	}
	if competitionStatus != "completed" || stageStatus != "completed" {
		t.Fatalf("competition=%s stage=%s, want both completed", competitionStatus, stageStatus)
	}

	rows, err := pool.Query(ctx, `SELECT id::text FROM competition_entries
		WHERE competition_id=$1 AND status='disqualified'`, competition.ID)
	if err != nil {
		t.Fatal(err)
	}
	disqualified, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(disqualified)
	removed := make([]string, 0, len(run.RemovedBy))
	for entryID := range run.RemovedBy {
		removed = append(removed, entryID)
	}
	slices.Sort(removed)
	if !slices.Equal(disqualified, removed) {
		t.Fatalf("disqualified entries = %v, want %v", disqualified, removed)
	}

	var removedPlaced, livePlaceless int
	if err = pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE entry.status='disqualified' AND placement.entry_id IS NOT NULL),
		count(*) FILTER (WHERE entry.status<>'disqualified' AND placement.entry_id IS NULL)
		FROM competition_draw_entries draw
		JOIN competition_entries entry ON entry.id=draw.entry_id
		LEFT JOIN competition_entry_placements placement
		  ON placement.competition_id=draw.competition_id AND placement.entry_id=draw.entry_id
		WHERE draw.competition_id=$1`, competition.ID).Scan(&removedPlaced, &livePlaceless); err != nil {
		t.Fatal(err)
	}
	if removedPlaced != 0 || livePlaceless != 0 {
		t.Fatalf("%d removed entries have a placement and %d live entries have none", removedPlaced, livePlaceless)
	}

	var unbalanced int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM competition_standings standing
		WHERE standing.stage_id=$1 AND (standing.played<>standing.wins+standing.draws+standing.losses
		  OR standing.played<>(SELECT count(*) FROM matches match
		    WHERE match.stage_id=standing.stage_id AND match.state IN ('completed','forfeit')
		      AND standing.entry_id IN (match.home_entry_id, match.away_entry_id)))`,
		competition.StageID).Scan(&unbalanced); err != nil {
		t.Fatal(err)
	}
	if unbalanced != 0 {
		t.Fatalf("%d standings rows do not balance played = wins + draws + losses = decided fixtures", unbalanced)
	}

	for entryID, match := range run.RemovedBy {
		var advanced, unsettled int
		if err = pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM match_slots WHERE source_match_id=$1 AND resolved_entry_id=$2),
			(SELECT count(*) FROM matches match JOIN competition_stages stage ON stage.id=match.stage_id
			 WHERE match.stage_id=$3 AND stage.format='round_robin' AND match.round_number>$4
			   AND $2 IN (match.home_entry_id, match.away_entry_id)
			   AND NOT (match.state='forfeit' AND match.completion_reason='walkover'
			            AND match.winner_entry_id IS DISTINCT FROM $2
			         OR match.state='cancelled' AND match.completion_reason='double_no_show'))`,
			match.ID, entryID, competition.StageID, match.Round).Scan(&advanced, &unsettled); err != nil {
			t.Fatal(err)
		}
		if advanced != 0 || unsettled != 0 {
			t.Fatalf("entry %s removed in %s advanced %d times and has %d unsettled later fixtures",
				entryID, match, advanced, unsettled)
		}
	}
}

type progressionRemovalMatchRow struct {
	State            string
	CompletionReason *string
	HomeEntryID      *string
	AwayEntryID      *string
	WinnerEntryID    *string
}

func progressionRemovalLoad(t *testing.T, pool *pgxpool.Pool, competitionID, bracket string,
	round, number int) progressionRemovalMatchRow {
	t.Helper()
	var row progressionRemovalMatchRow
	if err := pool.QueryRow(t.Context(), `SELECT state,completion_reason,home_entry_id::text,away_entry_id::text,
		winner_entry_id::text FROM matches
		WHERE competition_id=$1 AND bracket=$2 AND round_number=$3 AND match_number=$4`,
		competitionID, bracket, round, number).
		Scan(&row.State, &row.CompletionReason, &row.HomeEntryID, &row.AwayEntryID, &row.WinnerEntryID); err != nil {
		t.Fatalf("load %s-R%d-M%d: %v", bracket, round, number, err)
	}
	return row
}

func progressionRemovalExpect(t *testing.T, name string, row progressionRemovalMatchRow, state, reason string,
	winner *string) {
	t.Helper()
	if row.State != state || valueOrEmpty(row.CompletionReason) != reason || !sameOptionalString(row.WinnerEntryID, winner) {
		t.Fatalf("%s = %s/%s winner %q, want %s/%s winner %q", name, row.State, valueOrEmpty(row.CompletionReason),
			valueOrEmpty(row.WinnerEntryID), state, reason, valueOrEmpty(winner))
	}
}

func progressionRemovalPlacements(t *testing.T, pool *pgxpool.Pool, competitionID string) map[string]int {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT entry_id::text,placement FROM competition_entry_placements
		WHERE competition_id=$1`, competitionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	placements := map[string]int{}
	for rows.Next() {
		var entryID string
		var placement int
		if err = rows.Scan(&entryID, &placement); err != nil {
			t.Fatal(err)
		}
		placements[entryID] = placement
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return placements
}

func progressionRemovalExpectPlacements(t *testing.T, placements map[string]int, count int, want map[string]int) {
	t.Helper()
	if len(placements) != count {
		t.Fatalf("placement rows = %d (%v), want %d", len(placements), placements, count)
	}
	for entryID, placement := range want {
		if placements[entryID] != placement {
			t.Fatalf("entry %s placement = %d, want %d (all: %v)", entryID, placements[entryID], placement, placements)
		}
	}
}

// progressionRemovalExpectNoFinalists requires that, without a champion,
// nobody is promoted into the removed finalists' first or second place.
func progressionRemovalExpectNoFinalists(t *testing.T, placements map[string]int) {
	t.Helper()
	for entryID, placement := range placements {
		if placement < 3 {
			t.Fatalf("entry %s was promoted to placement %d", entryID, placement)
		}
	}
}

func TestIntegrationProgressionRemovalSingleElimination(t *testing.T) {
	seOptions := integrationSeedOptions{Format: "single_elimination", Entries: 8, ThirdPlace: true}
	progressionRemovalRunScenarios(t, []progressionRemovalScenario{
		{
			Name: "removed semifinal loser hands the bronze a walkover", Options: seOptions,
			Rules: []progressionRemovalRule{
				{Bracket: "main", Round: 1, Number: 1, Outcome: progressionRemovalRemoveHome},
				{Bracket: "main", Round: 2, Number: 1, Outcome: progressionRemovalRemoveAway},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				semifinal := progressionRemovalLoad(t, pool, id, "main", 2, 2)
				bronzeWinner := losingEntry(semifinal.HomeEntryID, semifinal.AwayEntryID, semifinal.WinnerEntryID)
				progressionRemovalExpect(t, "bronze", progressionRemovalLoad(t, pool, id, "bronze", 3, 1),
					"forfeit", "walkover", bronzeWinner)
				final := progressionRemovalLoad(t, pool, id, "main", 3, 1)
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, id), 6, map[string]int{
					*final.WinnerEntryID: 1,
					*losingEntry(final.HomeEntryID, final.AwayEntryID, final.WinnerEntryID): 2,
					*bronzeWinner: 3,
				})
			},
		},
		{
			Name: "both finalists removed leave no champion", Options: seOptions,
			Rules: []progressionRemovalRule{
				{Bracket: "main", Round: 1, Number: 1, Outcome: progressionRemovalRemoveBoth},
				{Bracket: "main", Round: 3, Number: 1, Outcome: progressionRemovalRemoveBoth},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				progressionRemovalExpect(t, "semifinal 1", progressionRemovalLoad(t, pool, id, "main", 2, 1),
					"forfeit", "walkover", progressionRemovalLoad(t, pool, id, "main", 1, 2).WinnerEntryID)
				progressionRemovalExpect(t, "final", progressionRemovalLoad(t, pool, id, "main", 3, 1),
					"cancelled", "no_result_reported", nil)
				semifinal := progressionRemovalLoad(t, pool, id, "main", 2, 2)
				bronzeWinner := losingEntry(semifinal.HomeEntryID, semifinal.AwayEntryID, semifinal.WinnerEntryID)
				progressionRemovalExpect(t, "bronze", progressionRemovalLoad(t, pool, id, "bronze", 3, 1),
					"forfeit", "walkover", bronzeWinner)
				placements := progressionRemovalPlacements(t, pool, id)
				progressionRemovalExpectPlacements(t, placements, 4, map[string]int{*bronzeWinner: 3})
				progressionRemovalExpectNoFinalists(t, placements)
			},
		},
	})
}

func TestIntegrationProgressionRemovalDoubleElimination(t *testing.T) {
	deOptions := func(entries int) integrationSeedOptions {
		return integrationSeedOptions{Format: "double_elimination", Entries: entries}
	}
	progressionRemovalRunScenarios(t, []progressionRemovalScenario{
		{
			Name: "two entries: removed winners finalist loser", Options: deOptions(2),
			Rules: []progressionRemovalRule{{Bracket: "winners", Round: 1, Number: 1, Outcome: progressionRemovalRemoveAway}},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				champion := progressionRemovalLoad(t, pool, id, "winners", 1, 1).WinnerEntryID
				progressionRemovalExpect(t, "grand final", progressionRemovalLoad(t, pool, id, "grand_final", 1, 1),
					"forfeit", "walkover", champion)
				progressionRemovalExpect(t, "reset", progressionRemovalLoad(t, pool, id, "grand_final", 2, 1),
					"cancelled", "reset_not_required", nil)
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, id), 1,
					map[string]int{*champion: 1})
			},
		},
		{
			Name: "two entries: both removed", Options: deOptions(2),
			Rules: []progressionRemovalRule{{Bracket: "winners", Round: 1, Number: 1, Outcome: progressionRemovalRemoveBoth}},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				progressionRemovalExpect(t, "grand final", progressionRemovalLoad(t, pool, id, "grand_final", 1, 1),
					"cancelled", "double_no_show", nil)
				progressionRemovalExpect(t, "reset", progressionRemovalLoad(t, pool, id, "grand_final", 2, 1),
					"cancelled", "reset_not_required", nil)
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, id), 0, nil)
			},
		},
		{
			Name:    "four entries: winners loser removed, then the winners champion in the grand final",
			Options: deOptions(4),
			Rules: []progressionRemovalRule{
				{Bracket: "winners", Round: 1, Number: 1, Outcome: progressionRemovalRemoveAway},
				{Bracket: "grand_final", Round: 1, Number: 1, Outcome: progressionRemovalRemoveHome},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				fed := progressionRemovalLoad(t, pool, id, "winners", 1, 2)
				progressionRemovalExpect(t, "losers round 1", progressionRemovalLoad(t, pool, id, "losers", 1, 1),
					"forfeit", "walkover", losingEntry(fed.HomeEntryID, fed.AwayEntryID, fed.WinnerEntryID))
				grandFinal := progressionRemovalLoad(t, pool, id, "grand_final", 1, 1)
				progressionRemovalExpect(t, "reset", progressionRemovalLoad(t, pool, id, "grand_final", 2, 1),
					"forfeit", "walkover", grandFinal.AwayEntryID)
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, id), 2,
					map[string]int{*grandFinal.AwayEntryID: 1})
			},
		},
		{
			Name: "four entries: losers champion removed in the grand final", Options: deOptions(4),
			Rules: []progressionRemovalRule{{Bracket: "grand_final", Round: 1, Number: 1, Outcome: progressionRemovalRemoveAway}},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				grandFinal := progressionRemovalLoad(t, pool, id, "grand_final", 1, 1)
				progressionRemovalExpect(t, "reset", progressionRemovalLoad(t, pool, id, "grand_final", 2, 1),
					"cancelled", "reset_not_required", nil)
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, id), 3,
					map[string]int{*grandFinal.HomeEntryID: 1})
			},
		},
		{
			Name: "four entries: both grand finalists removed", Options: deOptions(4),
			Rules: []progressionRemovalRule{{Bracket: "grand_final", Round: 1, Number: 1, Outcome: progressionRemovalRemoveBoth}},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				id := run.Competition.ID
				progressionRemovalExpect(t, "reset", progressionRemovalLoad(t, pool, id, "grand_final", 2, 1),
					"cancelled", "reset_not_required", nil)
				placements := progressionRemovalPlacements(t, pool, id)
				progressionRemovalExpectPlacements(t, placements, 2, nil)
				progressionRemovalExpectNoFinalists(t, placements)
			},
		},
		{
			Name: "eight entries: removals and a review across the winners bracket", Options: deOptions(8),
			Rules: []progressionRemovalRule{
				{Bracket: "winners", Round: 1, Number: 1, Outcome: progressionRemovalRemoveAway},
				{Bracket: "winners", Round: 1, Number: 2, Outcome: progressionRemovalRemoveBoth},
				{Bracket: "winners", Round: 1, Number: 3, Outcome: progressionRemovalReviewAwayWins},
				{Bracket: "winners", Round: 2, Number: 2, Outcome: progressionRemovalRemoveHome},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, run.Competition.ID), 4, nil)
			},
		},
	})
}

func TestIntegrationProgressionRemovalRoundRobin(t *testing.T) {
	progressionRemovalRunScenarios(t, []progressionRemovalScenario{
		{
			Name: "odd field with byes", Options: integrationSeedOptions{Format: "round_robin", Entries: 5},
			Rules: []progressionRemovalRule{
				{Bracket: "main", Round: 1, Outcome: progressionRemovalRemoveHome},
				{Bracket: "main", Round: 1, Outcome: progressionRemovalDraw},
				{Bracket: "main", Round: 2, Outcome: progressionRemovalRemoveBoth},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, run.Competition.ID), 2, nil)
			},
		},
		{
			Name:    "two groups released stage-wide",
			Options: integrationSeedOptions{Format: "round_robin", Entries: 8, GroupCount: 2},
			Rules: []progressionRemovalRule{
				{Bracket: "group_a", Round: 1, Outcome: progressionRemovalRemoveAway},
				{Bracket: "group_b", Round: 1, Outcome: progressionRemovalDraw},
				{Bracket: "group_b", Round: 2, Outcome: progressionRemovalReviewRemoveBoth},
			},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, run.Competition.ID), 5, nil)
			},
		},
		{
			Name:    "double round robin settles whole rounds to a fixpoint",
			Options: integrationSeedOptions{Format: "round_robin", Entries: 4, DoubleRoundRobin: true},
			Rules:   []progressionRemovalRule{{Bracket: "main", Round: 1, Outcome: progressionRemovalRemoveBoth}},
			Check: func(t *testing.T, pool *pgxpool.Pool, run progressionRemovalRun) {
				stageID := run.Competition.StageID
				// Round 1's last result releases round 2; with both round-1 losers
				// removed, rounds 2 and 3 are all walkovers and round 4 is released in
				// the same transaction: one double no-show and one playable fixture.
				roundOne := slices.IndexFunc(run.Steps, func(step progressionRemovalStep) bool {
					return step.Match.Round == 1 && step.Outcome == progressionRemovalHomeWins
				})
				if roundOne < 0 {
					t.Fatal("round 1 was not completed by a played match")
				}
				released := run.Steps[roundOne].Result
				wantRounds := []matchProgressionReleasedRound{
					{StageID: stageID, Round: 2}, {StageID: stageID, Round: 3}, {StageID: stageID, Round: 4},
				}
				if !slices.Equal(released.ReleasedRounds, wantRounds) || len(released.ForfeitMatchIDs) != 4 ||
					len(released.CancelledMatchIDs) != 1 || len(released.ReadiedMatchIDs) != 1 ||
					len(released.StandingsMatchIDs) != 5 {
					t.Fatalf("round 1 release = %+v", released)
				}
				last := run.Steps[len(run.Steps)-1].Result
				wantRounds = []matchProgressionReleasedRound{{StageID: stageID, Round: 5}, {StageID: stageID, Round: 6}}
				if !slices.Equal(last.ReleasedRounds, wantRounds) || len(last.ForfeitMatchIDs) != 4 ||
					!slices.Equal(last.CompletedStageIDs, []string{stageID}) ||
					!slices.Equal(last.CompletedCompetitionIDs, []string{run.Competition.ID}) || !last.PlacementsWritten {
					t.Fatalf("final release = %+v", last)
				}
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, pool, run.Competition.ID), 2, nil)
			},
		},
	})
}
