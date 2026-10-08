package httpapi

import (
	"context"
	"fmt"
	"slices"
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
	// VoidedEntryIDs are the entries that were due to play this match but whose
	// slot was voided because they were no longer live. Placement counts them as
	// having played the match without winning it, so a removed entry keeps the
	// place it had reached and nobody is promoted into it.
	VoidedEntryIDs []string
	// LoserDrop is the round this match's loser is sent to, if any: in double
	// elimination a winners match drops its loser into the losers bracket, or,
	// in a two-entry draw, into the grand final. It comes from the stored graph
	// because pruned byes can send a loser past the round the generator's drop
	// mapping names.
	LoserDrop *progressionPlacementRound
}

type progressionPlacementRound struct {
	Bracket string
	Round   int
}

// rankRoundRobinPlacements requires standings for the whole frozen field, then
// ranks live entries only, so the table re-ranks around removed entries. With
// no live entry left it returns no placement. Placements come back in table
// order. The public standings rank each group table with this function, so a
// single-table stage's ranks equal its placements; a stage with groups is
// placed across all its groups, so it shares only the order and the removal
// rule with its group tables.
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
	sortRoundRobinStandings(ordered)
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

// sortRoundRobinStandings puts rows in table order: points, goal difference and
// goals scored, then the entry id, which keeps exact ties in a stable order.
func sortRoundRobinStandings(rows []progressionStandingForPlacement) {
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
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
}

func sameRoundRobinPlacementScore(left, right progressionStandingForPlacement) bool {
	return left.Points == right.Points && left.GoalsFor-left.GoalsAgainst == right.GoalsFor-right.GoalsAgainst &&
		left.GoalsFor == right.GoalsFor
}

