package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/competition"
)

// This file holds the competition input rules as pure functions. They take a
// clock rather than reading one, so every boundary can be tested exactly.

// normalizeCompetitionInput validates a create request and fills the derived
// fields. Anything the database would reject with a constraint violation is
// rejected here first, with an error a person can act on.
func normalizeCompetitionInput(input organizerCompetitionInput, now time.Time) (competitionDraft, *organizerFault) {
	draft := competitionDraft{
		Name:                 strings.TrimSpace(input.Name),
		Slug:                 strings.TrimSpace(strings.ToLower(input.Slug)),
		Description:          strings.TrimSpace(input.Description),
		GameID:               strings.TrimSpace(input.GameID),
		Format:               strings.TrimSpace(input.Format),
		MaxEntries:           input.MaxEntries,
		EntryFeeMinor:        input.EntryFeeMinor,
		Currency:             strings.ToUpper(strings.TrimSpace(input.Currency)),
		PrizeAmountMinor:     input.PrizeAmountMinor,
		PrizeFunding:         strings.TrimSpace(input.PrizeFunding),
		RegistrationOpensAt:  input.RegistrationOpensAt.UTC(),
		RegistrationClosesAt: input.RegistrationClosesAt.UTC(),
		StartsAt:             input.StartsAt.UTC(),
	}
	if input.CheckInOpensAt != nil {
		checkIn := input.CheckInOpensAt.UTC()
		draft.CheckInOpensAt = &checkIn
	}
	if draft.Slug == "" {
		draft.Slug = slugify(draft.Name)
	}
	if draft.Currency == "" {
		draft.Currency = "KES"
	}
	if draft.PrizeFunding == "" {
		draft.PrizeFunding = "none"
	}
	rules, problem := normalizeRules(input.Rules)
	if problem != nil {
		return competitionDraft{}, problem
	}
	draft.Rules = rules
	if problem := validateCompetitionDraft(&draft, now, nil); problem != nil {
		return competitionDraft{}, problem
	}
	return draft, nil
}

// applyCompetitionPatch merges a partial update onto the stored competition and
// enforces what the current status allows to change. It returns the merged
// draft and whether the rules snapshot changed, so the caller knows when to bump
// the rules version.
func applyCompetitionPatch(current competitionDraft, patch organizerCompetitionPatch, status string,
	entryCount int, now time.Time) (competitionDraft, bool, *organizerFault) {
	editable, live := competitionEditability(status)
	if !editable {
		return competitionDraft{}, false, fault(http.StatusConflict, "competition_locked",
			"A competition in "+status+" can no longer be edited.")
	}

	updated := current
	changed := false
	// In a live competition, players have already accepted the terms of entry.
	// Money, format and identity are frozen; only the fields that can be widened
	// without invalidating an existing registration stay open.
	locked := func(field string) *organizerFault {
		return fault(http.StatusConflict, "field_locked",
			"The field "+field+" cannot change once registration has opened.")
	}
	if patch.Slug != nil {
		if live {
			return competitionDraft{}, false, locked("slug")
		}
		updated.Slug = strings.TrimSpace(strings.ToLower(*patch.Slug))
		changed = true
	}
	if patch.GameID != nil {
		if live {
			return competitionDraft{}, false, locked("gameId")
		}
		updated.GameID = strings.TrimSpace(*patch.GameID)
		changed = true
	}
	if patch.Format != nil {
		if live {
			return competitionDraft{}, false, locked("format")
		}
		updated.Format = strings.TrimSpace(*patch.Format)
		changed = true
	}
	if patch.EntryFeeMinor != nil {
		if live {
			return competitionDraft{}, false, locked("entryFeeMinor")
		}
		updated.EntryFeeMinor = *patch.EntryFeeMinor
		changed = true
	}
	if patch.Currency != nil {
		if live {
			return competitionDraft{}, false, locked("currency")
		}
		updated.Currency = strings.ToUpper(strings.TrimSpace(*patch.Currency))
		changed = true
	}
	if patch.PrizeAmountMinor != nil {
		if live {
			return competitionDraft{}, false, locked("prizeAmountMinor")
		}
		updated.PrizeAmountMinor = *patch.PrizeAmountMinor
		changed = true
	}
	if patch.PrizeFunding != nil {
		if live {
			return competitionDraft{}, false, locked("prizeFunding")
		}
		updated.PrizeFunding = strings.TrimSpace(*patch.PrizeFunding)
		changed = true
	}
	if patch.RegistrationOpensAt != nil {
		if live {
			return competitionDraft{}, false, locked("registrationOpensAt")
		}
		updated.RegistrationOpensAt = patch.RegistrationOpensAt.UTC()
		changed = true
	}
	if patch.Name != nil {
		updated.Name = strings.TrimSpace(*patch.Name)
		changed = true
	}
	if patch.Description != nil {
		updated.Description = strings.TrimSpace(*patch.Description)
		changed = true
	}
	if patch.MaxEntries != nil {
		if live && *patch.MaxEntries < current.MaxEntries {
			return competitionDraft{}, false, fault(http.StatusConflict, "capacity_shrink_forbidden",
				"Capacity cannot be reduced once registration has opened.")
		}
		if *patch.MaxEntries < entryCount {
			return competitionDraft{}, false, fault(http.StatusConflict, "capacity_below_entries",
				"Capacity cannot be lower than the number of active entries.")
		}
		updated.MaxEntries = *patch.MaxEntries
		changed = true
	}
	if patch.RegistrationClosesAt != nil {
		next := patch.RegistrationClosesAt.UTC()
		if live && next.Before(current.RegistrationClosesAt) {
			return competitionDraft{}, false, fault(http.StatusConflict, "registration_shorten_forbidden",
				"Registration can be extended but not shortened once it has opened.")
		}
		updated.RegistrationClosesAt = next
		changed = true
	}
	if patch.CheckInOpensAt != nil {
		checkIn := patch.CheckInOpensAt.UTC()
		updated.CheckInOpensAt = &checkIn
		changed = true
	}
	if patch.StartsAt != nil {
		updated.StartsAt = patch.StartsAt.UTC()
		changed = true
	}
	rulesChanged := false
	if patch.Rules != nil {
		rules, problem := normalizeRules(*patch.Rules)
		if problem != nil {
			return competitionDraft{}, false, problem
		}
		rulesChanged = !bytes.Equal(rules, current.Rules)
		updated.Rules = rules
		changed = changed || rulesChanged
	}
	if !changed {
		return competitionDraft{}, false, fault(http.StatusBadRequest, "empty_patch",
			"Provide at least one field to update.")
	}
	if updated.Slug == "" {
		updated.Slug = slugify(updated.Name)
	}
	// Passing the stored competition tells the validator which timestamps this
	// request is actually introducing. A start time that has already passed is
	// history, and history must not block an unrelated edit; a start time the
	// organizer is moving must still land in the future.
	if problem := validateCompetitionDraft(&updated, now, &current); problem != nil {
		return competitionDraft{}, false, problem
	}
	return updated, rulesChanged, nil
}

