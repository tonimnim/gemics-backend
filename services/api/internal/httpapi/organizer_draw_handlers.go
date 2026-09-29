package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/bracket"
	"github.com/jackc/pgx/v5"
)

const (
	drawAlgorithm        = "materialized_bracket_graph"
	drawAlgorithmVersion = 1
	maxPersistedMatches  = 10_000
	// minimumPlayWindowMinutes is the playing time a late but legitimate
	// check-in keeps before the result deadline. Missing that deadline removes
	// both entries (R7), so the window must outlast the check-in grace (D18).
	minimumPlayWindowMinutes = 15
)

type organizerDrawRequest struct {
	SeedingPolicy  string               `json:"seedingPolicy"`
	ExpectedStatus string               `json:"expectedStatus"`
	Config         *organizerDrawConfig `json:"config"`
}

// Pointer numbers distinguish an omitted value (use the documented default)
// from an explicit zero (reject it). The normalized form below contains no
// pointers, which gives request hashing and stored config one canonical shape.
type organizerDrawConfig struct {
	BestOf               *int `json:"bestOf"`
	ThirdPlace           bool `json:"thirdPlace"`
	GroupCount           *int `json:"groupCount"`
	DoubleRoundRobin     bool `json:"doubleRoundRobin"`
	CheckInLeadMinutes   *int `json:"checkInLeadMinutes"`
	CheckInGraceMinutes  *int `json:"checkInGraceMinutes"`
	ResultWindowMinutes  *int `json:"resultWindowMinutes"`
	RoundIntervalMinutes *int `json:"roundIntervalMinutes"`
}

type normalizedDrawRequest struct {
	SeedingPolicy  string                   `json:"seedingPolicy"`
	ExpectedStatus string                   `json:"expectedStatus"`
	Config         normalizedDrawConfigView `json:"config"`
}

type normalizedDrawConfigView struct {
	BestOf               int  `json:"bestOf"`
	ThirdPlace           bool `json:"thirdPlace"`
	GroupCount           int  `json:"groupCount"`
	DoubleRoundRobin     bool `json:"doubleRoundRobin"`
	CheckInLeadMinutes   int  `json:"checkInLeadMinutes"`
	CheckInGraceMinutes  int  `json:"checkInGraceMinutes"`
	ResultWindowMinutes  int  `json:"resultWindowMinutes"`
	RoundIntervalMinutes int  `json:"roundIntervalMinutes"`
}

type drawCandidate struct {
	EntryID       string
	CaptainUserID string
	Seed          *int
	Rating        *int
	MatchesPlayed int
	CreatedAt     time.Time
}

type drawPlanEntry struct {
	Position       int     `json:"position"`
	EntryID        string  `json:"entryId"`
	SeedKey        int     `json:"seedKey"`
	RatingSnapshot *int    `json:"ratingSnapshot"`
	GroupKey       *string `json:"groupKey"`
}

type drawMatchPlan struct {
	ID                       string
	Node                     bracket.Node
	HomeEntryID, AwayEntryID *string
	State                    string
	ScheduledAt              *time.Time
	CheckInOpensAt           *time.Time
	CheckInClosesAt          *time.Time
	ResultDueAt              *time.Time
	ActivationSourceMatchID  *string
}

type drawSlotPlan struct {
	MatchID         string
	MatchRank       int
	Slot            string
	SourceKind      string
	SourceEntryID   *string
	SourceMatchID   *string
	ResolvedEntryID *string
	ResolvedAt      *time.Time
}

type organizerDrawPlan struct {
	CompetitionID    string
	StageID          string
	Format           string
	GeneratedAt      time.Time
	DrawSeed         [32]byte
	EntryFingerprint [32]byte
	Graph            bracket.Graph
	Config           normalizedDrawConfigView
	Entries          []drawPlanEntry
	Matches          []drawMatchPlan
	Slots            []drawSlotPlan
}