// rankEliminationPlacements ranks the whole frozen field by exit depth (see
// placementDepths), then omits non-live entries. An entry exits at the deepest
// match it played and did not win, counting a match it was voided from as one
// it did not win, so a removed entry keeps the place it had reached and nobody
// is promoted into it; a final cancelled between two removed entries leaves no
// first place. Placements are competition ranks: one plus the number of
// entries that went deeper, so they never exceed the field. A bronze match
// never sets an exit depth: its semifinal losers exit at the semifinal, and
// only a bronze winner overrides them to third and fourth. Without a champion
// nobody is placed first: the finalists share second, and every other
// placement is unchanged.
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

	depthOf := placementDepths(matches, format)
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
			if !setsPlacementDepth(match) || !placementParticipant(entryID, match) {
				continue
			}
			depth := depthOf(match)
			if depth > participationRank {
				participationRank = depth
			}
			// A voided entry never won the match it was voided from.
			won := match.WinnerEntryID != nil && *match.WinnerEntryID == entryID
			if !won && depth > exitRank {
				exitRank = depth
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
		// With a champion every exit is already second or lower; without one, the
		// finalists' shared first becomes second.
		result = append(result, progressionPlacement{EntryID: item.EntryID, Placement: max(placement, 2)})
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

// placementDepths says how deep an exit at each match sits. Depth is by round,
// never by graph rank: byes give some matches of a round a shorter path
// through the graph, but round numbers are counted over the full power-of-two
// tree and never renumbered by pruning, so every exit in one round shares a
// depth.
//
// Single elimination ranks by main-bracket round. Double elimination ranks by
// losers round, with the grand final above the losers final. A winners match
// knocks nobody out, so it counts at the round its loser drops into: an entry
// voided from it was guaranteed that round, and it ties with the entries that
// went out there instead of with earlier exits.
func placementDepths(matches []progressionMatchForPlacement, format string) func(progressionMatchForPlacement) int {
	if format != "double_elimination" {
		return func(match progressionMatchForPlacement) int { return match.RoundNumber }
	}
	losersRounds := 0
	for _, match := range matches {
		if match.Bracket == "losers" {
			losersRounds = max(losersRounds, match.RoundNumber)
		}
	}
	grandFinal := losersRounds + 1
	roundDepth := func(bracket string, round int) int {
		if bracket == "losers" {
			return round
		}
		return grandFinal
	}
	return func(match progressionMatchForPlacement) int {
		switch {
		case match.Bracket != "winners":
			return roundDepth(match.Bracket, match.RoundNumber)
		case match.LoserDrop != nil:
			return roundDepth(match.LoserDrop.Bracket, match.LoserDrop.Round)
		}
		// Without the stored drop, use the generator's unpruned mapping:
		// winners round 1 drops into losers round 1, winners round r into
		// losers round 2r-2, and a draw with no losers bracket into the grand
		// final.
		if drop := max(1, 2*match.RoundNumber-2); drop <= losersRounds {
			return drop
		}
		return grandFinal
	}
}

// setsPlacementDepth excludes the matches that must not move an exit: an
// unfinished match, an unneeded bracket reset, and the bronze match, which is
// played alongside the final and would otherwise lift both semifinal losers
// to the final loser's depth.
func setsPlacementDepth(match progressionMatchForPlacement) bool {
	return match.Bracket != "bronze" && isProgressionTerminal(match.State) &&
		(match.CompletionReason == nil || *match.CompletionReason != "reset_not_required")
}

// placementParticipant reports whether the entry played the match or was due
// to play it before its slot was voided.
func placementParticipant(entryID string, match progressionMatchForPlacement) bool {
	return match.HomeEntryID != nil && *match.HomeEntryID == entryID ||
		match.AwayEntryID != nil && *match.AwayEntryID == entryID ||
		slices.Contains(match.VoidedEntryIDs, entryID)
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
	return "champion first, then competition_rank by exit depth (no champion: nobody first, the deepest exits " +
		"share second); exit depth = deepest match played or voided from and not won (single_elimination: " +
		"main-bracket round; double_elimination: losers round, a winners match at the round its loser drops " +
		"into, grand final above the losers final), bronze excluded; bronze result overrides third/fourth" + omitted
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

// loadProgressionPlacementMatches also recovers who each voided slot belonged
// to. A slot is voided either because its source produced nobody (a match with
// no winner, or no loser) or because the entry it resolved to was no longer
// live, and only the second names an entry: the slot's own entry, or the
// source match's winner or loser. It also reads where each match's loser drops:
// match_slots_one_consumer_uidx allows at most one loser_of consumer, so the
// join never repeats a match.
func loadProgressionPlacementMatches(ctx context.Context, tx pgx.Tx,
	stageID string) ([]progressionMatchForPlacement, error) {
	rows, err := tx.Query(ctx, `SELECT target.id::text,target.bracket,target.round_number,target.graph_rank,
		target.state,target.home_entry_id::text,target.away_entry_id::text,target.winner_entry_id::text,
		target.completion_reason,
		ARRAY(SELECT voided.entry_id FROM (
			SELECT slot.slot,CASE slot.source_kind
				WHEN 'entry' THEN slot.source_entry_id
				WHEN 'winner_of' THEN source.winner_entry_id
				WHEN 'loser_of' THEN CASE source.winner_entry_id
					WHEN source.home_entry_id THEN source.away_entry_id
					WHEN source.away_entry_id THEN source.home_entry_id END
				END::text AS entry_id
			FROM match_slots slot
			LEFT JOIN matches source ON source.id=slot.source_match_id
			WHERE slot.match_id=target.id AND slot.voided_at IS NOT NULL) voided
			WHERE voided.entry_id IS NOT NULL ORDER BY voided.slot),
		loser_drop.bracket,loser_drop.round_number
		FROM matches target
		LEFT JOIN match_slots drop_slot ON drop_slot.source_match_id=target.id AND drop_slot.source_kind='loser_of'
		LEFT JOIN matches loser_drop ON loser_drop.id=drop_slot.match_id
		WHERE target.stage_id=$1 ORDER BY target.graph_rank,target.id`, stageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]progressionMatchForPlacement, 0)
	for rows.Next() {
		var match progressionMatchForPlacement
		var dropBracket *string
		var dropRound *int
		if err = rows.Scan(&match.ID, &match.Bracket, &match.RoundNumber, &match.GraphRank, &match.State,
			&match.HomeEntryID, &match.AwayEntryID, &match.WinnerEntryID, &match.CompletionReason,
			&match.VoidedEntryIDs, &dropBracket, &dropRound); err != nil {
			return nil, err
		}
		if dropBracket != nil && dropRound != nil {
			match.LoserDrop = &progressionPlacementRound{Bracket: *dropBracket, Round: *dropRound}
		}
		result = append(result, match)
	}
	return result, rows.Err()
}
