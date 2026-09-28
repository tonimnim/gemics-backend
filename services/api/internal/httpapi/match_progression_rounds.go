package httpapi

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type progressionRoundKey struct {
	StageID string
	Round   int
}

// roundFixtureSettlement is how a released round-robin fixture starts. Only a
// fixture between two live entries is played; any other is settled at release,
// the way planChildSettlement settles a knockout match with a void slot.
type roundFixtureSettlement struct {
	State            string
	WinnerEntryID    *string
	CompletionReason string
	NotLiveEntryIDs  []string
}

// planRoundFixtureSettlement is a pure function: a walkover to the only live
// entry, a double no-show when neither is live, otherwise ready to play.
func planRoundFixtureSettlement(home, away string, homeLive, awayLive bool) roundFixtureSettlement {
	switch {
	case homeLive && awayLive:
		return roundFixtureSettlement{State: "ready"}
	case homeLive:
		return roundFixtureSettlement{State: "forfeit", WinnerEntryID: &home, CompletionReason: "walkover",
			NotLiveEntryIDs: []string{away}}
	case awayLive:
		return roundFixtureSettlement{State: "forfeit", WinnerEntryID: &away, CompletionReason: "walkover",
			NotLiveEntryIDs: []string{home}}
	default:
		return roundFixtureSettlement{State: "cancelled", CompletionReason: "double_no_show",
			NotLiveEntryIDs: []string{home, away}}
	}
}

