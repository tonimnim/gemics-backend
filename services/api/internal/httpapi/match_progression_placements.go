package httpapi

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type progressionPlacement struct {
	EntryID   string
	Placement int
}

// progressionPlacementEntry is one frozen draw entry. A non-live (removed or
// withdrawing) entry is still part of the field, but receives no placement.
type progressionPlacementEntry struct {
	EntryID string
	Live    bool
}

type progressionStandingForPlacement struct {
	EntryID      string
	Points       int
	GoalsFor     int
	GoalsAgainst int
}

type progressionMatchForPlacement struct {
	ID               string
	Bracket          string
	RoundNumber      int
	GraphRank        int
	State            string
	HomeEntryID      *string
	AwayEntryID      *string
	WinnerEntryID    *string
	CompletionReason *string
}

// rankRoundRobinPlacements requires standings for the whole frozen field, then
// ranks live entries only, so the table re-ranks around removed entries. With
// no live entry left it returns no placement.
func rankRoundRobinPlacements(entries []progressionPlacementEntry,
	standings []progressionStandingForPlacement) ([]progressionPlacement, error) {
	if len(entries) == 0 || len(entries) != len(standings) {
		return nil, fmt.Errorf("%w: round-robin standings do not cover the frozen field", errMatchProgressionInvalid)
	}
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.EntryID] = entry.Live
	}
	ordered := make([]progressionStandingForPlacement, 0, len(standings))
	for _, row := range standings {
		live, exists := known[row.EntryID]
		if !exists {
			return nil, errMatchProgressionConflict
		}
		delete(known, row.EntryID)
		if live {
			ordered = append(ordered, row)
		}
	}
	if len(known) != 0 {
		return nil, errMatchProgressionConflict
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.Points != right.Points {
			return left.Points > right.Points
		}
		leftDifference := left.GoalsFor - left.GoalsAgainst
		rightDifference := right.GoalsFor - right.GoalsAgainst
		if leftDifference != rightDifference {
			return leftDifference > rightDifference
		}
		if left.GoalsFor != right.GoalsFor {
			return left.GoalsFor > right.GoalsFor
		}
		return left.EntryID < right.EntryID
	})
	result := make([]progressionPlacement, len(ordered))
	placement := 1
	for index, row := range ordered {
		if index > 0 && !sameRoundRobinPlacementScore(row, ordered[index-1]) {
			placement = index + 1
		}
		result[index] = progressionPlacement{EntryID: row.EntryID, Placement: placement}
	}
	return result, nil
}

func sameRoundRobinPlacementScore(left, right progressionStandingForPlacement) bool {
	return left.Points == right.Points && left.GoalsFor-left.GoalsAgainst == right.GoalsFor-right.GoalsAgainst &&
		left.GoalsFor == right.GoalsFor
}