type organizerDrawView struct {
	CompetitionID    string                   `json:"competitionId"`
	StageID          string                   `json:"stageId"`
	Algorithm        string                   `json:"algorithm"`
	AlgorithmVersion int                      `json:"algorithmVersion"`
	SeedingPolicy    string                   `json:"seedingPolicy"`
	DrawSeed         string                   `json:"drawSeed"`
	EntryCount       int                      `json:"entryCount"`
	MatchCount       int                      `json:"matchCount"`
	EntryFingerprint string                   `json:"entryFingerprint"`
	GraphFingerprint string                   `json:"graphFingerprint"`
	Config           normalizedDrawConfigView `json:"config"`
	Entries          []drawPlanEntry          `json:"entries"`
	GeneratedBy      string                   `json:"generatedBy"`
	GeneratedAt      time.Time                `json:"generatedAt"`
}

func (s *Server) createOrganizerDraw(w http.ResponseWriter, r *http.Request) {
	competitionID := strings.TrimSpace(r.PathValue("competitionId"))
	if !uuidPattern.MatchString(competitionID) {
		writeCompetitionNotFound(w)
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	var raw organizerDrawRequest
	if !decodeOrganizerDrawRequest(w, r, &raw) {
		return
	}
	input, problem := normalizeOrganizerDrawRequest(raw)
	if problem != nil {
		problem.write(w)
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The draw request is invalid.")
		return
	}

	membership := organizerFromContext(r.Context())
	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to generate the draw.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	// This is the same competition-wide gate progression uses. It is the first
	// transactional lock, so future draw/result operations cannot introduce a
	// lock-order cycle at the exact moment a tournament starts.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(
		hashtextextended('competition:' || $1::text, 91340287))`, competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to generate the draw.")
		return
	}
	scope := "organizer-draw:" + membership.OrganizationID + ":" + competitionID
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another draw request.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to generate the draw.")
		return
	}
	if replay != nil {
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to replay the draw response.")
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}

	var format, status, gameID string
	var startsAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT format,status,game_id,starts_at
		FROM competitions WHERE id=$1 AND organization_id=$2 FOR UPDATE`,
		competitionID, membership.OrganizationID).Scan(&format, &status, &gameID, &startsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCompetitionNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	if status != input.ExpectedStatus {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "competition_state_changed", "message": "The competition is no longer in the expected state.",
			"expectedStatus": input.ExpectedStatus, "currentStatus": status,
		})
		return
	}
	var drawExists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM competition_draws WHERE competition_id=$1)`,
		competitionID).Scan(&drawExists); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check the existing draw.")
		return
	}
	if drawExists {
		writeError(w, http.StatusConflict, "draw_already_generated", "This competition already has a persisted draw and cannot be regenerated.")
		return
	}

	candidates, err := loadFrozenDrawCandidates(r.Context(), tx, competitionID, gameID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to freeze the competition entries.")
		return
	}
	if len(candidates) < 2 {
		writeError(w, http.StatusConflict, "not_enough_entries", "A draw needs at least two active entries.")
		return
	}

	generatedAt := time.Now().UTC()
	plan, problem := buildOrganizerDrawPlan(competitionID, format, startsAt, generatedAt, input, candidates, nil)
	if problem != nil {
		problem.write(w)
		return
	}
	if err = persistOrganizerDraw(r.Context(), tx, plan, input.SeedingPolicy, actorID); err != nil {
		s.logger.Error("persist organizer draw", "competition_id", competitionID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw could not be persisted.")
		return
	}
	view := drawPlanView(plan, input.SeedingPolicy, actorID)
	body, err := json.Marshal(map[string]any{"data": view})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw response could not be created.")
		return
	}
	configJSON, err := json.Marshal(plan.Config)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw config could not be encoded.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, actorID, "competition.draw_generated", "competition_draw",
		competitionID, nil, map[string]any{
			"competitionId": competitionID, "stageId": plan.StageID, "algorithm": drawAlgorithm,
			"algorithmVersion": drawAlgorithmVersion, "seedingPolicy": input.SeedingPolicy,
			"drawSeed": view.DrawSeed, "config": plan.Config,
			"entryCount": len(plan.Entries), "matchCount": len(plan.Matches),
			"entryFingerprint": view.EntryFingerprint, "graphFingerprint": view.GraphFingerprint,
		}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw audit could not be persisted.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('competition',$1,'competition.draw_generated',jsonb_build_object(
		'competitionId',$1::text,'organizationId',$2::text,'stageId',$3::text,
		'entryCount',$4::integer,'matchCount',$5::integer,'seedingPolicy',$6::text,
		'drawSeed',$7::text,'entryFingerprint',$8::text,'graphFingerprint',$9::text,'config',$10::jsonb))`,
		competitionID, membership.OrganizationID, plan.StageID, len(plan.Entries), len(plan.Matches),
		input.SeedingPolicy, view.DrawSeed, view.EntryFingerprint, view.GraphFingerprint, json.RawMessage(configJSON)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw event could not be queued.")
		return
	}
	if err = finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusCreated, body); err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw response could not be persisted.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "draw_persistence_failed", "The draw could not be committed.")
		return
	}
	s.invalidateCompetitionCachesContext(r.Context(), competitionID)
	writeResultRawJSON(w, http.StatusCreated, body)
}