// releaseProgressionRounds starts the next round of every stage whose current
// round the sources completed. Rounds are released stage-wide, across groups.
// Fixtures settled at release are returned so the caller can progress them as
// sources and release again.
func releaseProgressionRounds(ctx context.Context, tx pgx.Tx, sources []progressionSource,
	result *matchProgressionResult) ([]progressionSource, error) {
	unique := make(map[progressionRoundKey]progressionSource, len(sources))
	for _, source := range sources {
		key := progressionRoundKey{StageID: source.StageID, Round: source.RoundNumber}
		if previous, exists := unique[key]; !exists || previous.CompletedAt.Before(source.CompletedAt) {
			unique[key] = source
		}
	}
	keys := make([]progressionRoundKey, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].StageID != keys[j].StageID {
			return keys[i].StageID < keys[j].StageID
		}
		return keys[i].Round < keys[j].Round
	})
	settled := make([]progressionSource, 0)
	for _, key := range keys {
		source := unique[key]
		var currentComplete bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (
			SELECT 1 FROM matches WHERE stage_id=$1 AND round_number=$2
			AND state NOT IN ('completed','forfeit','cancelled'))`, key.StageID, key.Round).Scan(&currentComplete); err != nil {
			return nil, err
		}
		if !currentComplete {
			continue
		}
		var nextRound *int
		if err := tx.QueryRow(ctx, `SELECT min(round_number) FROM matches
			WHERE stage_id=$1 AND round_number>$2`, key.StageID, key.Round).Scan(&nextRound); err != nil {
			return nil, err
		}
		if nextRound == nil {
			continue
		}
		var nextPending bool
		if err := tx.QueryRow(ctx, `SELECT bool_and(state='pending') FROM matches
			WHERE stage_id=$1 AND round_number=$2`, key.StageID, *nextRound).Scan(&nextPending); err != nil {
			return nil, err
		}
		if !nextPending {
			continue
		}
		matchIDs, err := loadProgressionRoundMatchIDs(ctx, tx, key.StageID, *nextRound)
		if err != nil {
			return nil, err
		}
		readiedIDs, settledIDs := make([]string, 0, len(matchIDs)), make([]string, 0)
		for _, matchID := range matchIDs {
			fixture, settleErr := settleRoundFixture(ctx, tx, source, *nextRound, matchID, result)
			if settleErr != nil {
				return nil, settleErr
			}
			if fixture == nil {
				readiedIDs = append(readiedIDs, matchID)
				continue
			}
			settled = append(settled, *fixture)
			settledIDs = append(settledIDs, matchID)
		}
		result.ReleasedRounds = append(result.ReleasedRounds,
			matchProgressionReleasedRound{StageID: source.StageID, Round: *nextRound})
		if err = insertProgressionEvent(ctx, tx, progressionEventInput{
			CompetitionID: source.CompetitionID, StageID: source.StageID, SourceMatchID: &source.ID,
			Kind: "round_released", Cause: source.Cause, ActorUserID: source.ActorUserID,
			Detail: map[string]any{
				"round": *nextRound, "matchIds": matchIDs,
				"readiedMatchIds": readiedIDs, "settledMatchIds": settledIDs,
			}, OccurredAt: source.CompletedAt,
		}); err != nil {
			return nil, err
		}
		if err = insertProgressionOutbox(ctx, tx, "competition_stage", source.StageID, "round.released", map[string]any{
			"competitionId": source.CompetitionID, "stageId": source.StageID,
			"round": *nextRound, "matchIds": matchIDs,
			"readiedMatchIds": readiedIDs, "settledMatchIds": settledIDs,
		}); err != nil {
			return nil, err
		}
	}
	return settled, nil
}

// roundFixture is one locked, pending fixture of a released round with the
// settlement its entries' status calls for.
type roundFixture struct {
	MatchID                  string
	Version                  int
	GroupKey                 string
	PersistedSchedule        *time.Time
	HomeEntryID, AwayEntryID *string
	HomeStatus, AwayStatus   *string
	Settlement               roundFixtureSettlement
}

// settleRoundFixture starts one fixture of a released round. A fixture between
// two live entries is readied on its persisted schedule and nil is returned.
// Otherwise the fixture is settled at once, completed at the releasing source's
// time, and returned as a progression source of its own. A removed entry's
// already-played results stand; only its unplayed fixtures are settled.
func settleRoundFixture(ctx context.Context, tx pgx.Tx, source progressionSource, round int, matchID string,
	result *matchProgressionResult) (*progressionSource, error) {
	fixture, err := lockRoundFixture(ctx, tx, source.CompetitionID, matchID)
	if err != nil {
		return nil, err
	}
	if fixture.Settlement.State == "ready" {
		return nil, readyRoundFixture(ctx, tx, source, round, fixture, result)
	}
	return settleNotLiveRoundFixture(ctx, tx, source, round, fixture, result)
}

// lockRoundFixture locks a pending, unconditional fixture whose two slots are
// resolved and plans its settlement from both entries' status.
func lockRoundFixture(ctx context.Context, tx pgx.Tx, competitionID, matchID string) (roundFixture, error) {
	fixture := roundFixture{MatchID: matchID}
	var state, activationRule string
	if err := tx.QueryRow(ctx, `SELECT state,activation_rule,version,scheduled_at,
		home_entry_id::text,away_entry_id::text,COALESCE(group_key,'main') FROM matches
		WHERE id=$1 AND competition_id=$2 FOR UPDATE`, matchID, competitionID).
		Scan(&state, &activationRule, &fixture.Version, &fixture.PersistedSchedule, &fixture.HomeEntryID,
			&fixture.AwayEntryID, &fixture.GroupKey); err != nil {
		return roundFixture{}, err
	}
	if state != "pending" || activationRule != "unconditional" {
		return roundFixture{}, errMatchProgressionConflict
	}
	var resolved, voided int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE resolved_entry_id IS NOT NULL),
		count(*) FILTER (WHERE voided_at IS NOT NULL) FROM match_slots WHERE match_id=$1`, matchID).
		Scan(&resolved, &voided); err != nil {
		return roundFixture{}, err
	}
	if resolved != 2 || voided != 0 || fixture.HomeEntryID == nil || fixture.AwayEntryID == nil {
		return roundFixture{}, errMatchProgressionConflict
	}
	var err error
	if fixture.HomeStatus, err = progressionEntryStatus(ctx, tx, *fixture.HomeEntryID); err != nil {
		return roundFixture{}, err
	}
	if fixture.AwayStatus, err = progressionEntryStatus(ctx, tx, *fixture.AwayEntryID); err != nil {
		return roundFixture{}, err
	}
	fixture.Settlement = planRoundFixtureSettlement(*fixture.HomeEntryID, *fixture.AwayEntryID,
		progressionEntryIsLive(fixture.HomeStatus), progressionEntryIsLive(fixture.AwayStatus))
	return fixture, nil
}