// rankEliminationPlacements ranks the whole frozen field by graph depth, then
// omits non-live entries. Nobody is promoted into a removed entry's place, so a
// final cancelled between two removed entries leaves no first place.
func rankEliminationPlacements(entries []progressionPlacementEntry, matches []progressionMatchForPlacement,
	format string) ([]progressionPlacement, error) {
	if len(entries) < 2 || len(matches) == 0 {
		return nil, fmt.Errorf("%w: elimination placement input is incomplete", errMatchProgressionInvalid)
	}
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if _, duplicate := known[entry.EntryID]; duplicate {
			return nil, errMatchProgressionConflict
		}
		known[entry.EntryID] = entry.Live
	}
	championshipBracket := "main"
	if format == "double_elimination" {
		championshipBracket = "grand_final"
	}
	var championship *progressionMatchForPlacement
	for index := range matches {
		match := &matches[index]
		if match.Bracket != championshipBracket || !isProgressionTerminal(match.State) ||
			match.CompletionReason != nil && *match.CompletionReason == "reset_not_required" {
			continue
		}
		if championship == nil || placementMatchLater(*match, *championship) {
			championship = match
		}
	}
	if championship == nil {
		return nil, fmt.Errorf("%w: championship match is missing", errMatchProgressionInvalid)
	}
	champion := cloneOptionalString(championship.WinnerEntryID)
	if champion != nil {
		if _, exists := known[*champion]; !exists {
			return nil, errMatchProgressionConflict
		}
	}

	type exit struct {
		EntryID string
		Rank    int
	}
	exits := make([]exit, 0, len(entries))
	for _, entry := range entries {
		entryID := entry.EntryID
		if champion != nil && entryID == *champion {
			continue
		}
		exitRank, participationRank := -1, -1
		for _, match := range matches {
			if !isProgressionTerminal(match.State) ||
				match.CompletionReason != nil && *match.CompletionReason == "reset_not_required" ||
				!entryParticipated(entryID, match) {
				continue
			}
			if match.GraphRank > participationRank {
				participationRank = match.GraphRank
			}
			if (match.WinnerEntryID == nil || *match.WinnerEntryID != entryID) && match.GraphRank > exitRank {
				exitRank = match.GraphRank
			}
		}
		if exitRank < 0 {
			exitRank = participationRank
		}
		if exitRank < 0 {
			exitRank = 0
		}
		exits = append(exits, exit{EntryID: entryID, Rank: exitRank})
	}
	sort.Slice(exits, func(i, j int) bool {
		if exits[i].Rank != exits[j].Rank {
			return exits[i].Rank > exits[j].Rank
		}
		return exits[i].EntryID < exits[j].EntryID
	})
	result := make([]progressionPlacement, 0, len(entries))
	offset := 1
	if champion != nil {
		result = append(result, progressionPlacement{EntryID: *champion, Placement: 1})
		offset = 2
	}
	placement := offset
	for index, item := range exits {
		if index > 0 && item.Rank != exits[index-1].Rank {
			placement = offset + index
		}
		result = append(result, progressionPlacement{EntryID: item.EntryID, Placement: placement})
	}

	if format == "single_elimination" {
		for _, match := range matches {
			if match.Bracket != "bronze" || !isProgressionTerminal(match.State) || match.WinnerEntryID == nil {
				continue
			}
			loser := losingEntry(match.HomeEntryID, match.AwayEntryID, match.WinnerEntryID)
			if loser == nil {
				continue
			}
			setProgressionPlacement(result, *match.WinnerEntryID, 3)
			setProgressionPlacement(result, *loser, 4)
		}
	}
	placed := make([]progressionPlacement, 0, len(result))
	for _, placement := range result {
		if known[placement.EntryID] {
			placed = append(placed, placement)
		}
	}
	sort.Slice(placed, func(i, j int) bool { return placed[i].EntryID < placed[j].EntryID })
	return placed, nil
}

func entryParticipated(entryID string, match progressionMatchForPlacement) bool {
	return match.HomeEntryID != nil && *match.HomeEntryID == entryID ||
		match.AwayEntryID != nil && *match.AwayEntryID == entryID
}

func placementMatchLater(left, right progressionMatchForPlacement) bool {
	if left.GraphRank != right.GraphRank {
		return left.GraphRank > right.GraphRank
	}
	if left.RoundNumber != right.RoundNumber {
		return left.RoundNumber > right.RoundNumber
	}
	return left.ID > right.ID
}

func setProgressionPlacement(placements []progressionPlacement, entryID string, placement int) {
	for index := range placements {
		if placements[index].EntryID == entryID {
			placements[index].Placement = placement
			return
		}
	}
}

