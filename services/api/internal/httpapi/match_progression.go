package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errMatchProgressionConflict = errors.New("match progression outcome conflicts with the applied outcome")
	errMatchProgressionInvalid  = errors.New("match progression input is invalid")
)

// Progression causes. The ledger CHECKs still list one retired cause for
// history only; validateMatchProgressionInput accepts none but these.
const (
	progressionCausePlayerConfirmation = "player_confirmation"
	progressionCauseTimeoutForfeit     = "timeout_forfeit"
	progressionCauseWithdrawal         = "withdrawal"
	progressionCauseDisqualification   = "disqualification"
	progressionCauseAdminCorrection    = "admin_correction"
	progressionCausePlatformReview     = "platform_review"
)

// matchProgressionInput describes the material outcome, not the request which
// produced it. Actor and cause are audit metadata and deliberately are not part
// of the outcome hash: retrying the same finalized version through a recovery
// worker is a replay, not a conflicting sporting result.
type matchProgressionInput struct {
	MatchID          string
	CompetitionID    string
	FinalizedVersion int
	FinalState       string
	WinnerEntryID    *string
	HomeScore        *int
	AwayScore        *int
	Cause            string
	ActorUserID      *string
}

type matchProgressionSlotChange struct {
	MatchID string  `json:"matchId"`
	Slot    string  `json:"slot"`
	EntryID *string `json:"entryId"`
	Voided  bool    `json:"voided"`
}

type matchProgressionReleasedRound struct {
	StageID string `json:"stageId"`
	Round   int    `json:"round"`
}

type matchProgressionResult struct {
	Replayed                bool                            `json:"replayed"`
	SlotChanges             []matchProgressionSlotChange    `json:"slotChanges"`
	ReadiedMatchIDs         []string                        `json:"readiedMatchIds"`
	ForfeitMatchIDs         []string                        `json:"forfeitMatchIds"`
	CancelledMatchIDs       []string                        `json:"cancelledMatchIds"`
	StandingsMatchIDs       []string                        `json:"standingsMatchIds"`
	ReleasedRounds          []matchProgressionReleasedRound `json:"releasedRounds"`
	CompletedStageIDs       []string                        `json:"completedStageIds"`
	CompletedCompetitionIDs []string                        `json:"completedCompetitionIds"`
	PlacementsWritten       bool                            `json:"placementsWritten"`
}

type progressionOutcome struct {
	MatchID       string  `json:"matchId"`
	MatchVersion  int     `json:"matchVersion"`
	State         string  `json:"state"`
	WinnerEntryID *string `json:"winnerEntryId"`
	HomeScore     *int    `json:"homeScore"`
	AwayScore     *int    `json:"awayScore"`
}

func (outcome progressionOutcome) canonical() ([]byte, [32]byte, error) {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return raw, sha256.Sum256(raw), nil
}

type progressionSource struct {
	ID               string
	CompetitionID    string
	StageID          string
	StageFormat      string
	StageConfig      progressionScheduleConfig
	GroupKey         string
	RoundNumber      int
	State            string
	Version          int
	HomeEntryID      *string
	AwayEntryID      *string
	WinnerEntryID    *string
	CompletionReason *string
	CompletedAt      time.Time
	HomeScore        *int
	AwayScore        *int
	Cause            string
	ActorUserID      *string
}

func (source progressionSource) outcome() progressionOutcome {
	return progressionOutcome{
		MatchID: source.ID, MatchVersion: source.Version, State: source.State,
		WinnerEntryID: source.WinnerEntryID, HomeScore: source.HomeScore, AwayScore: source.AwayScore,
	}
}

type progressionScheduleConfig struct {
	CheckInLeadMinutes  int `json:"checkInLeadMinutes"`
	CheckInGraceMinutes int `json:"checkInGraceMinutes"`
	ResultWindowMinutes int `json:"resultWindowMinutes"`
}

func decodeProgressionSchedule(raw []byte) (progressionScheduleConfig, error) {
	config := progressionScheduleConfig{CheckInLeadMinutes: 15, CheckInGraceMinutes: 10, ResultWindowMinutes: 60}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return progressionScheduleConfig{}, err
		}
	}
	if config.CheckInLeadMinutes < 1 || config.CheckInGraceMinutes < 0 || config.ResultWindowMinutes < 1 {
		return progressionScheduleConfig{}, fmt.Errorf("%w: invalid persisted match window", errMatchProgressionInvalid)
	}
	return config, nil
}

