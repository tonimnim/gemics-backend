package httpapi

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type progressionStandingsWork struct {
	Source progressionSource
}

type progressionStandingDelta struct {
	EntryID      string
	Wins         int
	Draws        int
	Losses       int
	Walkovers    int
	GoalsFor     int
	GoalsAgainst int
	Points       int
}

func roundRobinStandingDeltas(source progressionSource) ([]progressionStandingDelta, error) {
	if source.HomeEntryID == nil || source.AwayEntryID == nil || *source.HomeEntryID == *source.AwayEntryID {
		return nil, fmt.Errorf("%w: round-robin participants are missing or duplicated", errMatchProgressionInvalid)
	}
	home := progressionStandingDelta{EntryID: *source.HomeEntryID}
	away := progressionStandingDelta{EntryID: *source.AwayEntryID}
	if source.HomeScore != nil && source.AwayScore != nil {
		home.GoalsFor, home.GoalsAgainst = *source.HomeScore, *source.AwayScore
		away.GoalsFor, away.GoalsAgainst = *source.AwayScore, *source.HomeScore
	}
	switch {
	case source.WinnerEntryID == nil && source.State == "completed":
		home.Draws, away.Draws, home.Points, away.Points = 1, 1, 1, 1
	case sameOptionalString(source.WinnerEntryID, source.HomeEntryID):
		home.Wins, away.Losses, home.Points = 1, 1, 3
		if source.State == "forfeit" {
			home.Walkovers = 1
		}
	case sameOptionalString(source.WinnerEntryID, source.AwayEntryID):
		away.Wins, home.Losses, away.Points = 1, 1, 3
		if source.State == "forfeit" {
			away.Walkovers = 1
		}
	default:
		return nil, fmt.Errorf("%w: a non-draw round-robin result needs a participant winner", errMatchProgressionInvalid)
	}
	result := []progressionStandingDelta{home, away}
	sort.Slice(result, func(i, j int) bool { return result[i].EntryID < result[j].EntryID })
	return result, nil
}

func applyProgressionStandings(ctx context.Context, tx pgx.Tx, source progressionSource) error {
	deltas, err := roundRobinStandingDeltas(source)
	if err != nil {
		return err
	}
	// Row locks follow entry id, independent of which side was home. A-B and
	// B-A therefore cannot deadlock when separate groups are progressed at once.
	for _, delta := range deltas {
		var found string
		if err = tx.QueryRow(ctx, `SELECT entry_id::text FROM competition_standings
			WHERE stage_id=$1 AND competition_id=$2 AND entry_id=$3 FOR UPDATE`,
			source.StageID, source.CompetitionID, delta.EntryID).Scan(&found); err != nil {
			return err
		}
	}
	for _, delta := range deltas {
		command, updateErr := tx.Exec(ctx, `UPDATE competition_standings SET
			played=played+1,wins=wins+$1,draws=draws+$2,losses=losses+$3,
			walkovers=walkovers+$4,goals_for=goals_for+$5,goals_against=goals_against+$6,
			points=points+$7,standings_version=standings_version+1,updated_at=now()
			WHERE stage_id=$8 AND competition_id=$9 AND entry_id=$10`,
			delta.Wins, delta.Draws, delta.Losses, delta.Walkovers, delta.GoalsFor,
			delta.GoalsAgainst, delta.Points, source.StageID, source.CompetitionID, delta.EntryID)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return errMatchProgressionConflict
		}
	}
	if err = insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: source.CompetitionID, StageID: source.StageID, SourceMatchID: &source.ID,
		Kind: "standings_updated", Cause: source.Cause, ActorUserID: source.ActorUserID,
		Detail: map[string]any{
			"matchVersion": source.Version, "groupKey": source.GroupKey,
			"homeEntryId": source.HomeEntryID, "awayEntryId": source.AwayEntryID,
			"homeScore": source.HomeScore, "awayScore": source.AwayScore,
		}, OccurredAt: source.CompletedAt,
	}); err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "competition_stage", source.StageID, "standings.updated", map[string]any{
		"competitionId": source.CompetitionID, "stageId": source.StageID, "groupKey": source.GroupKey,
		"matchId": source.ID, "matchVersion": source.Version,
	})
}
