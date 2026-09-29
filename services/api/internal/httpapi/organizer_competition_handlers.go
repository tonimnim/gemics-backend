package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/competition"
	"github.com/jackc/pgx/v5"
)

const (
	maxCompetitionEntries     = 1024
	maxCompetitionNameLength  = 120
	maxCompetitionDescription = 4000
	maxCompetitionRulesBytes  = 32 << 10
	// A KES 50,000 ceiling on an administration fee. Anything above it is a
	// data-entry mistake far more often than a real event.
	maxCompetitionFeeMinor   = 5_000_000
	maxCompetitionPrizeMinor = 500_000_000
)

var competitionFormats = []competition.Format{
	competition.SingleElimination, competition.DoubleElimination, competition.RoundRobin,
}

// organizerFault carries a validation outcome from the pure normalizers to the
// handler, so input rules can be unit tested without an HTTP recorder.
type organizerFault struct {
	Status  int
	Code    string
	Message string
}

func fault(status int, code, message string) *organizerFault {
	return &organizerFault{Status: status, Code: code, Message: message}
}

func (f *organizerFault) write(w http.ResponseWriter) { writeError(w, f.Status, f.Code, f.Message) }

type organizerCompetitionInput struct {
	Name                 string          `json:"name"`
	Slug                 string          `json:"slug"`
	Description          string          `json:"description"`
	GameID               string          `json:"gameId"`
	Format               string          `json:"format"`
	MaxEntries           int             `json:"maxEntries"`
	EntryFeeMinor        int64           `json:"entryFeeMinor"`
	Currency             string          `json:"currency"`
	PrizeAmountMinor     int64           `json:"prizeAmountMinor"`
	PrizeFunding         string          `json:"prizeFunding"`
	RegistrationOpensAt  time.Time       `json:"registrationOpensAt"`
	RegistrationClosesAt time.Time       `json:"registrationClosesAt"`
	CheckInOpensAt       *time.Time      `json:"checkInOpensAt"`
	StartsAt             time.Time       `json:"startsAt"`
	Rules                json.RawMessage `json:"rules"`
}

type organizerCompetitionPatch struct {
	Name                 *string          `json:"name"`
	Slug                 *string          `json:"slug"`
	Description          *string          `json:"description"`
	GameID               *string          `json:"gameId"`
	Format               *string          `json:"format"`
	MaxEntries           *int             `json:"maxEntries"`
	EntryFeeMinor        *int64           `json:"entryFeeMinor"`
	Currency             *string          `json:"currency"`
	PrizeAmountMinor     *int64           `json:"prizeAmountMinor"`
	PrizeFunding         *string          `json:"prizeFunding"`
	RegistrationOpensAt  *time.Time       `json:"registrationOpensAt"`
	RegistrationClosesAt *time.Time       `json:"registrationClosesAt"`
	CheckInOpensAt       *time.Time       `json:"checkInOpensAt"`
	StartsAt             *time.Time       `json:"startsAt"`
	Rules                *json.RawMessage `json:"rules"`
}

// competitionDraft is the validated, storage-shaped form of an organizer's
// input. Every field here has already passed the rules the database constraints
// also enforce, so a constraint violation at this point is a bug, not a 400.
type competitionDraft struct {
	Name                 string
	Slug                 string
	Description          string
	GameID               string
	Format               string
	MaxEntries           int
	EntryFeeMinor        int64
	FeePurpose           string
	Currency             string
	PrizeAmountMinor     int64
	PrizeFunding         string
	RegistrationOpensAt  time.Time
	RegistrationClosesAt time.Time
	CheckInOpensAt       *time.Time
	StartsAt             time.Time
	Rules                []byte
}

