package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	gamicscache "github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// competitionStandings is the public table view of a competition: one ranked
// table per round-robin group, and the final placements of every format once
// progression has written them.
type competitionStandings struct {
	CompetitionID string                      `json:"competitionId"`
	Stages        []competitionStandingsStage `json:"stages"`
	Placements    []competitionPlacement      `json:"placements"`
}

type competitionStandingsStage struct {
	ID       string                      `json:"id"`
	Name     string                      `json:"name"`
	Position int                         `json:"position"`
	Status   string                      `json:"status"`
	Groups   []competitionStandingsGroup `json:"groups"`
}

type competitionStandingsGroup struct {
	Key  string                   `json:"key"`
	Rows []competitionStandingRow `json:"rows"`
}

// competitionStandingRow is one entry of a group table. Rank is nil exactly
// when Removed is true: a removed entry keeps the results it played, but it is
// out of the ranking as it is out of the placements.
type competitionStandingRow struct {
	Rank           *int   `json:"rank"`
	EntryID        string `json:"entryId"`
	DisplayName    string `json:"displayName"`
	Played         int    `json:"played"`
	Wins           int    `json:"wins"`
	Draws          int    `json:"draws"`
	Losses         int    `json:"losses"`
	GoalsFor       int    `json:"goalsFor"`
	GoalsAgainst   int    `json:"goalsAgainst"`
	GoalDifference int    `json:"goalDifference"`
	Points         int    `json:"points"`
	Walkovers      int    `json:"walkovers"`
	Removed        bool   `json:"removed"`
}

type competitionPlacement struct {
	Placement   int    `json:"placement"`
	EntryID     string `json:"entryId"`
	DisplayName string `json:"displayName"`
}

// competitionStandingRecord is one stored standings row with its stage, its
// group and the entry status that decides whether it is ranked.
type competitionStandingRecord struct {
	StageID       string
	StageName     string
	StagePosition int
	StageStatus   string
	GroupKey      string
	EntryStatus   string
	Row           competitionStandingRow
}

// competitionStandingsCacheKey is shared by the read and the invalidation, so
// the two can never drift apart.
func competitionStandingsCacheKey(competitionID string) string {
	return "competition-standings:" + competitionID
}

// getCompetitionStandings serves exactly the competitions the bracket serves,
// cancelled ones included.
func (s *Server) getCompetitionStandings(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	// A result changes the tables as it changes the bracket, so both keep the
	// same freshness and are dropped together after every progression commit.
	response, err := s.responses.GetOrLoad(r.Context(), competitionStandingsCacheKey(id), gamicscache.Policy{
		FreshFor: 2 * time.Second, KeepFor: 20 * time.Second, LoadTimeout: 3 * time.Second,
		LockFor: 3 * time.Second, WaitFor: 400 * time.Millisecond, MaxBodyBytes: 2 << 20,
	}, func(ctx context.Context) ([]byte, error) {
		standings, loadErr := readPublicCompetition(ctx, s, func(pool *pgxpool.Pool) (competitionStandings, error) {
			return queryCompetitionStandings(ctx, pool, id)
		})
		if loadErr != nil {
			return nil, loadErr
		}
		return json.Marshal(map[string]any{"data": standings})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "standings_unavailable", "The standings are temporarily unavailable.")
		return
	}
	writeCompetitionCacheResponse(w, r, response, "public, max-age=1, s-maxage=2, stale-while-revalidate=20")
}

