package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type progressionStageLock struct {
	ID            string
	CompetitionID string
}

func sortedProgressionStages(stages map[string]progressionStageLock) []progressionStageLock {
	result := make([]progressionStageLock, 0, len(stages))
	for _, stage := range stages {
		result = append(result, stage)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

type progressionTarget struct {
	ID                      string
	CompetitionID           string
	StageID                 string
	StageFormat             string
	StageConfig             progressionScheduleConfig
	GroupKey                string
	RoundNumber             int
	State                   string
	Version                 int
	GraphRank               int
	HomeEntryID             *string
	AwayEntryID             *string
	ActivationRule          string
	ActivationSourceMatchID *string
}

type progressionSlot struct {
	Side            string
	SourceKind      string
	SourceMatchID   *string
	ResolvedEntryID *string
	ResolvedAt      *time.Time
	VoidedAt        *time.Time
	EntryStatus     *string
}

func (slot progressionSlot) terminal() bool {
	return slot.ResolvedEntryID != nil || slot.VoidedAt != nil
}

type progressionActivation uint8

const (
	progressionActivationPending progressionActivation = iota
	progressionActivationSatisfied
	progressionActivationFailed
)

type childSettlementPlan struct {
	State            string
	WinnerEntryID    *string
	CompletionReason *string
	Terminal         bool
}

// planChildSettlement is a pure function. In particular, a void-first arrival
// and a fill-first arrival produce the same state once both slots are terminal.
func planChildSettlement(activation progressionActivation, home, away progressionSlot) (childSettlementPlan, error) {
	if home.ResolvedEntryID != nil && home.VoidedAt != nil || away.ResolvedEntryID != nil && away.VoidedAt != nil {
		return childSettlementPlan{}, errMatchProgressionConflict
	}
	if activation == progressionActivationFailed {
		reason := "reset_not_required"
		return childSettlementPlan{State: "cancelled", CompletionReason: &reason, Terminal: true}, nil
	}
	if !home.terminal() || !away.terminal() {
		return childSettlementPlan{State: "pending"}, nil
	}
	if home.ResolvedEntryID != nil && away.ResolvedEntryID != nil {
		if *home.ResolvedEntryID == *away.ResolvedEntryID {
			return childSettlementPlan{}, errMatchProgressionConflict
		}
		if activation != progressionActivationSatisfied {
			return childSettlementPlan{State: "pending"}, nil
		}
		return childSettlementPlan{State: "ready"}, nil
	}
	if home.ResolvedEntryID != nil || away.ResolvedEntryID != nil {
		winner := home.ResolvedEntryID
		if winner == nil {
			winner = away.ResolvedEntryID
		}
		reason := "walkover"
		return childSettlementPlan{State: "forfeit", WinnerEntryID: cloneOptionalString(winner), CompletionReason: &reason, Terminal: true}, nil
	}
	reason := "double_no_show"
	return childSettlementPlan{State: "cancelled", CompletionReason: &reason, Terminal: true}, nil
}

func applyProgressionEdges(ctx context.Context, tx pgx.Tx, source progressionSource,
	result *matchProgressionResult, stages map[string]progressionStageLock) ([]progressionSource, error) {
	targetIDs, err := loadProgressionTargetIDs(ctx, tx, source)
	if err != nil {
		return nil, err
	}
	terminalChildren := make([]progressionSource, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		target, lockErr := lockProgressionTarget(ctx, tx, targetID, source.CompetitionID)
		if lockErr != nil {
			return nil, lockErr
		}
		stages[target.StageID] = progressionStageLock{ID: target.StageID, CompetitionID: target.CompetitionID}
		if target.State != "pending" {
			return nil, fmt.Errorf("%w: target match %s is already %s", errMatchProgressionConflict, target.ID, target.State)
		}
		slots, slotErr := lockProgressionSlots(ctx, tx, target.ID)
		if slotErr != nil {
			return nil, slotErr
		}
		if len(slots) != 2 || slots[0].Side != "home" || slots[1].Side != "away" {
			return nil, fmt.Errorf("%w: target match %s does not have home and away slots", errMatchProgressionInvalid, target.ID)
		}

		changed := false
		for index := range slots {
			if slots[index].SourceMatchID == nil || *slots[index].SourceMatchID != source.ID {
				continue
			}
			var entryID *string
			switch slots[index].SourceKind {
			case "winner_of":
				entryID = source.WinnerEntryID
			case "loser_of":
				entryID = losingEntry(source.HomeEntryID, source.AwayEntryID, source.WinnerEntryID)
			default:
				return nil, fmt.Errorf("%w: dependent slot has source kind %s", errMatchProgressionInvalid, slots[index].SourceKind)
			}
			slotChanged, updateErr := resolveProgressionSlot(ctx, tx, target, source, &slots[index], entryID, result)
			if updateErr != nil {
				return nil, updateErr
			}
			changed = changed || slotChanged
		}

		activation, activationErr := progressionTargetActivation(ctx, tx, target, source)
		if activationErr != nil {
			return nil, activationErr
		}
		if activation == progressionActivationFailed {
			for index := range slots {
				if slots[index].terminal() {
					continue
				}
				slotChanged, voidErr := resolveProgressionSlot(ctx, tx, target, source, &slots[index], nil, result)
				if voidErr != nil {
					return nil, voidErr
				}
				changed = changed || slotChanged
			}
		}
		for index := range slots {
			if slots[index].ResolvedEntryID == nil || progressionEntryIsLive(slots[index].EntryStatus) {
				continue
			}
			if err = voidResolvedProgressionSlot(ctx, tx, target, source, &slots[index], result); err != nil {
				return nil, err
			}
			changed = true
		}

		plan, planErr := planChildSettlement(activation, slots[0], slots[1])
		if planErr != nil {
			return nil, planErr
		}
		stateChanged := plan.State != target.State
		mirrorChanged := !sameOptionalString(target.HomeEntryID, slots[0].ResolvedEntryID) ||
			!sameOptionalString(target.AwayEntryID, slots[1].ResolvedEntryID)
		if !changed && !stateChanged && !mirrorChanged {
			continue
		}
		updatedVersion, updateErr := updateProgressionTarget(ctx, tx, target, source.CompletedAt, slots, plan)
		if updateErr != nil {
			return nil, updateErr
		}
		if plan.State != "pending" {
			if err = writeProgressionTargetTransition(ctx, tx, source, target, updatedVersion, plan, result); err != nil {
				return nil, err
			}
		}
		if plan.Terminal {
			terminalChildren = append(terminalChildren, progressionSource{
				ID: target.ID, CompetitionID: target.CompetitionID, StageID: target.StageID,
				StageFormat: target.StageFormat, StageConfig: target.StageConfig, GroupKey: target.GroupKey,
				RoundNumber: target.RoundNumber,
				State:       plan.State, Version: updatedVersion,
				HomeEntryID:      cloneOptionalString(slots[0].ResolvedEntryID),
				AwayEntryID:      cloneOptionalString(slots[1].ResolvedEntryID),
				WinnerEntryID:    cloneOptionalString(plan.WinnerEntryID),
				CompletionReason: cloneOptionalString(plan.CompletionReason), CompletedAt: source.CompletedAt,
				Cause: source.Cause, ActorUserID: cloneOptionalString(source.ActorUserID),
			})
		}
	}
	return terminalChildren, nil
}

func loadProgressionTargetIDs(ctx context.Context, tx pgx.Tx, source progressionSource) ([]string, error) {
	// GROUP BY rather than DISTINCT: PostgreSQL only lets a DISTINCT query order
	// by selected expressions, and the lock order needs the uuid, not its text.
	rows, err := tx.Query(ctx, `SELECT target.id::text,target.graph_rank
		FROM match_slots edge JOIN matches target ON target.id=edge.match_id
		WHERE edge.source_match_id=$1 AND target.competition_id=$2
		GROUP BY target.id,target.graph_rank
		ORDER BY target.graph_rank,target.id`, source.ID, source.CompetitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, 2)
	for rows.Next() {
		var id string
		var rank int
		if err = rows.Scan(&id, &rank); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func lockProgressionTarget(ctx context.Context, tx pgx.Tx, targetID, competitionID string) (progressionTarget, error) {
	var target progressionTarget
	var configRaw []byte
	err := tx.QueryRow(ctx, `SELECT match.id::text,match.competition_id::text,match.stage_id::text,
		stage.format,stage.config,COALESCE(match.group_key,'main'),match.round_number,match.state,match.version,match.graph_rank,
		match.home_entry_id::text,match.away_entry_id::text,match.activation_rule,
		match.activation_source_match_id::text
		FROM matches match JOIN competition_stages stage ON stage.id=match.stage_id
		WHERE match.id=$1 AND match.competition_id=$2 FOR UPDATE OF match`, targetID, competitionID).Scan(
		&target.ID, &target.CompetitionID, &target.StageID, &target.StageFormat, &configRaw,
		&target.GroupKey, &target.RoundNumber, &target.State, &target.Version, &target.GraphRank,
		&target.HomeEntryID, &target.AwayEntryID, &target.ActivationRule, &target.ActivationSourceMatchID)
	if err != nil {
		return progressionTarget{}, err
	}
	target.StageConfig, err = decodeProgressionSchedule(configRaw)
	return target, err
}

func lockProgressionSlots(ctx context.Context, tx pgx.Tx, targetID string) ([]progressionSlot, error) {
	rows, err := tx.Query(ctx, `SELECT slot.slot,slot.source_kind,slot.source_match_id::text,
		slot.resolved_entry_id::text,slot.resolved_at,slot.voided_at,entry.status
		FROM match_slots slot
		LEFT JOIN competition_entries entry ON entry.id=slot.resolved_entry_id
		WHERE slot.match_id=$1
		ORDER BY CASE slot.slot WHEN 'home' THEN 0 ELSE 1 END
		FOR UPDATE OF slot`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]progressionSlot, 0, 2)
	for rows.Next() {
		var slot progressionSlot
		if err = rows.Scan(&slot.Side, &slot.SourceKind, &slot.SourceMatchID, &slot.ResolvedEntryID,
			&slot.ResolvedAt, &slot.VoidedAt, &slot.EntryStatus); err != nil {
			return nil, err
		}
		result = append(result, slot)
	}
	return result, rows.Err()
}

func resolveProgressionSlot(ctx context.Context, tx pgx.Tx, target progressionTarget, source progressionSource,
	slot *progressionSlot, entryID *string, result *matchProgressionResult) (bool, error) {
	if slot.terminal() {
		if entryID != nil && slot.ResolvedEntryID != nil && *entryID == *slot.ResolvedEntryID || entryID == nil && slot.VoidedAt != nil {
			return false, nil
		}
		return false, errMatchProgressionConflict
	}
	var commandErr error
	if entryID != nil {
		command, err := tx.Exec(ctx, `UPDATE match_slots SET resolved_entry_id=$3,resolved_at=$4,voided_at=NULL
			WHERE match_id=$1 AND slot=$2 AND resolved_at IS NULL AND voided_at IS NULL`,
			target.ID, slot.Side, *entryID, source.CompletedAt)
		commandErr = err
		if err == nil && command.RowsAffected() != 1 {
			commandErr = errMatchProgressionConflict
		}
		if commandErr == nil {
			slot.ResolvedEntryID, slot.ResolvedAt = cloneOptionalString(entryID), progressionTimePointer(source.CompletedAt)
			slot.EntryStatus, commandErr = progressionEntryStatus(ctx, tx, *entryID)
		}
	} else {
		command, err := tx.Exec(ctx, `UPDATE match_slots SET resolved_entry_id=NULL,resolved_at=NULL,voided_at=$3
			WHERE match_id=$1 AND slot=$2 AND resolved_at IS NULL AND voided_at IS NULL`,
			target.ID, slot.Side, source.CompletedAt)
		commandErr = err
		if err == nil && command.RowsAffected() != 1 {
			commandErr = errMatchProgressionConflict
		}
		if commandErr == nil {
			slot.VoidedAt = progressionTimePointer(source.CompletedAt)
		}
	}
	if commandErr != nil {
		return false, commandErr
	}
	change := matchProgressionSlotChange{MatchID: target.ID, Slot: slot.Side,
		EntryID: cloneOptionalString(entryID), Voided: entryID == nil}
	result.SlotChanges = append(result.SlotChanges, change)
	kind := "slot_filled"
	if entryID == nil {
		kind = "slot_voided"
	}
	if err := insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: source.CompetitionID, StageID: target.StageID,
		SourceMatchID: &source.ID, TargetMatchID: &target.ID, TargetSlot: &slot.Side,
		EntryID: entryID, Kind: kind, Cause: source.Cause, ActorUserID: source.ActorUserID,
		Detail:     map[string]any{"sourceKind": slot.SourceKind, "sourceMatchVersion": source.Version},
		OccurredAt: source.CompletedAt,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func voidResolvedProgressionSlot(ctx context.Context, tx pgx.Tx, target progressionTarget, source progressionSource,
	slot *progressionSlot, result *matchProgressionResult) error {
	entryID := cloneOptionalString(slot.ResolvedEntryID)
	command, err := tx.Exec(ctx, `UPDATE match_slots SET resolved_entry_id=NULL,resolved_at=NULL,voided_at=$3
		WHERE match_id=$1 AND slot=$2 AND resolved_entry_id=$4 AND voided_at IS NULL`,
		target.ID, slot.Side, source.CompletedAt, *entryID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errMatchProgressionConflict
	}
	slot.ResolvedEntryID, slot.ResolvedAt, slot.EntryStatus = nil, nil, nil
	slot.VoidedAt = progressionTimePointer(source.CompletedAt)
	result.SlotChanges = append(result.SlotChanges, matchProgressionSlotChange{
		MatchID: target.ID, Slot: slot.Side, Voided: true,
	})
	return insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: source.CompetitionID, StageID: target.StageID,
		SourceMatchID: &source.ID, TargetMatchID: &target.ID, TargetSlot: &slot.Side,
		Kind: "slot_voided", Cause: source.Cause, ActorUserID: source.ActorUserID,
		Detail: map[string]any{"entryId": entryID, "reason": "entry_not_live"}, OccurredAt: source.CompletedAt,
	})
}

func progressionEntryStatus(ctx context.Context, tx pgx.Tx, entryID string) (*string, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM competition_entries WHERE id=$1`, entryID).Scan(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

func progressionEntryIsLive(status *string) bool {
	return status == nil || *status != "withdrawal_pending" && *status != "withdrawn" && *status != "disqualified"
}

func progressionTargetActivation(ctx context.Context, tx pgx.Tx, target progressionTarget,
	current progressionSource) (progressionActivation, error) {
	if target.ActivationRule == "unconditional" {
		return progressionActivationSatisfied, nil
	}
	if target.ActivationRule != "if_source_away_wins" || target.ActivationSourceMatchID == nil {
		return progressionActivationPending, errMatchProgressionInvalid
	}
	state, winner, away := "", (*string)(nil), (*string)(nil)
	if *target.ActivationSourceMatchID == current.ID {
		state, winner, away = current.State, current.WinnerEntryID, current.AwayEntryID
	} else {
		err := tx.QueryRow(ctx, `SELECT state,winner_entry_id::text,away_entry_id::text
			FROM matches WHERE id=$1 AND competition_id=$2`,
			*target.ActivationSourceMatchID, target.CompetitionID).Scan(&state, &winner, &away)
		if err != nil {
			return progressionActivationPending, err
		}
	}
	if !isProgressionTerminal(state) {
		return progressionActivationPending, nil
	}
	return conditionalProgressionActivation(state, winner, away), nil
}

func conditionalProgressionActivation(state string, winner, away *string) progressionActivation {
	if !isProgressionTerminal(state) {
		return progressionActivationPending
	}
	if state != "cancelled" && winner != nil && away != nil && *winner == *away {
		return progressionActivationSatisfied
	}
	return progressionActivationFailed
}

func updateProgressionTarget(ctx context.Context, tx pgx.Tx, target progressionTarget, at time.Time,
	slots []progressionSlot, plan childSettlementPlan) (int, error) {
	var scheduledAt, checkInOpensAt, checkInClosesAt, resultDueAt *time.Time
	if plan.State == "ready" {
		scheduled := at.Add(time.Duration(target.StageConfig.CheckInLeadMinutes) * time.Minute)
		opens := at
		closes := scheduled.Add(time.Duration(target.StageConfig.CheckInGraceMinutes) * time.Minute)
		due := scheduled.Add(time.Duration(target.StageConfig.ResultWindowMinutes) * time.Minute)
		scheduledAt, checkInOpensAt, checkInClosesAt, resultDueAt = &scheduled, &opens, &closes, &due
	}
	var completedAt *time.Time
	if plan.Terminal {
		completedAt = progressionTimePointer(at)
	}
	var version int
	err := tx.QueryRow(ctx, `UPDATE matches SET
		home_entry_id=$1,away_entry_id=$2,state=$3,winner_entry_id=$4,
		completed_at=CASE WHEN $5::timestamptz IS NULL THEN completed_at ELSE $5 END,
		completion_reason=$6,
		scheduled_at=CASE WHEN $7::timestamptz IS NULL THEN scheduled_at ELSE $7 END,
		check_in_opens_at=CASE WHEN $8::timestamptz IS NULL THEN check_in_opens_at ELSE $8 END,
		check_in_closes_at=CASE WHEN $9::timestamptz IS NULL THEN check_in_closes_at ELSE $9 END,
		result_due_at=CASE WHEN $10::timestamptz IS NULL THEN result_due_at ELSE $10 END,
		version=version+1,updated_at=now()
		WHERE id=$11 AND competition_id=$12 AND version=$13 AND state='pending'
		RETURNING version`, slots[0].ResolvedEntryID, slots[1].ResolvedEntryID, plan.State,
		plan.WinnerEntryID, completedAt, plan.CompletionReason, scheduledAt, checkInOpensAt,
		checkInClosesAt, resultDueAt, target.ID, target.CompetitionID, target.Version).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errMatchProgressionConflict
	}
	return version, err
}

func writeProgressionTargetTransition(ctx context.Context, tx pgx.Tx, source progressionSource,
	target progressionTarget, targetVersion int, plan childSettlementPlan, result *matchProgressionResult) error {
	kind, eventType := "", ""
	switch plan.State {
	case "ready":
		kind, eventType = "match_readied", "match.ready"
		result.ReadiedMatchIDs = append(result.ReadiedMatchIDs, target.ID)
	case "forfeit":
		kind, eventType = "match_forfeited", "match.forfeited"
		result.ForfeitMatchIDs = append(result.ForfeitMatchIDs, target.ID)
	case "cancelled":
		kind, eventType = "match_cancelled", "match.cancelled"
		result.CancelledMatchIDs = append(result.CancelledMatchIDs, target.ID)
	default:
		return nil
	}
	if err := insertProgressionEvent(ctx, tx, progressionEventInput{
		CompetitionID: source.CompetitionID, StageID: target.StageID,
		SourceMatchID: &source.ID, TargetMatchID: &target.ID, Kind: kind, Cause: source.Cause,
		ActorUserID: source.ActorUserID, Detail: map[string]any{
			"targetMatchVersion": targetVersion, "winnerEntryId": plan.WinnerEntryID,
			"completionReason": plan.CompletionReason,
		}, OccurredAt: source.CompletedAt,
	}); err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "match", target.ID, eventType, map[string]any{
		"matchId": target.ID, "competitionId": target.CompetitionID, "stageId": target.StageID,
		"matchVersion": targetVersion, "winnerEntryId": plan.WinnerEntryID,
		"completionReason": plan.CompletionReason,
	})
}

func losingEntry(home, away, winner *string) *string {
	if winner == nil {
		return nil
	}
	if home != nil && *winner == *home {
		return cloneOptionalString(away)
	}
	if away != nil && *winner == *away {
		return cloneOptionalString(home)
	}
	return nil
}

func progressionTimePointer(value time.Time) *time.Time {
	cloned := value
	return &cloned
}

type progressionEventInput struct {
	CompetitionID string
	StageID       string
	SourceMatchID *string
	TargetMatchID *string
	TargetSlot    *string
	EntryID       *string
	Kind          string
	Cause         string
	ActorUserID   *string
	Detail        map[string]any
	OccurredAt    time.Time
}

func insertProgressionEvent(ctx context.Context, tx pgx.Tx, input progressionEventInput) error {
	detail, err := json.Marshal(input.Detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO progression_events
		(competition_id,stage_id,source_match_id,target_match_id,target_slot,entry_id,
		 kind,cause,actor_user_id,detail,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, input.CompetitionID, input.StageID,
		input.SourceMatchID, input.TargetMatchID, input.TargetSlot, input.EntryID,
		input.Kind, input.Cause, input.ActorUserID, json.RawMessage(detail), input.OccurredAt)
	return err
}

func insertProgressionOutbox(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string,
	payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ($1,$2,$3,$4)`, aggregateType, aggregateID, eventType, json.RawMessage(raw))
	return err
}