func decodeOrganizerDrawRequest(w http.ResponseWriter, r *http.Request, destination *organizerDrawRequest) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The draw request body is invalid.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "The draw request must contain exactly one JSON object.")
		return false
	}
	return true
}

func normalizeOrganizerDrawRequest(input organizerDrawRequest) (normalizedDrawRequest, *organizerFault) {
	policy := strings.TrimSpace(input.SeedingPolicy)
	switch policy {
	case "seeded", "rating", "random", "registration_order":
	default:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_seeding_policy",
			"Seeding policy must be seeded, rating, random or registration_order.")
	}
	if strings.TrimSpace(input.ExpectedStatus) != "check_in" {
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_expected_status",
			"expectedStatus must be check_in; draws are generated only after registration closes and before play starts.")
	}
	if input.Config == nil {
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "draw_config_required", "Provide the draw config object.")
	}
	config := normalizedDrawConfigView{
		BestOf: 1, GroupCount: 1, CheckInLeadMinutes: 15, CheckInGraceMinutes: 10,
		ResultWindowMinutes: 60, RoundIntervalMinutes: 90,
		ThirdPlace: input.Config.ThirdPlace, DoubleRoundRobin: input.Config.DoubleRoundRobin,
	}
	if input.Config.BestOf != nil {
		config.BestOf = *input.Config.BestOf
	}
	if input.Config.GroupCount != nil {
		config.GroupCount = *input.Config.GroupCount
	}
	if input.Config.CheckInLeadMinutes != nil {
		config.CheckInLeadMinutes = *input.Config.CheckInLeadMinutes
	}
	if input.Config.CheckInGraceMinutes != nil {
		config.CheckInGraceMinutes = *input.Config.CheckInGraceMinutes
	}
	if input.Config.ResultWindowMinutes != nil {
		config.ResultWindowMinutes = *input.Config.ResultWindowMinutes
	}
	if input.Config.RoundIntervalMinutes != nil {
		config.RoundIntervalMinutes = *input.Config.RoundIntervalMinutes
	}
	switch {
	case config.BestOf < 1 || config.BestOf > 9 || config.BestOf%2 == 0:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_best_of", "bestOf must be an odd number from 1 to 9.")
	case config.GroupCount < 1 || config.GroupCount > 64:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_group_count", "groupCount must be from 1 to 64.")
	case config.CheckInLeadMinutes < 5 || config.CheckInLeadMinutes > 180:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_check_in_lead", "checkInLeadMinutes must be from 5 to 180.")
	case config.CheckInGraceMinutes < 0 || config.CheckInGraceMinutes > 60:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_check_in_grace", "checkInGraceMinutes must be from 0 to 60.")
	case config.ResultWindowMinutes < 15 || config.ResultWindowMinutes > 1440:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_result_window", "resultWindowMinutes must be from 15 to 1440.")
	case config.RoundIntervalMinutes < 15 || config.RoundIntervalMinutes > 1440:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_round_interval", "roundIntervalMinutes must be from 15 to 1440.")
	case config.ResultWindowMinutes < config.CheckInGraceMinutes+minimumPlayWindowMinutes:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_result_window",
			"resultWindowMinutes must leave at least 15 minutes to play after the check-in grace period.")
	case config.RoundIntervalMinutes < config.ResultWindowMinutes:
		return normalizedDrawRequest{}, fault(http.StatusBadRequest, "invalid_round_interval", "roundIntervalMinutes must be at least the result window.")
	}
	return normalizedDrawRequest{SeedingPolicy: policy, ExpectedStatus: "check_in", Config: config}, nil
}