// competitionEditability maps a status to what may change. The second return
// says whether entries already exist for the competition in this status, which
// is what freezes money and format.
func competitionEditability(status string) (editable bool, live bool) {
	switch competition.Status(status) {
	case competition.StatusDraft, competition.StatusPublished:
		return true, false
	case competition.StatusRegistration:
		return true, true
	default:
		return false, false
	}
}

// validateCompetitionDraft enforces every invariant the competitions table also
// checks, plus the product rules SQL cannot express.
//
// previous is the stored competition for an edit, or nil for a creation. It is
// what separates "this timestamp is in the past" from "you are moving this
// timestamp into the past": only the second is an error.
func validateCompetitionDraft(draft *competitionDraft, now time.Time, previous *competitionDraft) *organizerFault {
	if len(draft.Name) < 3 || len(draft.Name) > maxCompetitionNameLength {
		return fault(http.StatusBadRequest, "invalid_name", "The competition name must be 3 to 120 characters.")
	}
	if !validOrganizationSlug(draft.Slug) {
		return fault(http.StatusBadRequest, "invalid_slug",
			"The slug must be 3 to 48 characters of lowercase letters, numbers and single hyphens.")
	}
	if len(draft.Description) > maxCompetitionDescription {
		return fault(http.StatusBadRequest, "invalid_description", "The description is too long.")
	}
	if draft.GameID == "" || len(draft.GameID) > 64 {
		return fault(http.StatusBadRequest, "invalid_game", "Choose a game for this competition.")
	}
	if !slices.Contains(competitionFormats, competition.Format(draft.Format)) {
		return fault(http.StatusBadRequest, "invalid_format",
			"Format must be single_elimination, double_elimination or round_robin.")
	}
	if draft.MaxEntries < 2 || draft.MaxEntries > maxCompetitionEntries {
		return fault(http.StatusBadRequest, "invalid_capacity",
			"Capacity must be between 2 and 1024 entries.")
	}
	if draft.EntryFeeMinor < 0 || draft.EntryFeeMinor > maxCompetitionFeeMinor {
		return fault(http.StatusBadRequest, "invalid_entry_fee",
			"The entry fee must be between 0 and 5000000 minor units.")
	}
	// fee_purpose is derived rather than accepted: the schema allows exactly one
	// pairing, and asking a client to restate it only creates a way to get it
	// wrong.
	draft.FeePurpose = "none"
	if draft.EntryFeeMinor > 0 {
		draft.FeePurpose = "administration"
	}
	if len(draft.Currency) != 3 || strings.ToUpper(draft.Currency) != draft.Currency {
		return fault(http.StatusBadRequest, "invalid_currency", "Use a three-letter ISO currency code.")
	}
	if draft.EntryFeeMinor > 0 && draft.Currency != "KES" {
		// M-Pesa is the only collection rail wired up, so a fee in any other
		// currency would be uncollectable.
		return fault(http.StatusBadRequest, "unsupported_currency",
			"Paid entry is only supported in KES today.")
	}
	if draft.EntryFeeMinor%100 != 0 {
		// M-Pesa collects whole shillings, so a fee with cents could never be paid.
		return fault(http.StatusBadRequest, "invalid_entry_fee",
			"The entry fee must be a whole number of shillings.")
	}
	if draft.PrizeAmountMinor < 0 || draft.PrizeAmountMinor > maxCompetitionPrizeMinor {
		return fault(http.StatusBadRequest, "invalid_prize", "The prize amount is outside the allowed range.")
	}
	switch draft.PrizeFunding {
	case "none":
		if draft.PrizeAmountMinor > 0 {
			// Entry fees are an administration fee, never a prize pool. Naming a
			// funder is what keeps that boundary auditable.
			return fault(http.StatusBadRequest, "prize_funding_required",
				"State whether the organizer or a sponsor funds this prize.")
		}
	case "organizer", "sponsor":
		if draft.PrizeAmountMinor <= 0 {
			return fault(http.StatusBadRequest, "invalid_prize",
				"A funded prize needs an amount above zero.")
		}
	default:
		return fault(http.StatusBadRequest, "invalid_prize_funding",
			"Prize funding must be none, organizer or sponsor.")
	}
	if !draft.RegistrationOpensAt.Before(draft.RegistrationClosesAt) {
		return fault(http.StatusBadRequest, "invalid_registration_window",
			"Registration must close after it opens.")
	}
	if draft.StartsAt.Before(draft.RegistrationClosesAt) {
		return fault(http.StatusBadRequest, "invalid_start_time",
			"The competition cannot start before registration closes.")
	}
	if introducesTimestamp(draft.StartsAt, previous, func(p *competitionDraft) time.Time { return p.StartsAt }) &&
		!draft.StartsAt.After(now) {
		return fault(http.StatusBadRequest, "start_time_in_past", "The start time must be in the future.")
	}
	if introducesTimestamp(draft.RegistrationClosesAt, previous,
		func(p *competitionDraft) time.Time { return p.RegistrationClosesAt }) &&
		!draft.RegistrationClosesAt.After(now) {
		return fault(http.StatusBadRequest, "registration_window_in_past",
			"Registration must close in the future.")
	}
	if draft.CheckInOpensAt != nil {
		if draft.CheckInOpensAt.Before(draft.RegistrationClosesAt) || draft.CheckInOpensAt.After(draft.StartsAt) {
			return fault(http.StatusBadRequest, "invalid_check_in_window",
				"Check-in must open between registration closing and the start time.")
		}
	}
	// The domain model is the final word on schedule shape. Reaching it means
	// the specific checks above missed something, so this is a guard, not the
	// primary validation.
	model := competition.Competition{
		Name: draft.Name, OrganizationID: "validated", GameID: draft.GameID, MaxEntries: draft.MaxEntries,
		RegistrationOpen: draft.RegistrationOpensAt, RegistrationEnd: draft.RegistrationClosesAt,
		StartsAt: draft.StartsAt,
	}
	if err := model.ValidateSchedule(); err != nil {
		return fault(http.StatusBadRequest, "invalid_schedule", "The competition schedule is not valid.")
	}
	return nil
}