type organizerCompetition struct {
	ID                   string          `json:"id"`
	OrganizationID       string          `json:"organizationId"`
	GameID               string          `json:"gameId"`
	GameName             string          `json:"gameName"`
	Name                 string          `json:"name"`
	Slug                 string          `json:"slug"`
	Description          string          `json:"description"`
	Format               string          `json:"format"`
	Status               string          `json:"status"`
	MaxEntries           int             `json:"maxEntries"`
	EntryCount           int             `json:"entryCount"`
	AvailableSlots       int             `json:"availableSlots"`
	EntryFeeMinor        int64           `json:"entryFeeMinor"`
	FeePurpose           string          `json:"feePurpose"`
	Currency             string          `json:"currency"`
	PrizeAmountMinor     int64           `json:"prizeAmountMinor"`
	PrizeFunding         string          `json:"prizeFunding"`
	RegistrationOpensAt  time.Time       `json:"registrationOpensAt"`
	RegistrationClosesAt time.Time       `json:"registrationClosesAt"`
	CheckInOpensAt       *time.Time      `json:"checkInOpensAt"`
	StartsAt             time.Time       `json:"startsAt"`
	RulesVersion         int             `json:"rulesVersion"`
	Rules                json.RawMessage `json:"rules"`
	AllowedTransitions   []string        `json:"allowedTransitions"`
	MatchCount           int             `json:"matchCount"`
	CreatedBy            string          `json:"createdBy"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

type organizerCompetitionPage struct {
	Data []organizerCompetition `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type organizerEntry struct {
	ID            string     `json:"id"`
	DisplayName   string     `json:"displayName"`
	Status        string     `json:"status"`
	Seed          *int       `json:"seed"`
	CheckedInAt   *time.Time `json:"checkedInAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	PlayerID      string     `json:"playerId"`
	PlayerName    string     `json:"playerName"`
	Handle        *string    `json:"handle"`
	InGameName    *string    `json:"inGameName"`
	Platform      *string    `json:"platform"`
	PaymentStatus *string    `json:"paymentStatus"`
}

type organizerEntryPage struct {
	Data []organizerEntry `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type competitionTransitionInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// listOrganizerCompetitions returns every competition the organization owns,
// including drafts and cancelled events the public discovery endpoints hide.
// Ordering matches competitions_org_status_starts_idx so the keyset walk stays
// on the index.
func (s *Server) listOrganizerCompetitions(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	limit, ok := parseCompetitionLimit(w, r, 20, 50)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validCompetitionStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_status", "That competition status does not exist.")
		return
	}
	var cursorTime *time.Time
	var cursorID *string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor := &competitionCursor{}
		if !decodeCompetitionCursor(raw, cursor) || !uuidPattern.MatchString(cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid.")
			return
		}
		cursorTime, cursorID = &cursor.StartsAt, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), organizerCompetitionSelect+`
		WHERE competition.organization_id=$1 AND ($2='' OR competition.status=$2)
		AND ($3::timestamptz IS NULL OR competition.starts_at<$3
		     OR (competition.starts_at=$3 AND competition.id>$4::uuid))
		ORDER BY competition.starts_at DESC,competition.id LIMIT $5`,
		membership.OrganizationID, status, cursorTime, cursorID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load competitions.")
		return
	}
	defer rows.Close()
	page := organizerCompetitionPage{Data: make([]organizerCompetition, 0, limit)}
	for rows.Next() {
		item, scanErr := scanOrganizerCompetition(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load competitions.")
			return
		}
		page.Data = append(page.Data, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load competitions.")
		return
	}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next := encodeCompetitionCursor(competitionCursor{StartsAt: last.StartsAt, ID: last.ID})
		page.Page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getOrganizerCompetition(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	competitionID := strings.TrimSpace(r.PathValue("competitionId"))
	if !uuidPattern.MatchString(competitionID) {
		writeCompetitionNotFound(w)
		return
	}
	item, err := s.loadOrganizerCompetition(r, s.db.Writer, membership.OrganizationID, competitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCompetitionNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": item})
}

// createOrganizerCompetition writes a draft. Drafts are invisible to players, so
// this route creates nothing a player can act on until a later transition
// publishes it.
func (s *Server) createOrganizerCompetition(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	var input organizerCompetitionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	draft, problem := normalizeCompetitionInput(input, time.Now().UTC())
	if problem != nil {
		problem.write(w)
		return
	}

	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the competition.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if problem = assertPlayableGame(r, tx, draft.GameID); problem != nil {
		problem.write(w)
		return
	}
	var competitionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO competitions(organization_id,game_id,name,slug,description,format,
		max_entries,entry_fee_minor,fee_purpose,currency,prize_amount_minor,prize_funding,
		registration_opens_at,registration_closes_at,check_in_opens_at,starts_at,rules_snapshot,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18) RETURNING id`,
		membership.OrganizationID, draft.GameID, draft.Name, draft.Slug, draft.Description, draft.Format,
		draft.MaxEntries, draft.EntryFeeMinor, draft.FeePurpose, draft.Currency, draft.PrizeAmountMinor,
		draft.PrizeFunding, draft.RegistrationOpensAt, draft.RegistrationClosesAt, draft.CheckInOpensAt,
		draft.StartsAt, draft.Rules, userID).Scan(&competitionID)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken",
			"Your organization already has a competition with that slug.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the competition.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, userID, "competition.created", "competition",
		competitionID, nil, map[string]any{"name": draft.Name, "slug": draft.Slug, "status": "draft",
			"format": draft.Format, "maxEntries": draft.MaxEntries}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the competition.")
		return
	}
	item, err := s.loadOrganizerCompetition(r, tx, membership.OrganizationID, competitionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the new competition.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the competition.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": item})
}

// patchOrganizerCompetition applies an edit under the rules for the current
// status. The competition row is locked first so the status a change is
// validated against is the status it is written against.
func (s *Server) patchOrganizerCompetition(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	competitionID := strings.TrimSpace(r.PathValue("competitionId"))
	if !uuidPattern.MatchString(competitionID) {
		writeCompetitionNotFound(w)
		return
	}
	var patch organizerCompetitionPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the competition.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	current, status, rulesVersion, entryCount, err := lockOrganizerCompetition(r, tx,
		membership.OrganizationID, competitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCompetitionNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	updated, rulesChanged, problem := applyCompetitionPatch(current, patch, status, entryCount, time.Now().UTC())
	if problem != nil {
		problem.write(w)
		return
	}
	if updated.GameID != current.GameID {
		if problem = assertPlayableGame(r, tx, updated.GameID); problem != nil {
			problem.write(w)
			return
		}
	}
	// A published rules change must be visible as a new version: entrants agreed
	// to a numbered ruleset, and silently editing it underneath them would make
	// the audit trail a lie.
	nextRulesVersion := rulesVersion
	if rulesChanged && status != string(competition.StatusDraft) {
		nextRulesVersion++
	}
	_, err = tx.Exec(r.Context(), `UPDATE competitions SET name=$2,slug=$3,description=$4,game_id=$5,format=$6,
		max_entries=$7,entry_fee_minor=$8,fee_purpose=$9,currency=$10,prize_amount_minor=$11,prize_funding=$12,
		registration_opens_at=$13,registration_closes_at=$14,check_in_opens_at=$15,starts_at=$16,
		rules_snapshot=$17::jsonb,rules_version=$18,updated_at=now() WHERE id=$1`,
		competitionID, updated.Name, updated.Slug, updated.Description, updated.GameID, updated.Format,
		updated.MaxEntries, updated.EntryFeeMinor, updated.FeePurpose, updated.Currency, updated.PrizeAmountMinor,
		updated.PrizeFunding, updated.RegistrationOpensAt, updated.RegistrationClosesAt, updated.CheckInOpensAt,
		updated.StartsAt, updated.Rules, nextRulesVersion)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken",
			"Your organization already has a competition with that slug.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the competition.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, userID, "competition.updated", "competition",
		competitionID, competitionAuditState(current, rulesVersion),
		competitionAuditState(updated, nextRulesVersion)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	item, err := s.loadOrganizerCompetition(r, tx, membership.OrganizationID, competitionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the competition.")
		return
	}
	s.invalidateCompetitionCaches(r, competitionID)
	writeJSON(w, http.StatusOK, map[string]any{"data": item})
}

// transitionOrganizerCompetition is the only route that changes a competition's
// status. It defers legality to the domain state machine and then applies the
// guards that need database state the domain model cannot see.
func (s *Server) transitionOrganizerCompetition(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	competitionID := strings.TrimSpace(r.PathValue("competitionId"))
	if !uuidPattern.MatchString(competitionID) {
		writeCompetitionNotFound(w)
		return
	}
	var input competitionTransitionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	target := competition.Status(strings.TrimSpace(input.Status))
	if !validCompetitionStatus(string(target)) {
		writeError(w, http.StatusBadRequest, "invalid_status", "That competition status does not exist.")
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if len(reason) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_reason", "The reason must be 500 characters or fewer.")
		return
	}

	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the status.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	// Only the owning organization may queue on the gate (D28);
	// lockOrganizerCompetition repeats the check authoritatively.
	err = probeOrganizerCompetition(r.Context(), tx, membership.OrganizationID, competitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCompetitionNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	// A drawn competition's row and matches are also locked by result
	// finalizers, which take the competition gate first; so does this path.
	if err = lockCompetitionProgressionGate(r.Context(), tx, competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the status.")
		return
	}
	current, status, _, _, err := lockOrganizerCompetition(r, tx, membership.OrganizationID, competitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCompetitionNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	if status == string(target) {
		writeError(w, http.StatusConflict, "already_in_status", "The competition is already in that status.")
		return
	}
	model := competition.Competition{Status: competition.Status(status)}
	if err := model.Transition(target); err != nil {
		writeError(w, http.StatusConflict, "invalid_transition",
			"A competition cannot move from "+status+" to "+string(target)+".")
		return
	}
	if problem := s.assertTransitionReady(r, tx, competitionID, current, target); problem != nil {
		problem.write(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE competitions SET status=$2,updated_at=now() WHERE id=$1`,
		competitionID, string(target)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the status.")
		return
	}
	if target == competition.StatusCancelled {
		if err = cancelCompetitionMatches(r.Context(), tx, membership.OrganizationID, competitionID, userID,
			r.Header.Get("X-Request-ID")); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to cancel the competition's matches.")
			return
		}
		if err = createCompetitionCancellationRefunds(r, tx, competitionID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create cancellation refunds.")
			return
		}
	}
	after := map[string]any{"status": string(target)}
	if reason != "" {
		after["reason"] = reason
	}
	if err = appendAudit(r, tx, membership.OrganizationID, userID, "competition."+string(target), "competition",
		competitionID, map[string]any{"status": status}, after); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	// Every parameter inside jsonb_build_object needs an explicit cast. The
	// function takes "any", so PostgreSQL cannot infer a type and rejects the
	// statement at parse time with "could not determine data type of parameter".
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('competition',$1,$2,jsonb_build_object('competitionId',$1::text,'organizationId',$3::text,
		'fromStatus',$4::text,'toStatus',$5::text))`,
		competitionID, "competition."+string(target), membership.OrganizationID, status, string(target)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the change.")
		return
	}
	item, err := s.loadOrganizerCompetition(r, tx, membership.OrganizationID, competitionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the status.")
		return
	}
	// Publishing and cancelling change what players see right now, so the public
	// caches are dropped rather than left to expire.
	s.invalidateCompetitionCaches(r, competitionID)
	writeJSON(w, http.StatusOK, map[string]any{"data": item})
}

// listOrganizerCompetitionEntries is the registration desk: who is in, who
// checked in, and for paid events whether their money actually arrived.
func (s *Server) listOrganizerCompetitionEntries(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	competitionID := strings.TrimSpace(r.PathValue("competitionId"))
	if !uuidPattern.MatchString(competitionID) {
		writeCompetitionNotFound(w)
		return
	}
	limit, ok := parseCompetitionLimit(w, r, 50, 100)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validEntryStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_status", "That entry status does not exist.")
		return
	}
	var cursorTime *time.Time
	var cursorID *string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor := &registrationCursor{}
		if !decodeCompetitionCursor(raw, cursor) || !uuidPattern.MatchString(cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid.")
			return
		}
		cursorTime, cursorID = &cursor.CreatedAt, &cursor.ID
	}
	// Resolve the competition inside this organization first. Without this, a
	// competition id belonging to another organization would return an empty
	// page, which reads as "this competition has no entries" rather than "this
	// competition is not yours".
	var exists bool
	if err := s.db.Writer.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM competitions
		WHERE id=$1 AND organization_id=$2)`, competitionID, membership.OrganizationID).Scan(&exists); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the entries.")
		return
	}
	if !exists {
		writeCompetitionNotFound(w)
		return
	}
	// The organization filter is repeated on the competition join below. The
	// isolation must live in the SQL as well as the lookup above, so a future
	// edit to either one cannot silently open a cross-tenant read.
	rows, err := s.db.Writer.Query(r.Context(), `SELECT entry.id,entry.display_name,entry.status,entry.seed,
		entry.checked_in_at,entry.created_at,entry.captain_user_id,player.display_name,profile.handle,
		account.in_game_name,account.platform,payment.status
		FROM competition_entries entry
		JOIN competitions competition ON competition.id=entry.competition_id AND competition.organization_id=$1
		JOIN users player ON player.id=entry.captain_user_id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		LEFT JOIN entry_members member ON member.entry_id=entry.id AND member.user_id=entry.captain_user_id
		LEFT JOIN game_accounts account ON account.id=member.game_account_id
		LEFT JOIN LATERAL (SELECT status FROM payment_intents
			WHERE competition_id=entry.competition_id AND user_id=entry.captain_user_id
			ORDER BY created_at DESC LIMIT 1) payment ON true
		WHERE entry.competition_id=$2 AND ($3='' OR entry.status=$3)
		AND ($4::timestamptz IS NULL OR entry.created_at<$4
		     OR (entry.created_at=$4 AND entry.id>$5::uuid))
		ORDER BY entry.created_at DESC,entry.id LIMIT $6`,
		membership.OrganizationID, competitionID, status, cursorTime, cursorID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the entries.")
		return
	}
	defer rows.Close()
	page := organizerEntryPage{Data: make([]organizerEntry, 0, limit)}
	for rows.Next() {
		var item organizerEntry
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Status, &item.Seed, &item.CheckedInAt,
			&item.CreatedAt, &item.PlayerID, &item.PlayerName, &item.Handle, &item.InGameName,
			&item.Platform, &item.PaymentStatus); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the entries.")
			return
		}
		page.Data = append(page.Data, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the entries.")
		return
	}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next := encodeCompetitionCursor(registrationCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.Page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

// assertTransitionReady holds the guards that need database state: a
// competition cannot open registration into the past, cannot start without a
// bracket, and cannot complete with matches still outstanding.
func (s *Server) assertTransitionReady(r *http.Request, tx pgx.Tx, competitionID string,
	current competitionDraft, target competition.Status) *organizerFault {
	now := time.Now().UTC()
	switch target {
	case competition.StatusPublished:
		if !current.StartsAt.After(now) {
			return fault(http.StatusConflict, "start_time_passed",
				"Move the start time into the future before publishing.")
		}
	case competition.StatusRegistration:
		if !current.RegistrationClosesAt.After(now) {
			return fault(http.StatusConflict, "registration_window_passed",
				"Registration already closed. Extend the closing time before opening registration.")
		}
	case competition.StatusCheckIn:
		var accepted int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM competition_entries
			WHERE competition_id=$1 AND status NOT IN ('withdrawn','disqualified')`, competitionID).Scan(&accepted); err != nil {
			return fault(http.StatusServiceUnavailable, "database_unavailable", "Unable to count the entries.")
		}
		if accepted < 2 {
			return fault(http.StatusConflict, "not_enough_entries",
				"A competition needs at least two active entries before check-in.")
		}
	case competition.StatusRunning:
		var matches int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM matches WHERE competition_id=$1`,
			competitionID).Scan(&matches); err != nil {
			return fault(http.StatusServiceUnavailable, "database_unavailable", "Unable to check the bracket.")
		}
		if matches == 0 {
			// Nothing in this service writes matches yet. Failing loudly here is
			// better than starting a competition whose players would open an
			// empty match list.
			return fault(http.StatusConflict, "bracket_not_generated",
				"Generate the bracket before starting this competition.")
		}
	case competition.StatusCompleted:
		var outstanding int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM matches WHERE competition_id=$1
			AND state NOT IN ('completed','forfeit','cancelled')`, competitionID).Scan(&outstanding); err != nil {
			return fault(http.StatusServiceUnavailable, "database_unavailable", "Unable to check the matches.")
		}
		if outstanding > 0 {
			return fault(http.StatusConflict, "matches_outstanding",
				"Finish or cancel every match before completing this competition.")
		}
	}
	return nil
}

// invalidateCompetitionCaches retires the public views an organizer write can
// change. The detail, bracket and standings keys are addressable and deleted
// directly; the list keys are query-hashed, so they are retired by bumping the
// collection generation instead.
func (s *Server) invalidateCompetitionCaches(r *http.Request, competitionID string) {
	s.invalidateCompetitionCachesContext(r.Context(), competitionID)
	s.responses.BumpGeneration(r.Context(), competitionCacheFamily)
}

// invalidateCompetitionCachesContext retires the addressable detail, bracket
// and standings views. Every path that progresses a match calls it after its
// commit, so a result reaches the tables and placements as it reaches the
// bracket. High-frequency registrations/results intentionally do not bump the
// collection generation: list pages keep their short 15-second counter staleness
// instead of turning every registration into a platform-wide cache miss.
func (s *Server) invalidateCompetitionCachesContext(ctx context.Context, competitionID string) {
	s.responses.Invalidate(ctx, "competition-detail:"+competitionID, "competition-bracket:"+competitionID,
		competitionStandingsCacheKey(competitionID))
}

// organizerCompetitionSelect is shared by the list and single-record reads so
// the two can never drift into returning different shapes.
const organizerCompetitionSelect = `SELECT competition.id,competition.organization_id,competition.game_id,game.name,
	competition.name,competition.slug,competition.description,competition.format,competition.status,
	competition.max_entries,
	(SELECT count(*) FROM competition_entries entry WHERE entry.competition_id=competition.id
	 AND entry.status NOT IN ('withdrawn','disqualified')),
	(SELECT count(*) FROM matches WHERE matches.competition_id=competition.id),
	competition.entry_fee_minor,competition.fee_purpose,competition.currency,competition.prize_amount_minor,
	competition.prize_funding,competition.registration_opens_at,competition.registration_closes_at,
	competition.check_in_opens_at,competition.starts_at,competition.rules_version,competition.rules_snapshot,
	competition.created_by,competition.created_at,competition.updated_at
	FROM competitions competition JOIN games game ON game.id=competition.game_id`

type rowScanner interface {
	Scan(destinations ...any) error
}

func scanOrganizerCompetition(row rowScanner) (organizerCompetition, error) {
	var item organizerCompetition
	if err := row.Scan(&item.ID, &item.OrganizationID, &item.GameID, &item.GameName, &item.Name, &item.Slug,
		&item.Description, &item.Format, &item.Status, &item.MaxEntries, &item.EntryCount, &item.MatchCount,
		&item.EntryFeeMinor, &item.FeePurpose, &item.Currency, &item.PrizeAmountMinor, &item.PrizeFunding,
		&item.RegistrationOpensAt, &item.RegistrationClosesAt, &item.CheckInOpensAt, &item.StartsAt,
		&item.RulesVersion, &item.Rules, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return organizerCompetition{}, err
	}
	item.AvailableSlots = max(0, item.MaxEntries-item.EntryCount)
	item.AllowedTransitions = make([]string, 0, 2)
	for _, status := range competition.AllowedTransitions(competition.Status(item.Status)) {
		item.AllowedTransitions = append(item.AllowedTransitions, string(status))
	}
	return item, nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Server) loadOrganizerCompetition(r *http.Request, source queryRower,
	organizationID, competitionID string) (organizerCompetition, error) {
	row := source.QueryRow(r.Context(), organizerCompetitionSelect+`
		WHERE competition.id=$1 AND competition.organization_id=$2`, competitionID, organizationID)
	return scanOrganizerCompetition(row)
}

// probeOrganizerCompetition is the unlocked ownership probe that runs before
// the competition gate: another organization gets pgx.ErrNoRows, so it can
// never queue behind a competition's finalization (D28).
func probeOrganizerCompetition(ctx context.Context, tx pgx.Tx, organizationID, competitionID string) error {
	var found int
	return tx.QueryRow(ctx, `SELECT 1 FROM competitions WHERE id=$1 AND organization_id=$2`,
		competitionID, organizationID).Scan(&found)
}

// lockOrganizerCompetition takes a row lock and returns the competition in
// draft shape so a patch or transition validates against the same snapshot it
// writes.
func lockOrganizerCompetition(r *http.Request, tx pgx.Tx, organizationID, competitionID string) (
	competitionDraft, string, int, int, error) {
	var draft competitionDraft
	var status string
	var rulesVersion, entryCount int
	err := tx.QueryRow(r.Context(), `SELECT competition.name,competition.slug,competition.description,
		competition.game_id,competition.format,competition.status,competition.max_entries,
		competition.entry_fee_minor,competition.fee_purpose,competition.currency,competition.prize_amount_minor,
		competition.prize_funding,competition.registration_opens_at,competition.registration_closes_at,
		competition.check_in_opens_at,competition.starts_at,competition.rules_version,competition.rules_snapshot,
		(SELECT count(*) FROM competition_entries entry WHERE entry.competition_id=competition.id
		 AND entry.status NOT IN ('withdrawn','disqualified'))
		FROM competitions competition WHERE competition.id=$1 AND competition.organization_id=$2
		FOR UPDATE OF competition`, competitionID, organizationID).
		Scan(&draft.Name, &draft.Slug, &draft.Description, &draft.GameID, &draft.Format, &status, &draft.MaxEntries,
			&draft.EntryFeeMinor, &draft.FeePurpose, &draft.Currency, &draft.PrizeAmountMinor, &draft.PrizeFunding,
			&draft.RegistrationOpensAt, &draft.RegistrationClosesAt, &draft.CheckInOpensAt, &draft.StartsAt,
			&rulesVersion, &draft.Rules, &entryCount)
	return draft, status, rulesVersion, entryCount, err
}

func assertPlayableGame(r *http.Request, tx pgx.Tx, gameID string) *organizerFault {
	var active bool
	err := tx.QueryRow(r.Context(), `SELECT active FROM games WHERE id=$1`, gameID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return fault(http.StatusBadRequest, "unsupported_game", "That game is not available for competitions.")
	}
	if err != nil {
		return fault(http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the game.")
	}
	return nil
}

func competitionAuditState(draft competitionDraft, rulesVersion int) map[string]any {
	return map[string]any{
		"name": draft.Name, "slug": draft.Slug, "format": draft.Format, "gameId": draft.GameID,
		"maxEntries": draft.MaxEntries, "entryFeeMinor": draft.EntryFeeMinor, "currency": draft.Currency,
		"prizeAmountMinor": draft.PrizeAmountMinor, "prizeFunding": draft.PrizeFunding,
		"registrationOpensAt": draft.RegistrationOpensAt, "registrationClosesAt": draft.RegistrationClosesAt,
		"startsAt": draft.StartsAt, "rulesVersion": rulesVersion,
	}
}

func writeCompetitionNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
}

func validCompetitionStatus(value string) bool {
	switch competition.Status(value) {
	case competition.StatusDraft, competition.StatusPublished, competition.StatusRegistration,
		competition.StatusCheckIn, competition.StatusRunning, competition.StatusCompleted,
		competition.StatusCancelled:
		return true
	}
	return false
}

func validEntryStatus(value string) bool {
	switch value {
	case "registered", "checked_in", "accepted", "withdrawal_pending", "withdrawn", "disqualified":
		return true
	}
	return false
}