func loadFrozenDrawCandidates(ctx context.Context, tx pgx.Tx, competitionID, gameID string) ([]drawCandidate, error) {
	// The competition row is already locked. Row-share locks below keep a status
	// mutation from changing membership while the vector, fingerprints and graph
	// are copied. An explicit id order prevents PostgreSQL plan changes from
	// becoming draw-order changes. A player's rating row is created by their
	// first rated result, so a new player has none: the rating stays NULL (the
	// rating policy treats it as 1500) and matches played is zero.
	rows, err := tx.Query(ctx, `SELECT entry.id::text,entry.captain_user_id::text,entry.seed,
		rating.rating,COALESCE(rating.matches_played,0),entry.created_at
		FROM competition_entries entry
		LEFT JOIN player_game_ratings rating ON rating.user_id=entry.captain_user_id AND rating.game_id=$2
		WHERE entry.competition_id=$1 AND entry.status IN ('registered','checked_in','accepted')
		ORDER BY entry.id FOR SHARE OF entry`, competitionID, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]drawCandidate, 0)
	for rows.Next() {
		var item drawCandidate
		if err = rows.Scan(&item.EntryID, &item.CaptainUserID, &item.Seed, &item.Rating,
			&item.MatchesPlayed, &item.CreatedAt); err != nil {
			return nil, err
		}
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

func buildOrganizerDrawPlan(competitionID, format string, startsAt, generatedAt time.Time,
	input normalizedDrawRequest, candidates []drawCandidate, suppliedRandomSeed []byte) (organizerDrawPlan, *organizerFault) {
	config := input.Config
	switch bracket.Format(format) {
	case bracket.SingleElimination:
		if config.GroupCount != 1 || config.DoubleRoundRobin {
			return organizerDrawPlan{}, fault(http.StatusBadRequest, "format_config_mismatch",
				"groupCount and doubleRoundRobin are available only for round_robin.")
		}
	case bracket.DoubleElimination:
		if config.ThirdPlace || config.GroupCount != 1 || config.DoubleRoundRobin {
			return organizerDrawPlan{}, fault(http.StatusBadRequest, "format_config_mismatch",
				"Double elimination does not support thirdPlace, groups or doubleRoundRobin.")
		}
	case bracket.RoundRobin:
		if config.ThirdPlace {
			return organizerDrawPlan{}, fault(http.StatusBadRequest, "format_config_mismatch",
				"thirdPlace is available only for single_elimination.")
		}
		if config.GroupCount > len(candidates)/2 {
			return organizerDrawPlan{}, fault(http.StatusConflict, "group_count_exceeds_field",
				"Every round-robin group needs at least two active entries.")
		}
	default:
		return organizerDrawPlan{}, fault(http.StatusConflict, "unsupported_format", "The competition format cannot generate a draw.")
	}
	if estimatedDrawMatches(format, len(candidates), config) > maxPersistedMatches {
		return organizerDrawPlan{}, fault(http.StatusConflict, "draw_too_large",
			"This configuration creates too many matches. Split a round robin into more groups.")
	}

	var seed [32]byte
	if input.SeedingPolicy == "random" {
		if suppliedRandomSeed == nil {
			if _, err := rand.Read(seed[:]); err != nil {
				return organizerDrawPlan{}, fault(http.StatusServiceUnavailable, "randomness_unavailable", "Secure draw randomness is unavailable.")
			}
		} else if len(suppliedRandomSeed) != len(seed) {
			return organizerDrawPlan{}, fault(http.StatusBadRequest, "invalid_draw_seed", "A random draw seed must contain 32 bytes.")
		} else {
			copy(seed[:], suppliedRandomSeed)
		}
	}
	ordered := orderDrawCandidates(candidates, input.SeedingPolicy, seed[:])
	entries := materializeDrawEntries(ordered, input.SeedingPolicy, format, config.GroupCount)
	entryFingerprint := fingerprintDrawEntries(entries)
	if input.SeedingPolicy != "random" {
		seed = sha256.Sum256([]byte("gamics/draw/v1\x00" + competitionID + "\x00" + input.SeedingPolicy + "\x00" +
			hex.EncodeToString(entryFingerprint[:])))
	}

	drawEntries := make([]bracket.DrawEntry, len(entries))
	for index, entry := range entries {
		groupKey := ""
		if entry.GroupKey != nil {
			groupKey = *entry.GroupKey
		}
		drawEntries[index] = bracket.DrawEntry{EntryID: entry.EntryID, SeedKey: entry.SeedKey, GroupKey: groupKey}
	}
	graph, err := bracket.Emit(bracket.DrawInput{
		Format: bracket.Format(format), Entries: drawEntries,
		Config: bracket.Config{BestOf: config.BestOf, ThirdPlace: config.ThirdPlace,
			GroupCount: config.GroupCount, DoubleRoundRobin: config.DoubleRoundRobin},
	})
	if err != nil || bracket.Validate(graph) != nil {
		return organizerDrawPlan{}, fault(http.StatusConflict, "draw_generation_failed", "The active field cannot produce a valid bracket.")
	}
	if len(graph.Nodes) > maxPersistedMatches {
		return organizerDrawPlan{}, fault(http.StatusConflict, "draw_too_large",
			"This configuration creates too many matches. Split a round robin into more groups.")
	}
	stageID, err := randomUUID()
	if err != nil {
		return organizerDrawPlan{}, fault(http.StatusServiceUnavailable, "randomness_unavailable", "Secure identifiers are unavailable.")
	}
	plan := organizerDrawPlan{CompetitionID: competitionID, StageID: stageID, Format: format,
		GeneratedAt: generatedAt.UTC(), DrawSeed: seed, EntryFingerprint: entryFingerprint,
		Graph: graph, Config: config, Entries: entries}
	if err = materializeDrawGraph(&plan, startsAt.UTC(), randomUUID); err != nil {
		return organizerDrawPlan{}, fault(http.StatusServiceUnavailable, "randomness_unavailable", "Secure identifiers are unavailable.")
	}
	return plan, nil
}

// estimatedDrawMatches rejects an impractical round robin before the emitter
// allocates its quadratic fixture slice. Knockout formats remain linear and
// cannot approach the persistence ceiling at the platform's 1024-entry cap.
func estimatedDrawMatches(format string, entries int, config normalizedDrawConfigView) int {
	switch bracket.Format(format) {
	case bracket.SingleElimination:
		matches := max(0, entries-1)
		if config.ThirdPlace && entries >= 4 {
			matches++
		}
		return matches
	case bracket.DoubleElimination:
		return max(0, 2*entries-1)
	case bracket.RoundRobin:
		matches := 0
		for group := 0; group < config.GroupCount; group++ {
			members := entries / config.GroupCount
			if group < entries%config.GroupCount {
				members++
			}
			matches += members * (members - 1) / 2
		}
		if config.DoubleRoundRobin {
			matches *= 2
		}
		return matches
	default:
		return 0
	}
}

func orderDrawCandidates(source []drawCandidate, policy string, seed []byte) []drawCandidate {
	ordered := append([]drawCandidate(nil), source...)
	randomKeys := make(map[string][32]byte, len(ordered))
	if policy == "random" {
		for _, candidate := range ordered {
			mac := hmac.New(sha256.New, seed)
			_, _ = mac.Write([]byte(candidate.EntryID))
			var key [32]byte
			copy(key[:], mac.Sum(nil))
			randomKeys[candidate.EntryID] = key
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		switch policy {
		case "seeded":
			if (left.Seed == nil) != (right.Seed == nil) {
				return left.Seed != nil
			}
			if left.Seed != nil && right.Seed != nil && *left.Seed != *right.Seed {
				return *left.Seed < *right.Seed
			}
		case "rating":
			leftRating, rightRating := 1500, 1500
			if left.Rating != nil {
				leftRating = *left.Rating
			}
			if right.Rating != nil {
				rightRating = *right.Rating
			}
			if leftRating != rightRating {
				return leftRating > rightRating
			}
			if left.MatchesPlayed != right.MatchesPlayed {
				return left.MatchesPlayed > right.MatchesPlayed
			}
		case "random":
			leftKey, rightKey := randomKeys[left.EntryID], randomKeys[right.EntryID]
			if compared := bytes.Compare(leftKey[:], rightKey[:]); compared != 0 {
				return compared < 0
			}
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.EntryID < right.EntryID
	})
	return ordered
}

func materializeDrawEntries(ordered []drawCandidate, policy, format string, groupCount int) []drawPlanEntry {
	entries := make([]drawPlanEntry, len(ordered))
	for index, candidate := range ordered {
		seedKey := index + 1
		switch policy {
		case "seeded":
			if candidate.Seed != nil {
				seedKey = *candidate.Seed
			}
		case "rating":
			seedKey = 1500
			if candidate.Rating != nil {
				seedKey = *candidate.Rating
			}
		}
		var groupKey *string
		if format == string(bracket.RoundRobin) && groupCount > 1 {
			value := "group_" + drawGroupLabel(index%groupCount)
			groupKey = &value
		}
		entries[index] = drawPlanEntry{Position: index + 1, EntryID: candidate.EntryID,
			SeedKey: seedKey, RatingSnapshot: candidate.Rating, GroupKey: groupKey}
	}
	return entries
}

func fingerprintDrawEntries(entries []drawPlanEntry) [32]byte {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%d|%s|%d|", entry.Position, entry.EntryID, entry.SeedKey)
		if entry.RatingSnapshot != nil {
			_, _ = fmt.Fprintf(hash, "%d", *entry.RatingSnapshot)
		}
		_, _ = hash.Write([]byte("|"))
		if entry.GroupKey != nil {
			_, _ = hash.Write([]byte(*entry.GroupKey))
		}
		_, _ = hash.Write([]byte("\n"))
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func materializeDrawGraph(plan *organizerDrawPlan, advertisedStart time.Time,
	idFactory func() (string, error)) error {
	ids := make(map[bracket.LocalRef]string, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		id, err := idFactory()
		if err != nil {
			return err
		}
		ids[node.Ref] = id
	}
	lead := time.Duration(plan.Config.CheckInLeadMinutes) * time.Minute
	base := advertisedStart
	if earliest := plan.GeneratedAt.Add(lead); base.Before(earliest) {
		base = earliest
	}
	interval := time.Duration(plan.Config.RoundIntervalMinutes) * time.Minute
	resultWindow := time.Duration(plan.Config.ResultWindowMinutes) * time.Minute
	grace := time.Duration(plan.Config.CheckInGraceMinutes) * time.Minute
	plan.Matches = make([]drawMatchPlan, 0, len(plan.Graph.Nodes))
	plan.Slots = make([]drawSlotPlan, 0, len(plan.Graph.Nodes)*2)
	for _, node := range plan.Graph.Nodes {
		match := drawMatchPlan{ID: ids[node.Ref], Node: node, State: "pending"}
		if node.ActSrc != (bracket.LocalRef{}) {
			activationID := ids[node.ActSrc]
			match.ActivationSourceMatchID = &activationID
		}
		homeDirect := node.Home.Kind == bracket.SourceEntry
		awayDirect := node.Away.Kind == bracket.SourceEntry
		if homeDirect {
			value := node.Home.EntryID
			match.HomeEntryID = &value
		}
		if awayDirect {
			value := node.Away.EntryID
			match.AwayEntryID = &value
		}
		playable := homeDirect && awayDirect && node.Activation == bracket.ActivationUnconditional
		if plan.Format == string(bracket.RoundRobin) {
			// Round one opens immediately. Later fixtures are fully scheduled but
			// gated pending, ready for the round-release worker.
			when := base.Add(time.Duration(node.Ref.Round-1) * interval)
			assignMatchWindow(&match, when, lead, grace, resultWindow)
			if playable && node.Ref.Round == 1 {
				match.State = "ready"
			}
		} else if playable {
			// Dependency matches get fresh windows when progression makes them
			// ready. Anchoring them now would leave a late-running final with an
			// already-expired check-in window.
			assignMatchWindow(&match, base, lead, grace, resultWindow)
			match.State = "ready"
		}
		plan.Matches = append(plan.Matches, match)
		plan.Slots = append(plan.Slots,
			materializeDrawSlot(match.ID, "home", node.Rank, node.Home, ids, plan.GeneratedAt),
			materializeDrawSlot(match.ID, "away", node.Rank, node.Away, ids, plan.GeneratedAt))
	}
	return nil
}

func assignMatchWindow(match *drawMatchPlan, scheduled time.Time, lead, grace, resultWindow time.Duration) {
	scheduled = scheduled.UTC()
	opens, closes, due := scheduled.Add(-lead), scheduled.Add(grace), scheduled.Add(resultWindow)
	match.ScheduledAt, match.CheckInOpensAt, match.CheckInClosesAt, match.ResultDueAt =
		&scheduled, &opens, &closes, &due
}

func materializeDrawSlot(matchID, side string, rank int, slot bracket.Slot,
	ids map[bracket.LocalRef]string, generatedAt time.Time) drawSlotPlan {
	result := drawSlotPlan{MatchID: matchID, MatchRank: rank, Slot: side, SourceKind: slot.Kind.String()}
	switch slot.Kind {
	case bracket.SourceEntry:
		entryID, resolvedAt := slot.EntryID, generatedAt
		result.SourceEntryID, result.ResolvedEntryID, result.ResolvedAt = &entryID, &entryID, &resolvedAt
	case bracket.SourceWinnerOf, bracket.SourceLoserOf:
		sourceID := ids[slot.Src]
		result.SourceMatchID = &sourceID
	}
	return result
}

func sourceRanks(graph bracket.Graph) map[bracket.LocalRef]int {
	ranks := make(map[bracket.LocalRef]int, len(graph.Nodes))
	for _, node := range graph.Nodes {
		ranks[node.Ref] = node.Rank
	}
	return ranks
}

func persistOrganizerDraw(ctx context.Context, tx pgx.Tx, plan organizerDrawPlan, seedingPolicy, actorID string) error {
	configJSON, err := json.Marshal(plan.Config)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO competition_stages
		(id,competition_id,position,name,format,best_of,config,status,created_at,updated_at)
		VALUES ($1,$2,0,'Main stage',$3,$4,$5,'active',$6,$6)`,
		plan.StageID, plan.CompetitionID, plan.Format, plan.Config.BestOf, configJSON, plan.GeneratedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO competition_draws
		(competition_id,stage_id,algorithm,algorithm_version,draw_seed,seeding_policy,entry_count,
		 entry_fingerprint,graph_fingerprint,match_count,config,generated_by,generated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		plan.CompetitionID, plan.StageID, drawAlgorithm, drawAlgorithmVersion, plan.DrawSeed[:], seedingPolicy,
		len(plan.Entries), plan.EntryFingerprint[:], plan.Graph.Fingerprint[:], len(plan.Matches), configJSON,
		actorID, plan.GeneratedAt); err != nil {
		return err
	}
	entryRows := make([][]any, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		entryRows = append(entryRows, []any{plan.CompetitionID, entry.Position, entry.EntryID, entry.GroupKey,
			entry.SeedKey, entry.RatingSnapshot, nil})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"competition_draw_entries"},
		[]string{"competition_id", "draw_position", "entry_id", "group_key", "seed_key", "rating_snapshot", "deviation_snapshot"},
		pgx.CopyFromRows(entryRows)); err != nil {
		return err
	}
	matchRows := make([][]any, 0, len(plan.Matches))
	for _, match := range plan.Matches {
		matchRows = append(matchRows, []any{match.ID, plan.CompetitionID, plan.StageID, match.Node.Ref.Bracket,
			match.Node.Ref.Round, match.Node.Ref.Number, match.HomeEntryID, match.AwayEntryID, match.State,
			match.ScheduledAt, match.CheckInOpensAt, match.CheckInClosesAt, match.ResultDueAt, 1,
			match.Node.Rank, nullableString(match.Node.GroupKey), match.Node.Activation,
			match.ActivationSourceMatchID, plan.GeneratedAt, plan.GeneratedAt})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"matches"},
		[]string{"id", "competition_id", "stage_id", "bracket", "round_number", "match_number",
			"home_entry_id", "away_entry_id", "state", "scheduled_at", "check_in_opens_at", "check_in_closes_at",
			"result_due_at", "version", "graph_rank", "group_key", "activation_rule", "activation_source_match_id",
			"created_at", "updated_at"}, pgx.CopyFromRows(matchRows)); err != nil {
		return err
	}
	ranks := sourceRanks(plan.Graph)
	refByMatchID := make(map[string]bracket.LocalRef, len(plan.Matches))
	for _, match := range plan.Matches {
		refByMatchID[match.ID] = match.Node.Ref
	}
	slotRows := make([][]any, 0, len(plan.Slots))
	for _, slot := range plan.Slots {
		var sourceRank *int
		if slot.SourceMatchID != nil {
			rank := ranks[refByMatchID[*slot.SourceMatchID]]
			sourceRank = &rank
		}
		slotRows = append(slotRows, []any{slot.MatchID, plan.CompetitionID, slot.Slot, slot.MatchRank,
			slot.SourceKind, slot.SourceEntryID, slot.SourceMatchID, sourceRank, slot.ResolvedEntryID,
			slot.ResolvedAt, nil, plan.GeneratedAt})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"match_slots"},
		[]string{"match_id", "competition_id", "slot", "match_rank", "source_kind", "source_entry_id",
			"source_match_id", "source_rank", "resolved_entry_id", "resolved_at", "voided_at", "created_at"},
		pgx.CopyFromRows(slotRows)); err != nil {
		return err
	}
	if plan.Format == string(bracket.RoundRobin) {
		standingRows := make([][]any, 0, len(plan.Entries))
		for _, entry := range plan.Entries {
			groupKey := "main"
			if entry.GroupKey != nil {
				groupKey = *entry.GroupKey
			}
			standingRows = append(standingRows, []any{plan.StageID, plan.CompetitionID, entry.EntryID,
				groupKey, plan.GeneratedAt})
		}
		if _, err = tx.CopyFrom(ctx, pgx.Identifier{"competition_standings"},
			[]string{"stage_id", "competition_id", "entry_id", "group_key", "updated_at"},
			pgx.CopyFromRows(standingRows)); err != nil {
			return err
		}
	}
	progressRows := make([][]any, 0, len(plan.Slots)+len(plan.Matches))
	for _, slot := range plan.Slots {
		if slot.ResolvedEntryID == nil {
			continue
		}
		detail := json.RawMessage(`{"sourceKind":"entry"}`)
		progressRows = append(progressRows, []any{plan.CompetitionID, plan.StageID, nil, slot.MatchID,
			slot.Slot, slot.ResolvedEntryID, "slot_filled", "draw", actorID,
			detail, plan.GeneratedAt})
	}
	for _, match := range plan.Matches {
		if match.State != "ready" {
			continue
		}
		detail, marshalErr := json.Marshal(map[string]any{"bracket": match.Node.Ref.Bracket,
			"round": match.Node.Ref.Round, "matchNumber": match.Node.Ref.Number})
		if marshalErr != nil {
			return marshalErr
		}
		progressRows = append(progressRows, []any{plan.CompetitionID, plan.StageID, nil, match.ID, nil, nil,
			"match_readied", "draw", actorID,
			json.RawMessage(detail), plan.GeneratedAt})
	}
	if len(progressRows) > 0 {
		if _, err = tx.CopyFrom(ctx, pgx.Identifier{"progression_events"},
			[]string{"competition_id", "stage_id", "source_match_id", "target_match_id", "target_slot", "entry_id",
				"kind", "cause", "actor_user_id", "detail", "occurred_at"}, pgx.CopyFromRows(progressRows)); err != nil {
			return err
		}
	}
	return nil
}

func drawPlanView(plan organizerDrawPlan, seedingPolicy, actorID string) organizerDrawView {
	return organizerDrawView{CompetitionID: plan.CompetitionID, StageID: plan.StageID, Algorithm: drawAlgorithm,
		AlgorithmVersion: drawAlgorithmVersion, SeedingPolicy: seedingPolicy,
		DrawSeed: hex.EncodeToString(plan.DrawSeed[:]), EntryCount: len(plan.Entries), MatchCount: len(plan.Matches),
		EntryFingerprint: hex.EncodeToString(plan.EntryFingerprint[:]),
		GraphFingerprint: hex.EncodeToString(plan.Graph.Fingerprint[:]), Config: plan.Config,
		Entries: plan.Entries, GeneratedBy: actorID, GeneratedAt: plan.GeneratedAt}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func drawGroupLabel(group int) string {
	var label []byte
	for {
		label = append([]byte{byte('a' + group%26)}, label...)
		group = group/26 - 1
		if group < 0 {
			return string(label)
		}
	}
}