// introducesTimestamp reports whether this request is setting the value rather
// than carrying forward what was already stored. Creations introduce every
// timestamp; edits introduce only the ones they change.
func introducesTimestamp(value time.Time, previous *competitionDraft, stored func(*competitionDraft) time.Time) bool {
	return previous == nil || !value.Equal(stored(previous))
}

// normalizeRules keeps the rules snapshot a bounded JSON object. An array or a
// bare scalar would still be valid JSON but would break every consumer that
// expects to read named rule keys.
func normalizeRules(raw json.RawMessage) ([]byte, *organizerFault) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	if len(raw) > maxCompetitionRulesBytes {
		return nil, fault(http.StatusBadRequest, "rules_too_large", "The rules document is too large.")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fault(http.StatusBadRequest, "invalid_rules", "The rules must be a JSON object.")
	}
	if decoded == nil {
		return []byte("{}"), nil
	}
	compacted := &bytes.Buffer{}
	if err := json.Compact(compacted, raw); err != nil {
		return nil, fault(http.StatusBadRequest, "invalid_rules", "The rules must be a JSON object.")
	}
	if _, err := parseCompetitionEligibilityRules(compacted.Bytes()); err != nil {
		return nil, fault(http.StatusBadRequest, "invalid_eligibility_rules", "The competition eligibility rules are invalid.")
	}
	if err := validateMatchVerificationRules(compacted.Bytes()); err != nil {
		message := "The match verification rules are invalid or unsupported."
		var rule *matchVerificationRuleError
		if errors.As(err, &rule) {
			message = rule.message
		}
		return nil, fault(http.StatusBadRequest, "invalid_match_verification_rules", message)
	}
	return compacted.Bytes(), nil
}