// lockCompetitionProgressionGate must be called before a progression-capable
// path locks a match, its result verification, reports or review, or an entry
// or the competition row of a drawn competition. applyMatchProgression takes it
// again (PostgreSQL transaction advisory locks are re-entrant), but that second
// acquisition is a guard, not a substitute for the caller's lock ordering.
func lockCompetitionProgressionGate(ctx context.Context, tx pgx.Tx, competitionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended('competition:' || $1::text, 91340287))`, competitionID)
	return err
}

// applyMatchProgression is the only post-draw writer of match participants. It
// follows persisted match_slots edges; it never infers a target from round
// arithmetic. All graph, standings, stage, audit-ledger and outbox writes are in
// the caller's transaction.
func applyMatchProgression(ctx context.Context, tx pgx.Tx, input matchProgressionInput) (matchProgressionResult, error) {
	if err := validateMatchProgressionInput(input); err != nil {
		return matchProgressionResult{}, err
	}
	if err := lockCompetitionProgressionGate(ctx, tx, input.CompetitionID); err != nil {
		return matchProgressionResult{}, err
	}

	requested := progressionOutcome{
		MatchID: input.MatchID, MatchVersion: input.FinalizedVersion, State: input.FinalState,
		WinnerEntryID: input.WinnerEntryID, HomeScore: input.HomeScore, AwayScore: input.AwayScore,
	}
	_, requestedHash, err := requested.canonical()
	if err != nil {
		return matchProgressionResult{}, err
	}
	if replay, found, replayErr := loadProgressionReplay(ctx, tx, input.MatchID, input.FinalizedVersion); replayErr != nil {
		return matchProgressionResult{}, replayErr
	} else if found {
		if !bytes.Equal(replay.OutcomeHash, requestedHash[:]) {
			return matchProgressionResult{}, errMatchProgressionConflict
		}
		replay.Result.Replayed = true
		return replay.Result, nil
	}

	source, err := lockProgressionSource(ctx, tx, input.MatchID)
	if err != nil {
		return matchProgressionResult{}, err
	}
	if source.CompetitionID != input.CompetitionID || source.Version != input.FinalizedVersion ||
		source.State != input.FinalState || !sameOptionalString(source.WinnerEntryID, input.WinnerEntryID) {
		return matchProgressionResult{}, errMatchProgressionConflict
	}
	source.HomeScore, source.AwayScore = cloneOptionalInt(input.HomeScore), cloneOptionalInt(input.AwayScore)
	source.Cause, source.ActorUserID = input.Cause, cloneOptionalString(input.ActorUserID)
	if err = validateProgressionSourceOutcome(source); err != nil {
		return matchProgressionResult{}, err
	}

	type applicationKey struct {
		MatchID string
		Version int
		Hash    [32]byte
	}
	claimed := make([]applicationKey, 0, 4)
	queue := []progressionSource{source}
	result := matchProgressionResult{
		SlotChanges: []matchProgressionSlotChange{}, ReadiedMatchIDs: []string{},
		ForfeitMatchIDs: []string{}, CancelledMatchIDs: []string{},
		StandingsMatchIDs: []string{}, ReleasedRounds: []matchProgressionReleasedRound{},
		CompletedStageIDs: []string{}, CompletedCompetitionIDs: []string{},
	}
	stages := map[string]progressionStageLock{}
	standings := make([]progressionStandingsWork, 0, 1)
	roundSources := make([]progressionSource, 0, 1)

	// Round release runs to a fixpoint. Fixtures it settles at release are
	// sources in their own right: they are claimed, update standings and can
	// complete their round, which releases the next one in this transaction.
	// Every pass consumes at least one round, so the loop terminates.
	for {
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			outcomeRaw, outcomeHash, hashErr := current.outcome().canonical()
			if hashErr != nil {
				return matchProgressionResult{}, hashErr
			}
			claimedNow, claimErr := claimProgressionApplication(ctx, tx, current, outcomeRaw, outcomeHash)
			if claimErr != nil {
				return matchProgressionResult{}, claimErr
			}
			if !claimedNow {
				continue
			}
			claimed = append(claimed, applicationKey{MatchID: current.ID, Version: current.Version, Hash: outcomeHash})
			stages[current.StageID] = progressionStageLock{ID: current.StageID, CompetitionID: current.CompetitionID}
			if current.StageFormat == "round_robin" {
				roundSources = append(roundSources, current)
				if current.State != "cancelled" {
					standings = append(standings, progressionStandingsWork{Source: current})
				}
			}

			children, childErr := applyProgressionEdges(ctx, tx, current, &result, stages)
			if childErr != nil {
				return matchProgressionResult{}, childErr
			}
			queue = append(queue, children...)
			if outboxErr := insertProgressionOutbox(ctx, tx, "match", current.ID, "match.progression_applied", map[string]any{
				"matchId": current.ID, "competitionId": current.CompetitionID,
				"stageId": current.StageID, "matchVersion": current.Version,
				"state": current.State, "winnerEntryId": current.WinnerEntryID,
			}); outboxErr != nil {
				return matchProgressionResult{}, outboxErr
			}
		}
		settled, releaseErr := releaseProgressionRounds(ctx, tx, roundSources, &result)
		if releaseErr != nil {
			return matchProgressionResult{}, releaseErr
		}
		roundSources = roundSources[:0]
		if len(settled) == 0 {
			break
		}
		queue = append(queue, settled...)
	}

	stageLocks := sortedProgressionStages(stages)
	stageStatuses := make(map[string]string, len(stageLocks))
	for _, stage := range stageLocks {
		var status string
		if err = tx.QueryRow(ctx, `SELECT status FROM competition_stages
			WHERE id=$1 AND competition_id=$2 FOR UPDATE`, stage.ID, stage.CompetitionID).Scan(&status); err != nil {
			return matchProgressionResult{}, err
		}
		stageStatuses[stage.ID] = status
	}

	sort.Slice(standings, func(i, j int) bool {
		if standings[i].Source.StageID != standings[j].Source.StageID {
			return standings[i].Source.StageID < standings[j].Source.StageID
		}
		return standings[i].Source.ID < standings[j].Source.ID
	})
	for _, work := range standings {
		if err = applyProgressionStandings(ctx, tx, work.Source); err != nil {
			return matchProgressionResult{}, err
		}
		result.StandingsMatchIDs = append(result.StandingsMatchIDs, work.Source.ID)
	}

	for _, stage := range stageLocks {
		if stageStatuses[stage.ID] == "completed" {
			continue
		}
		var complete bool
		if err = tx.QueryRow(ctx, `SELECT NOT EXISTS (
			SELECT 1 FROM matches WHERE stage_id=$1
			AND state NOT IN ('completed','forfeit','cancelled'))`, stage.ID).Scan(&complete); err != nil {
			return matchProgressionResult{}, err
		}
		if !complete {
			continue
		}
		placementCount, placementErr := writeProgressionPlacements(ctx, tx, stage, source, input)
		if placementErr != nil {
			return matchProgressionResult{}, placementErr
		}
		result.PlacementsWritten = result.PlacementsWritten || placementCount > 0
		command, updateErr := tx.Exec(ctx, `UPDATE competition_stages
			SET status='completed',updated_at=now() WHERE id=$1 AND competition_id=$2 AND status<>'completed'`,
			stage.ID, stage.CompetitionID)
		if updateErr != nil {
			return matchProgressionResult{}, updateErr
		}
		if command.RowsAffected() != 1 {
			continue
		}
		result.CompletedStageIDs = append(result.CompletedStageIDs, stage.ID)
		if err = insertProgressionEvent(ctx, tx, progressionEventInput{
			CompetitionID: stage.CompetitionID, StageID: stage.ID, SourceMatchID: &input.MatchID,
			Kind: "stage_completed", Cause: input.Cause, ActorUserID: input.ActorUserID,
			Detail: map[string]any{"stageId": stage.ID}, OccurredAt: source.CompletedAt,
		}); err != nil {
			return matchProgressionResult{}, err
		}
		if err = insertProgressionOutbox(ctx, tx, "competition_stage", stage.ID, "stage.completed", map[string]any{
			"competitionId": stage.CompetitionID, "stageId": stage.ID,
		}); err != nil {
			return matchProgressionResult{}, err
		}
	}
	if err = completeProgressionCompetitions(ctx, tx, stageLocks, source, input, &result); err != nil {
		return matchProgressionResult{}, err
	}

	sortMatchProgressionResult(&result)
	resultRaw, err := json.Marshal(result)
	if err != nil {
		return matchProgressionResult{}, err
	}
	for _, application := range claimed {
		command, updateErr := tx.Exec(ctx, `UPDATE match_progression_applications
			SET application_result=$1 WHERE source_match_id=$2 AND finalized_match_version=$3 AND outcome_hash=$4`,
			json.RawMessage(resultRaw), application.MatchID, application.Version, application.Hash[:])
		if updateErr != nil {
			return matchProgressionResult{}, updateErr
		}
		if command.RowsAffected() != 1 {
			return matchProgressionResult{}, errMatchProgressionConflict
		}
	}
	return result, nil
}

func validateMatchProgressionInput(input matchProgressionInput) error {
	if !uuidPattern.MatchString(input.MatchID) || !uuidPattern.MatchString(input.CompetitionID) || input.FinalizedVersion < 1 {
		return errMatchProgressionInvalid
	}
	if input.ActorUserID != nil && !uuidPattern.MatchString(*input.ActorUserID) {
		return errMatchProgressionInvalid
	}
	if input.FinalState != "completed" && input.FinalState != "forfeit" && input.FinalState != "cancelled" {
		return errMatchProgressionInvalid
	}
	if input.FinalState == "cancelled" && input.WinnerEntryID != nil {
		return errMatchProgressionInvalid
	}
	if input.FinalState == "cancelled" && input.HomeScore != nil {
		return errMatchProgressionInvalid
	}
	if (input.HomeScore == nil) != (input.AwayScore == nil) {
		return errMatchProgressionInvalid
	}
	if input.HomeScore != nil && (*input.HomeScore < 0 || *input.AwayScore < 0) {
		return errMatchProgressionInvalid
	}
	switch input.Cause {
	case progressionCausePlayerConfirmation, progressionCauseTimeoutForfeit, progressionCauseWithdrawal,
		progressionCauseDisqualification, progressionCauseAdminCorrection, progressionCausePlatformReview:
		return nil
	default:
		return errMatchProgressionInvalid
	}
}

type progressionReplay struct {
	OutcomeHash []byte
	Result      matchProgressionResult
}

func loadProgressionReplay(ctx context.Context, tx pgx.Tx, matchID string, version int) (progressionReplay, bool, error) {
	var replay progressionReplay
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT outcome_hash,application_result
		FROM match_progression_applications WHERE source_match_id=$1 AND finalized_match_version=$2`,
		matchID, version).Scan(&replay.OutcomeHash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return progressionReplay{}, false, nil
	}
	if err != nil {
		return progressionReplay{}, false, err
	}
	if err = json.Unmarshal(raw, &replay.Result); err != nil {
		return progressionReplay{}, false, err
	}
	return replay, true, nil
}