// queryCompetitionStandings reads the tables and the placements in one
// read-only snapshot. Progression writes the last standings update and the
// placements in the same transaction, so a read never sees one without the
// other.
func queryCompetitionStandings(ctx context.Context, pool *pgxpool.Pool, id string) (competitionStandings, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return competitionStandings{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var competitionStatus string
	if err = tx.QueryRow(ctx, `SELECT competition.status FROM competitions competition
		WHERE competition.id=$1 AND `+readableCompetitionSQL, id).Scan(&competitionStatus); err != nil {
		return competitionStandings{}, err
	}
	records, err := queryCompetitionStandingRecords(ctx, tx, id)
	if err != nil {
		return competitionStandings{}, err
	}
	stages, err := buildCompetitionStandingsStages(records, competitionStatus)
	if err != nil {
		return competitionStandings{}, err
	}
	placements, err := queryCompetitionPlacements(ctx, tx, id)
	if err != nil {
		return competitionStandings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return competitionStandings{}, err
	}
	return competitionStandings{CompetitionID: id, Stages: stages, Placements: placements}, nil
}

// queryCompetitionStandingRecords returns the round-robin rows in stage, group
// and entry order. The draw bounds them by the frozen field. The 27th group is
// keyed group_aa, so groups are ordered by key length before the key itself.
func queryCompetitionStandingRecords(ctx context.Context, tx pgx.Tx, competitionID string) ([]competitionStandingRecord, error) {
	rows, err := tx.Query(ctx, `SELECT stage.id::text,stage.name,stage.position,stage.status,standing.group_key,
		entry.status,standing.entry_id::text,entry.display_name,standing.played,standing.wins,standing.draws,
		standing.losses,standing.goals_for,standing.goals_against,standing.points,standing.walkovers
		FROM competition_stages stage
		JOIN competition_standings standing ON standing.stage_id=stage.id
		JOIN competition_entries entry ON entry.id=standing.entry_id
		WHERE stage.competition_id=$1 AND stage.format='round_robin'
		ORDER BY stage.position,length(standing.group_key),standing.group_key,standing.entry_id`, competitionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (competitionStandingRecord, error) {
		var record competitionStandingRecord
		err := row.Scan(&record.StageID, &record.StageName, &record.StagePosition, &record.StageStatus,
			&record.GroupKey, &record.EntryStatus, &record.Row.EntryID, &record.Row.DisplayName, &record.Row.Played,
			&record.Row.Wins, &record.Row.Draws, &record.Row.Losses, &record.Row.GoalsFor, &record.Row.GoalsAgainst,
			&record.Row.Points, &record.Row.Walkovers)
		return record, err
	})
}

// queryCompetitionPlacements returns the final placements in placement order.
// Progression writes them all at once, so the list is empty or complete.
func queryCompetitionPlacements(ctx context.Context, tx pgx.Tx, competitionID string) ([]competitionPlacement, error) {
	rows, err := tx.Query(ctx, `SELECT placement.placement,placement.entry_id::text,entry.display_name
		FROM competition_entry_placements placement
		JOIN competition_entries entry ON entry.id=placement.entry_id
		WHERE placement.competition_id=$1
		ORDER BY placement.placement,placement.entry_id`, competitionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (competitionPlacement, error) {
		var placement competitionPlacement
		err := row.Scan(&placement.Placement, &placement.EntryID, &placement.DisplayName)
		return placement, err
	})
}

// buildCompetitionStandingsStages groups records, already in stage and group
// order, into ranked group tables.
func buildCompetitionStandingsStages(records []competitionStandingRecord,
	competitionStatus string) ([]competitionStandingsStage, error) {
	stages := make([]competitionStandingsStage, 0)
	for start := 0; start < len(records); {
		first := records[start]
		end := start + 1
		for end < len(records) && records[end].StageID == first.StageID && records[end].GroupKey == first.GroupKey {
			end++
		}
		group, err := rankCompetitionStandingsGroup(first.GroupKey, records[start:end], competitionStatus)
		if err != nil {
			return nil, err
		}
		if len(stages) == 0 || stages[len(stages)-1].ID != first.StageID {
			stages = append(stages, competitionStandingsStage{ID: first.StageID, Name: first.StageName,
				Position: first.StagePosition, Status: first.StageStatus, Groups: []competitionStandingsGroup{}})
		}
		stage := &stages[len(stages)-1]
		stage.Groups = append(stage.Groups, group)
		start = end
	}
	return stages, nil
}

// rankCompetitionStandingsGroup orders one group table. Live rows come first,
// ranked by rankRoundRobinPlacements exactly as the stage's placements are;
// removed rows follow, unranked, in the same table order.
func rankCompetitionStandingsGroup(key string, records []competitionStandingRecord,
	competitionStatus string) (competitionStandingsGroup, error) {
	entries := make([]progressionPlacementEntry, len(records))
	standings := make([]progressionStandingForPlacement, len(records))
	removed := make([]progressionStandingForPlacement, 0)
	rows := make(map[string]competitionStandingRow, len(records))
	for index, record := range records {
		row := record.Row
		row.GoalDifference = row.GoalsFor - row.GoalsAgainst
		row.Removed = competitionStandingRemoved(competitionStatus, record.EntryStatus)
		rows[row.EntryID] = row
		entries[index] = progressionPlacementEntry{EntryID: row.EntryID, Live: !row.Removed}
		standings[index] = progressionStandingForPlacement{EntryID: row.EntryID, Points: row.Points,
			GoalsFor: row.GoalsFor, GoalsAgainst: row.GoalsAgainst}
		if row.Removed {
			removed = append(removed, standings[index])
		}
	}
	ranked, err := rankRoundRobinPlacements(entries, standings)
	if err != nil {
		return competitionStandingsGroup{}, err
	}
	sortRoundRobinStandings(removed)
	group := competitionStandingsGroup{Key: key, Rows: make([]competitionStandingRow, 0, len(records))}
	for _, placement := range ranked {
		row := rows[placement.EntryID]
		rank := placement.Placement
		row.Rank = &rank
		group.Rows = append(group.Rows, row)
	}
	for _, standing := range removed {
		group.Rows = append(group.Rows, rows[standing.EntryID])
	}
	return group, nil
}

// competitionStandingRemoved reports whether an entry is out of its table's
// ranking. It is the placements rule, progressionEntryIsLive, except on a
// cancelled competition: cancelling moves every paid entry to
// withdrawal_pending only to refund it, so there only a disqualification
// removes an entry.
func competitionStandingRemoved(competitionStatus, entryStatus string) bool {
	if competitionStatus == "cancelled" {
		return entryStatus == "disqualified"
	}
	return !progressionEntryIsLive(&entryStatus)
}