// readyRoundFixture opens a fixture between two live entries for check-in on
// its persisted schedule, never earlier than one check-in lead after the
// releasing source completed.
func readyRoundFixture(ctx context.Context, tx pgx.Tx, source progressionSource, round int, fixture roundFixture,
	result *matchProgressionResult) error {
	minimumSchedule := source.CompletedAt.Add(time.Duration(source.StageConfig.CheckInLeadMinutes) * time.Minute)
	scheduled := minimumSchedule
	if fixture.PersistedSchedule != nil && fixture.PersistedSchedule.After(scheduled) {
		scheduled = fixture.PersistedSchedule.UTC()
	}
	opens := scheduled.Add(-time.Duration(source.StageConfig.CheckInLeadMinutes) * time.Minute)
	closes := scheduled.Add(time.Duration(source.StageConfig.CheckInGraceMinutes) * time.Minute)
	due := scheduled.Add(time.Duration(source.StageConfig.ResultWindowMinutes) * time.Minute)
	var nextVersion int
	err := tx.QueryRow(ctx, `UPDATE matches SET state='ready',scheduled_at=$1,
		check_in_opens_at=$2,check_in_closes_at=$3,result_due_at=$4,
		version=version+1,updated_at=now()
		WHERE id=$5 AND competition_id=$6 AND version=$7 AND state='pending'
		RETURNING version`, scheduled, opens, closes, due, fixture.MatchID, source.CompetitionID, fixture.Version).
		Scan(&nextVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return errMatchProgressionConflict
	}
	if err != nil {
		return err
	}
	result.ReadiedMatchIDs = append(result.ReadiedMatchIDs, fixture.MatchID)
	return insertProgressionOutbox(ctx, tx, "match", fixture.MatchID, "match.ready", map[string]any{
		"matchId": fixture.MatchID, "competitionId": source.CompetitionID, "stageId": source.StageID,
		"round": round, "matchVersion": nextVersion,
	})
}

// settleNotLiveRoundFixture settles a fixture with a withdrawn or removed
// entry as a walkover or a double no-show, and returns it as a progression
// source so its standings and the next release follow.
func settleNotLiveRoundFixture(ctx context.Context, tx pgx.Tx, source progressionSource, round int,
	fixture roundFixture, result *matchProgressionResult) (*progressionSource, error) {
	settlement, matchID := fixture.Settlement, fixture.MatchID
	var nextVersion int
	err := tx.QueryRow(ctx, `UPDATE matches SET state=$1,winner_entry_id=$2,completion_reason=$3,
		completed_at=$4,version=version+1,updated_at=now()
		WHERE id=$5 AND competition_id=$6 AND version=$7 AND state='pending'
		RETURNING version`, settlement.State, settlement.WinnerEntryID, settlement.CompletionReason,
		source.CompletedAt, matchID, source.CompetitionID, fixture.Version).Scan(&nextVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errMatchProgressionConflict
	}
	if err != nil {
		return nil, err
	}
	cause := progressionCauseWithdrawal
	if *fixture.HomeStatus == "disqualified" || *fixture.AwayStatus == "disqualified" {
		cause = progressionCauseDisqualification
	}
	kind, eventType := "match_forfeited", "match.forfeited"
	if settlement.State == "cancelled" {
		kind, eventType = "match_cancelled", "match.cancelled"
		result.CancelledMatchIDs = append(result.CancelledMatchIDs, matchID)
	} else {
		result.ForfeitMatchIDs = append(result.ForfeitMatchIDs, matchID)
	}
	if err = insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: source.CompetitionID, StageID: source.StageID,
		SourceMatchID: &source.ID, TargetMatchID: &matchID, Kind: kind, Cause: cause,
		ActorUserID: source.ActorUserID, Detail: map[string]any{
			"reason": "entry_not_live", "entryIds": settlement.NotLiveEntryIDs, "targetMatchVersion": nextVersion,
		}, OccurredAt: source.CompletedAt,
	}); err != nil {
		return nil, err
	}
	if err = insertProgressionOutbox(ctx, tx, "match", matchID, eventType, map[string]any{
		"matchId": matchID, "competitionId": source.CompetitionID, "stageId": source.StageID,
		"matchVersion": nextVersion, "winnerEntryId": settlement.WinnerEntryID,
		"completionReason": settlement.CompletionReason,
	}); err != nil {
		return nil, err
	}
	completionReason := settlement.CompletionReason
	return &progressionSource{
		ID: matchID, CompetitionID: source.CompetitionID, StageID: source.StageID,
		StageFormat: source.StageFormat, StageConfig: source.StageConfig, GroupKey: fixture.GroupKey,
		RoundNumber: round, State: settlement.State, Version: nextVersion,
		HomeEntryID: fixture.HomeEntryID, AwayEntryID: fixture.AwayEntryID,
		WinnerEntryID:    cloneOptionalString(settlement.WinnerEntryID),
		CompletionReason: &completionReason, CompletedAt: source.CompletedAt,
		Cause: cause, ActorUserID: cloneOptionalString(source.ActorUserID),
	}, nil
}

func loadProgressionRoundMatchIDs(ctx context.Context, tx pgx.Tx, stageID string, round int) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM matches
		WHERE stage_id=$1 AND round_number=$2 ORDER BY graph_rank,id`, stageID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