func lockProgressionSource(ctx context.Context, tx pgx.Tx, matchID string) (progressionSource, error) {
	var source progressionSource
	var configRaw []byte
	var completedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT match.id::text,match.competition_id::text,match.stage_id::text,
		stage.format,stage.config,COALESCE(match.group_key,'main'),match.round_number,match.state,match.version,
		match.home_entry_id::text,match.away_entry_id::text,match.winner_entry_id::text,
		match.completion_reason,match.completed_at
		FROM matches match JOIN competition_stages stage ON stage.id=match.stage_id
		WHERE match.id=$1 FOR UPDATE OF match`, matchID).Scan(
		&source.ID, &source.CompetitionID, &source.StageID, &source.StageFormat, &configRaw,
		&source.GroupKey, &source.RoundNumber, &source.State, &source.Version, &source.HomeEntryID, &source.AwayEntryID,
		&source.WinnerEntryID, &source.CompletionReason, &completedAt)
	if err != nil {
		return progressionSource{}, err
	}
	if completedAt == nil || !isProgressionTerminal(source.State) {
		return progressionSource{}, errMatchProgressionInvalid
	}
	source.CompletedAt = completedAt.UTC()
	source.StageConfig, err = decodeProgressionSchedule(configRaw)
	return source, err
}

func validateProgressionSourceOutcome(source progressionSource) error {
	if !isProgressionTerminal(source.State) || source.Version < 1 || source.CompletedAt.IsZero() {
		return errMatchProgressionInvalid
	}
	if source.WinnerEntryID != nil &&
		!sameOptionalString(source.WinnerEntryID, source.HomeEntryID) && !sameOptionalString(source.WinnerEntryID, source.AwayEntryID) {
		return errMatchProgressionConflict
	}
	if source.State == "cancelled" && source.WinnerEntryID != nil {
		return errMatchProgressionConflict
	}
	if source.StageFormat == "round_robin" && source.State == "completed" {
		if source.HomeEntryID == nil || source.AwayEntryID == nil || source.HomeScore == nil || source.AwayScore == nil {
			return fmt.Errorf("%w: a completed round-robin match requires both participants and scores", errMatchProgressionInvalid)
		}
		homeWon, awayWon := *source.HomeScore > *source.AwayScore, *source.AwayScore > *source.HomeScore
		switch {
		case homeWon && !sameOptionalString(source.WinnerEntryID, source.HomeEntryID):
			return errMatchProgressionConflict
		case awayWon && !sameOptionalString(source.WinnerEntryID, source.AwayEntryID):
			return errMatchProgressionConflict
		case !homeWon && !awayWon && source.WinnerEntryID != nil:
			return errMatchProgressionConflict
		}
	}
	return nil
}

func claimProgressionApplication(ctx context.Context, tx pgx.Tx, source progressionSource, outcomeRaw []byte, outcomeHash [32]byte) (bool, error) {
	command, err := tx.Exec(ctx, `INSERT INTO match_progression_applications
		(source_match_id,finalized_match_version,competition_id,stage_id,outcome_hash,outcome,cause,actor_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (source_match_id,finalized_match_version) DO NOTHING`,
		source.ID, source.Version, source.CompetitionID, source.StageID, outcomeHash[:], json.RawMessage(outcomeRaw),
		source.Cause, source.ActorUserID)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 1 {
		return true, nil
	}
	var stored []byte
	if err = tx.QueryRow(ctx, `SELECT outcome_hash FROM match_progression_applications
		WHERE source_match_id=$1 AND finalized_match_version=$2`, source.ID, source.Version).Scan(&stored); err != nil {
		return false, err
	}
	if !bytes.Equal(stored, outcomeHash[:]) {
		return false, errMatchProgressionConflict
	}
	return false, nil
}

func isProgressionTerminal(state string) bool {
	return state == "completed" || state == "forfeit" || state == "cancelled"
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sortMatchProgressionResult(result *matchProgressionResult) {
	sort.Slice(result.SlotChanges, func(i, j int) bool {
		if result.SlotChanges[i].MatchID != result.SlotChanges[j].MatchID {
			return result.SlotChanges[i].MatchID < result.SlotChanges[j].MatchID
		}
		return result.SlotChanges[i].Slot < result.SlotChanges[j].Slot
	})
	sort.Strings(result.ReadiedMatchIDs)
	sort.Strings(result.ForfeitMatchIDs)
	sort.Strings(result.CancelledMatchIDs)
	sort.Strings(result.StandingsMatchIDs)
	sort.Slice(result.ReleasedRounds, func(i, j int) bool {
		if result.ReleasedRounds[i].StageID != result.ReleasedRounds[j].StageID {
			return result.ReleasedRounds[i].StageID < result.ReleasedRounds[j].StageID
		}
		return result.ReleasedRounds[i].Round < result.ReleasedRounds[j].Round
	})
	sort.Strings(result.CompletedStageIDs)
	sort.Strings(result.CompletedCompetitionIDs)
}

// completeProgressionCompetitions closes the tournament only after every
// persisted stage is terminal. The competition-wide advisory gate is already
// held, so a draw/state transition cannot race this decision.
func completeProgressionCompetitions(ctx context.Context, tx pgx.Tx, stages []progressionStageLock,
	trigger progressionSource, input matchProgressionInput, result *matchProgressionResult) error {
	competitionIDs := make([]string, 0, len(stages))
	seen := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		if _, exists := seen[stage.CompetitionID]; exists {
			continue
		}
		seen[stage.CompetitionID] = struct{}{}
		competitionIDs = append(competitionIDs, stage.CompetitionID)
	}
	sort.Strings(competitionIDs)
	for _, competitionID := range competitionIDs {
		var organizationID, status string
		if err := tx.QueryRow(ctx, `SELECT organization_id::text,status FROM competitions
			WHERE id=$1 FOR UPDATE`, competitionID).Scan(&organizationID, &status); err != nil {
			return err
		}
		if status == "completed" {
			continue
		}
		if status != "running" {
			continue
		}
		var allStagesComplete bool
		if err := tx.QueryRow(ctx, `SELECT count(*)>0 AND bool_and(status='completed')
			FROM competition_stages WHERE competition_id=$1`, competitionID).Scan(&allStagesComplete); err != nil {
			return err
		}
		if !allStagesComplete {
			continue
		}
		command, err := tx.Exec(ctx, `UPDATE competitions SET status='completed',updated_at=now()
			WHERE id=$1 AND status='running'`, competitionID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errMatchProgressionConflict
		}
		result.CompletedCompetitionIDs = append(result.CompletedCompetitionIDs, competitionID)
		after, err := json.Marshal(map[string]any{
			"competitionId": competitionID, "status": "completed", "completedAt": trigger.CompletedAt,
			"sourceMatchId": input.MatchID,
		})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events
			(organization_id,actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1,$2,'competition.completed','competition',$3,'match-progression',$4)`,
			organizationID, input.ActorUserID, competitionID, json.RawMessage(after)); err != nil {
			return err
		}
		if err = insertProgressionOutbox(ctx, tx, "competition", competitionID, "competition.completed", map[string]any{
			"competitionId": competitionID, "completedAt": trigger.CompletedAt,
			"sourceMatchId": input.MatchID,
		}); err != nil {
			return err
		}
	}
	return nil
}