func writeProgressionPlacements(ctx context.Context, tx pgx.Tx, stage progressionStageLock,
	trigger progressionSource, input matchProgressionInput) (int, error) {
	var format string
	if err := tx.QueryRow(ctx, `SELECT format FROM competition_stages
		WHERE id=$1 AND competition_id=$2`, stage.ID, stage.CompetitionID).Scan(&format); err != nil {
		return 0, err
	}
	entries, err := loadProgressionPlacementEntries(ctx, tx, stage.CompetitionID)
	if err != nil {
		return 0, err
	}
	var placements []progressionPlacement
	switch format {
	case "round_robin":
		standings, loadErr := loadProgressionPlacementStandings(ctx, tx, stage.ID)
		if loadErr != nil {
			return 0, loadErr
		}
		placements, err = rankRoundRobinPlacements(entries, standings)
	case "single_elimination", "double_elimination":
		matches, loadErr := loadProgressionPlacementMatches(ctx, tx, stage.ID)
		if loadErr != nil {
			return 0, loadErr
		}
		placements, err = rankEliminationPlacements(entries, matches, format)
	default:
		return 0, fmt.Errorf("%w: unsupported stage format %s", errMatchProgressionInvalid, format)
	}
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, placement := range placements {
		command, insertErr := tx.Exec(ctx, `INSERT INTO competition_entry_placements
			(competition_id,entry_id,placement,finalized_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (competition_id,entry_id) DO NOTHING`, stage.CompetitionID, placement.EntryID,
			placement.Placement, trigger.CompletedAt)
		if insertErr != nil {
			return 0, insertErr
		}
		if command.RowsAffected() == 1 {
			inserted++
			continue
		}
		var existing int
		if queryErr := tx.QueryRow(ctx, `SELECT placement FROM competition_entry_placements
			WHERE competition_id=$1 AND entry_id=$2`, stage.CompetitionID, placement.EntryID).Scan(&existing); queryErr != nil {
			return 0, queryErr
		}
		if existing != placement.Placement {
			return 0, errMatchProgressionConflict
		}
	}
	if inserted == 0 {
		return 0, nil
	}
	omittedEntryIDs := make([]string, 0)
	for _, entry := range entries {
		if !entry.Live {
			omittedEntryIDs = append(omittedEntryIDs, entry.EntryID)
		}
	}
	if err = insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: stage.CompetitionID, StageID: stage.ID, SourceMatchID: &input.MatchID,
		Kind: "placements_written", Cause: input.Cause, ActorUserID: input.ActorUserID,
		Detail: map[string]any{
			"entryCount": inserted, "format": format,
			"rankingRule": progressionPlacementRule(format), "omittedEntryIds": omittedEntryIDs,
		}, OccurredAt: trigger.CompletedAt,
	}); err != nil {
		return 0, err
	}
	if err = insertProgressionOutbox(ctx, tx, "competition", stage.CompetitionID,
		"competition.placements_written", map[string]any{
			"competitionId": stage.CompetitionID, "stageId": stage.ID,
			"entryCount": inserted, "format": format,
		}); err != nil {
		return 0, err
	}
	return inserted, nil
}

func progressionPlacementRule(format string) string {
	const omitted = "; non-live (removed) entries receive no placement"
	if format == "round_robin" {
		return "competition_rank(points,goal_difference,goals_for); exact metric ties share placement" + omitted
	}
	return "champion_then_competition_rank_by_terminal_graph_depth; bronze result overrides third/fourth" + omitted
}

func loadProgressionPlacementEntries(ctx context.Context, tx pgx.Tx,
	competitionID string) ([]progressionPlacementEntry, error) {
	rows, err := tx.Query(ctx, `SELECT draw.entry_id::text,entry.status
		FROM competition_draw_entries draw
		JOIN competition_entries entry ON entry.id=draw.entry_id
		WHERE draw.competition_id=$1 ORDER BY draw.draw_position`, competitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]progressionPlacementEntry, 0)
	for rows.Next() {
		var entry progressionPlacementEntry
		var status string
		if err = rows.Scan(&entry.EntryID, &status); err != nil {
			return nil, err
		}
		entry.Live = progressionEntryIsLive(&status)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func loadProgressionPlacementStandings(ctx context.Context, tx pgx.Tx,
	stageID string) ([]progressionStandingForPlacement, error) {
	rows, err := tx.Query(ctx, `SELECT entry_id::text,points,goals_for,goals_against
		FROM competition_standings WHERE stage_id=$1 ORDER BY entry_id`, stageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]progressionStandingForPlacement, 0)
	for rows.Next() {
		var row progressionStandingForPlacement
		if err = rows.Scan(&row.EntryID, &row.Points, &row.GoalsFor, &row.GoalsAgainst); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func loadProgressionPlacementMatches(ctx context.Context, tx pgx.Tx,
	stageID string) ([]progressionMatchForPlacement, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,bracket,round_number,graph_rank,state,
		home_entry_id::text,away_entry_id::text,winner_entry_id::text,completion_reason
		FROM matches WHERE stage_id=$1 ORDER BY graph_rank,id`, stageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]progressionMatchForPlacement, 0)
	for rows.Next() {
		var match progressionMatchForPlacement
		if err = rows.Scan(&match.ID, &match.Bracket, &match.RoundNumber, &match.GraphRank, &match.State,
			&match.HomeEntryID, &match.AwayEntryID, &match.WinnerEntryID, &match.CompletionReason); err != nil {
			return nil, err
		}
		result = append(result, match)
	}
	return result, rows.Err()
}
