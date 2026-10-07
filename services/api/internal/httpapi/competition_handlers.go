package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gamicscache "github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var publicCompetitionStatuses = []string{"published", "registration_open", "check_in", "running", "completed"}

// readableCompetitionSQL is the visibility rule of the addressable public reads
// (detail, bracket, eligibility and entry) for a row aliased "competition": a
// discovery status, or a cancellation of a competition that was published
// first, of an active game and an active organizer. Entrants keep their
// registrations and a cancellation push that link to it, so it must not turn
// into a 404; discovery lists still leave it out. A draft cancelled before
// publication was never public and stays hidden.
const readableCompetitionSQL = `(competition.status IN ('published','registration_open','check_in','running','completed')
	OR (competition.status='cancelled' AND competition.published_at IS NOT NULL))
	AND EXISTS(SELECT 1 FROM games readable_game WHERE readable_game.id=competition.game_id AND readable_game.active)
	AND EXISTS(SELECT 1 FROM organizations readable_organization
		WHERE readable_organization.id=competition.organization_id AND readable_organization.status='active')`

// competitionCacheFamily groups every cached public competition collection so an
// organizer action that changes which competitions exist can retire all of them
// at once.
const competitionCacheFamily = "competitions"

type competitionListFilter struct {
	GameID    string
	Status    string
	EntryType string
	Query     string
	Limit     int
	Cursor    *competitionCursor
}

type competitionCursor struct {
	StartsAt time.Time `json:"startsAt"`
	ID       string    `json:"id"`
}

type registrationCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

type competitionSummary struct {
	ID                   string     `json:"id"`
	Slug                 string     `json:"slug"`
	Name                 string     `json:"name"`
	Description          string     `json:"description"`
	GameID               string     `json:"gameId"`
	GameName             string     `json:"gameName"`
	OrganizerID          string     `json:"organizerId"`
	OrganizerName        string     `json:"organizerName"`
	OrganizerSlug        string     `json:"organizerSlug"`
	Format               string     `json:"format"`
	Status               string     `json:"status"`
	MaxEntries           int        `json:"maxEntries"`
	EntryCount           int        `json:"entryCount"`
	AvailableSlots       int        `json:"availableSlots"`
	EntryType            string     `json:"entryType"`
	EntryFeeMinor        int64      `json:"entryFeeMinor"`
	FeePurpose           string     `json:"feePurpose"`
	Currency             string     `json:"currency"`
	PrizeAmountMinor     int64      `json:"prizeAmountMinor"`
	PrizeFunding         string     `json:"prizeFunding"`
	RegistrationOpensAt  time.Time  `json:"registrationOpensAt"`
	RegistrationClosesAt time.Time  `json:"registrationClosesAt"`
	CheckInOpensAt       *time.Time `json:"checkInOpensAt"`
	StartsAt             time.Time  `json:"startsAt"`
	RulesVersion         int        `json:"rulesVersion"`
}

type competitionDetail struct {
	competitionSummary
	Rules json.RawMessage `json:"rules"`
}

type competitionPage struct {
	Data []competitionSummary `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type freeRegistrationInput struct {
	GameAccountID string `json:"gameAccountId"`
}

type registrationItem struct {
	ID              string    `json:"id"`
	CompetitionID   string    `json:"competitionId"`
	CompetitionName string    `json:"competitionName"`
	CompetitionSlug string    `json:"competitionSlug"`
	GameID          string    `json:"gameId"`
	GameName        string    `json:"gameName"`
	DisplayName     string    `json:"displayName"`
	GameAccountID   string    `json:"gameAccountId"`
	Status          string    `json:"status"`
	EntryFeeMinor   int64     `json:"entryFeeMinor"`
	Currency        string    `json:"currency"`
	StartsAt        time.Time `json:"startsAt"`
	CreatedAt       time.Time `json:"createdAt"`
	// CompetitionStatus tells an entry that stays registered that its
	// competition was cancelled.
	CompetitionStatus string `json:"competitionStatus"`
}

type registrationPage struct {
	Data []registrationItem `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type bracketStage struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Position int            `json:"position"`
	Format   string         `json:"format"`
	BestOf   int            `json:"bestOf"`
	Status   string         `json:"status"`
	Rounds   []bracketRound `json:"rounds"`
}

type bracketRound struct {
	Number  int            `json:"number"`
	Name    string         `json:"name"`
	Bracket string         `json:"bracket"`
	Matches []bracketMatch `json:"matches"`
}

type bracketMatch struct {
	ID            string             `json:"id"`
	Code          string             `json:"code"`
	MatchNumber   int                `json:"matchNumber"`
	State         string             `json:"state"`
	Home          bracketSlot        `json:"home"`
	Away          bracketSlot        `json:"away"`
	Score         *bracketScore      `json:"score"`
	WinnerEntryID *string            `json:"winnerEntryId"`
	ScheduledAt   *time.Time         `json:"scheduledAt"`
	ResultDueAt   *time.Time         `json:"resultDueAt"`
	CompletedAt   *time.Time         `json:"completedAt"`
	Progression   bracketProgression `json:"progression"`
	Version       int                `json:"version"`
}

type bracketSlot struct {
	Side        string              `json:"side"`
	Participant *bracketParticipant `json:"participant"`
	Source      *bracketSlotSource  `json:"source"`
}

type bracketParticipant struct {
	EntryID     string `json:"entryId"`
	DisplayName string `json:"displayName"`
	Seed        *int   `json:"seed"`
}

type bracketSlotSource struct {
	Kind            string     `json:"kind"`
	EntryID         *string    `json:"entryId"`
	MatchID         *string    `json:"matchId"`
	SourceGraphRank *int       `json:"sourceGraphRank"`
	ResolvedEntryID *string    `json:"resolvedEntryId"`
	ResolvedAt      *time.Time `json:"resolvedAt"`
	VoidedAt        *time.Time `json:"voidedAt"`
}

type bracketScore struct {
	SubmissionID string              `json:"submissionId"`
	HomeScore    int                 `json:"homeScore"`
	AwayScore    int                 `json:"awayScore"`
	Tiebreak     *tiebreakScoreInput `json:"tiebreak"`
	ConfirmedAt  time.Time           `json:"confirmedAt"`
}

type bracketProgression struct {
	GraphRank               int     `json:"graphRank"`
	ActivationRule          string  `json:"activationRule"`
	ActivationSourceMatchID *string `json:"activationSourceMatchId"`
	CompletionReason        *string `json:"completionReason"`
}

type bracketSlotRecord struct {
	SourceKind      *string
	SourceEntryID   *string
	SourceMatchID   *string
	SourceGraphRank *int
	ResolvedEntryID *string
	ResolvedAt      *time.Time
	VoidedAt        *time.Time
}

type bracketQueryRow struct {
	StageID                 string
	StageName               string
	StagePosition           int
	StageFormat             string
	StageBestOf             int
	StageStatus             string
	MatchID                 *string
	Bracket                 *string
	RoundNumber             *int
	MatchNumber             *int
	MatchState              *string
	GraphRank               *int
	ActivationRule          *string
	ActivationSourceMatchID *string
	CompletionReason        *string
	HomeEntryID             *string
	HomeDisplayName         *string
	HomeSeed                *int
	AwayEntryID             *string
	AwayDisplayName         *string
	AwaySeed                *int
	WinnerEntryID           *string
	ScheduledAt             *time.Time
	ResultDueAt             *time.Time
	CompletedAt             *time.Time
	Version                 *int
	ScoreSubmissionID       *string
	HomeScore               *int
	AwayScore               *int
	TiebreakType            *string
	HomeTiebreakScore       *int
	AwayTiebreakScore       *int
	ScoreConfirmedAt        *time.Time
	HomeSource              bracketSlotRecord
	AwaySource              bracketSlotRecord
}

func (s *Server) listCompetitions(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	filter, ok := parseCompetitionFilter(w, r)
	if !ok {
		return
	}
	// The generation stamp is what lets publishing a competition take effect
	// immediately. Without it a newly published event would stay invisible for
	// the whole freshness window, because this key is a hash nobody can target.
	keyDigest := sha256.Sum256([]byte(r.URL.Query().Encode()))
	cacheKey := "competition-list:" + s.responses.Generation(r.Context(), competitionCacheFamily) +
		":" + hex.EncodeToString(keyDigest[:])
	response, err := s.responses.GetOrLoad(r.Context(), cacheKey, gamicscache.Policy{
		FreshFor: 15 * time.Second, KeepFor: 45 * time.Second, LoadTimeout: 3 * time.Second,
		LockFor: 4 * time.Second, WaitFor: 700 * time.Millisecond, MaxBodyBytes: 2 << 20,
	}, func(ctx context.Context) ([]byte, error) {
		page, loadErr := s.loadCompetitionPage(ctx, filter)
		if loadErr != nil {
			return nil, loadErr
		}
		return json.Marshal(page)
	})
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "competitions_unavailable", "Competitions are temporarily unavailable.")
		return
	}
	writeCompetitionCacheResponse(w, r, response, "public, max-age=5, s-maxage=15, stale-while-revalidate=30")
}

func (s *Server) getCompetition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	response, err := s.responses.GetOrLoad(r.Context(), "competition-detail:"+id, gamicscache.Policy{
		FreshFor: 10 * time.Second, KeepFor: 30 * time.Second, LoadTimeout: 3 * time.Second,
		LockFor: 4 * time.Second, WaitFor: 700 * time.Millisecond, MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context) ([]byte, error) {
		detail, loadErr := s.loadCompetitionDetail(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		return json.Marshal(map[string]any{"data": detail})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "competitions_unavailable", "Competition is temporarily unavailable.")
		return
	}
	writeCompetitionCacheResponse(w, r, response, "public, max-age=5, s-maxage=10, stale-while-revalidate=30")
}

func (s *Server) getCompetitionBracket(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	response, err := s.responses.GetOrLoad(r.Context(), "competition-bracket:"+id, gamicscache.Policy{
		FreshFor: 2 * time.Second, KeepFor: 20 * time.Second, LoadTimeout: 3 * time.Second,
		LockFor: 3 * time.Second, WaitFor: 400 * time.Millisecond, MaxBodyBytes: 4 << 20,
	}, func(ctx context.Context) ([]byte, error) {
		stages, loadErr := s.loadCompetitionBracket(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		return json.Marshal(map[string]any{"data": map[string]any{"competitionId": id, "stages": stages}})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "bracket_unavailable", "The bracket is temporarily unavailable.")
		return
	}
	writeCompetitionCacheResponse(w, r, response, "public, max-age=1, s-maxage=2, stale-while-revalidate=20")
}

func (s *Server) createFreeRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	competitionID := strings.TrimSpace(r.PathValue("id"))
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !uuidPattern.MatchString(competitionID) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Provide an Idempotency-Key between 8 and 128 characters.")
		return
	}
	var input freeRegistrationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.GameAccountID = strings.TrimSpace(input.GameAccountID)
	if !uuidPattern.MatchString(input.GameAccountID) {
		writeError(w, http.StatusBadRequest, "invalid_game_account", "Choose a valid game account.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	requestDigest := sha256.Sum256([]byte(userID + "|" + competitionID + "|" + input.GameAccountID))
	requestHash := hex.EncodeToString(requestDigest[:])
	scope := "free-registration:" + userID + ":" + competitionID

	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to register right now.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,11))", scope+":"+idempotencyKey); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to register right now.")
		return
	}
	var storedHash string
	var storedStatus *int
	var storedBody []byte
	err = tx.QueryRow(r.Context(), `SELECT request_hash,response_status,response_body FROM idempotency_keys
		WHERE scope=$1 AND key=$2 AND expires_at>now()`, scope, idempotencyKey).Scan(&storedHash, &storedStatus, &storedBody)
	if err == nil {
		if storedHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another registration request.")
			return
		}
		if storedStatus != nil && len(storedBody) > 0 {
			if err := tx.Commit(r.Context()); err != nil {
				writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the registration.")
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(*storedStatus)
			_, _ = w.Write(storedBody)
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to register right now.")
		return
	}

	var competitionStatus, competitionGameID, currency, organizationID string
	var competitionName, competitionSlug, gameName string
	var feeMinor int64
	var maxEntries int
	var startsAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT status,game_id,entry_fee_minor,currency,max_entries,organization_id,name,slug,
		starts_at,(SELECT name FROM games WHERE id=competitions.game_id) FROM competitions WHERE id=$1 FOR UPDATE`, competitionID).
		Scan(&competitionStatus, &competitionGameID, &feeMinor, &currency, &maxEntries, &organizationID, &competitionName,
			&competitionSlug, &startsAt, &gameName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition.")
		return
	}
	now := time.Now().UTC()
	// Run the same typed policy used by the player preflight while the
	// competition row is locked. The capacity check below remains authoritative
	// for the final insert, but age/country/ranking/account restrictions must not
	// be a client-only promise. Visibility, cancellation, registration status
	// and deadlines are part of the policy, so a hidden competition is not
	// found and a blocked entry is reported exactly as M-Pesa checkout reports
	// it.
	eligibility, eligibilityErr := loadCompetitionEligibility(
		r.Context(), tx, competitionID, userID, input.GameAccountID, now, s.config.StrikeBanThreshold,
	)
	if errors.Is(eligibilityErr, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if errors.Is(eligibilityErr, errInvalidEligibilityPolicy) {
		writeError(w, http.StatusServiceUnavailable, "eligibility_policy_invalid", "This competition's eligibility policy is unavailable.")
		return
	}
	if eligibilityErr != nil {
		writeError(w, http.StatusServiceUnavailable, "eligibility_unavailable", "Eligibility cannot be evaluated right now.")
		return
	}
	if issue := eligibility.firstBlockingIssue(); issue != nil {
		writeCompetitionIneligible(w, eligibility, *issue)
		return
	}
	if feeMinor > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "payment_required", "message": "Start M-Pesa payment to register for this competition.",
			"nextAction": "POST /v1/payments/mpesa/stk-push",
		})
		return
	}

	var accountGameID, displayName string
	var onboardingComplete bool
	err = tx.QueryRow(r.Context(), `SELECT account.game_id,COALESCE(NULLIF(profile.handle,''),account.in_game_name),
		(profile.user_id IS NOT NULL
		 AND player.terms_accepted_at IS NOT NULL AND player.privacy_accepted_at IS NOT NULL)
		FROM users player JOIN game_accounts account ON account.id=$2 AND account.user_id=player.id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1 AND player.status='active'`, userID, input.GameAccountID).
		Scan(&accountGameID, &displayName, &onboardingComplete)
	if errors.Is(err, pgx.ErrNoRows) {
		writeBlockedEntry(w, eligibility, entryIssueGameAccountRequired)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the player account.")
		return
	}
	if !onboardingComplete {
		writeBlockedEntry(w, eligibility, entryIssueProfileIncomplete)
		return
	}
	if accountGameID != competitionGameID {
		writeBlockedEntry(w, eligibility, entryIssueGameAccountMismatch)
		return
	}

	var existing registrationItem
	err = tx.QueryRow(r.Context(), `SELECT entry.id,entry.competition_id,competition.name,competition.slug,
		competition.status,competition.game_id,game.name,entry.display_name,member.game_account_id,entry.status,
		competition.entry_fee_minor,competition.currency,competition.starts_at,entry.created_at
		FROM competition_entries entry JOIN competitions competition ON competition.id=entry.competition_id
		JOIN games game ON game.id=competition.game_id
		JOIN entry_members member ON member.entry_id=entry.id AND member.user_id=$2
		WHERE entry.competition_id=$1 AND entry.captain_user_id=$2`, competitionID, userID).
		Scan(&existing.ID, &existing.CompetitionID, &existing.CompetitionName, &existing.CompetitionSlug,
			&existing.CompetitionStatus, &existing.GameID, &existing.GameName, &existing.DisplayName,
			&existing.GameAccountID, &existing.Status, &existing.EntryFeeMinor, &existing.Currency,
			&existing.StartsAt, &existing.CreatedAt)
	if err == nil {
		if existing.Status == "withdrawn" || existing.Status == "disqualified" {
			writeBlockedEntry(w, eligibility, entryIssueRegistrationNotReusable)
			return
		}
		body, _ := json.Marshal(map[string]any{"data": existing})
		if err := recordRegistrationIdempotency(r.Context(), tx, scope, idempotencyKey, requestHash, http.StatusOK, body); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the registration response.")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the registration.")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check the registration.")
		return
	}
	var occupied int
	if err = tx.QueryRow(r.Context(), competitionCapacityEntriesSQL, competitionID).Scan(&occupied); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify capacity.")
		return
	}
	if occupied >= maxEntries {
		writeBlockedEntry(w, eligibility, entryIssueCompetitionFull)
		return
	}

	var entryID string
	var createdAt time.Time
	if err = tx.QueryRow(r.Context(), `INSERT INTO competition_entries(competition_id,display_name,captain_user_id)
		VALUES ($1,$2,$3) RETURNING id,created_at`, competitionID, displayName, userID).Scan(&entryID, &createdAt); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the registration.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
		VALUES ($1,$2,$3,$4)`, entryID, competitionID, userID, input.GameAccountID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the registration.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,subject_id,request_id,after_state)
		VALUES ($1,$2,'competition.registered','competition_entry',$3,$4,jsonb_build_object('competitionId',$5::text,'status','registered'))`,
		organizationID, userID, entryID, r.Header.Get("X-Request-ID"), competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the registration.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('competition_entry',$1,'competition.entry_registered',jsonb_build_object('competitionId',$2::text,'userId',$3::text))`,
		entryID, competitionID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the registration.")
		return
	}
	item := registrationItem{ID: entryID, CompetitionID: competitionID, CompetitionName: competitionName,
		CompetitionSlug: competitionSlug, CompetitionStatus: competitionStatus, GameID: competitionGameID,
		GameName: gameName, DisplayName: displayName, GameAccountID: input.GameAccountID, Status: "registered",
		EntryFeeMinor: 0, Currency: currency, StartsAt: startsAt, CreatedAt: createdAt}
	body, _ := json.Marshal(map[string]any{"data": item})
	if err = recordRegistrationIdempotency(r.Context(), tx, scope, idempotencyKey, requestHash, http.StatusCreated, body); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the registration response.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish the registration.")
		return
	}
	s.invalidateCompetitionCachesContext(r.Context(), competitionID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

func (s *Server) listMyRegistrations(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	limit, ok := parseCompetitionLimit(w, r, 20, 50)
	if !ok {
		return
	}
	var cursor *registrationCursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor = &registrationCursor{}
		if !decodeCompetitionCursor(raw, cursor) || !uuidPattern.MatchString(cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid.")
			return
		}
	}
	var cursorTime *time.Time
	var cursorID *string
	if cursor != nil {
		cursorTime, cursorID = &cursor.CreatedAt, &cursor.ID
	}
	userID := identityFromContext(r.Context()).UserID
	rows, err := s.db.Writer.Query(r.Context(), `SELECT entry.id,entry.competition_id,competition.name,competition.slug,
		competition.status,competition.game_id,game.name,entry.display_name,member.game_account_id,entry.status,
		competition.entry_fee_minor,competition.currency,competition.starts_at,entry.created_at
		FROM competition_entries entry JOIN competitions competition ON competition.id=entry.competition_id
		JOIN games game ON game.id=competition.game_id
		JOIN entry_members member ON member.entry_id=entry.id AND member.user_id=$1
		WHERE entry.captain_user_id=$1 AND ($2::timestamptz IS NULL OR (entry.created_at,entry.id)<($2,$3::uuid))
		ORDER BY entry.created_at DESC,entry.id DESC LIMIT $4`, userID, cursorTime, cursorID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load registrations.")
		return
	}
	defer rows.Close()
	items := make([]registrationItem, 0, limit)
	for rows.Next() {
		var item registrationItem
		if err := rows.Scan(&item.ID, &item.CompetitionID, &item.CompetitionName, &item.CompetitionSlug,
			&item.CompetitionStatus, &item.GameID, &item.GameName, &item.DisplayName, &item.GameAccountID, &item.Status,
			&item.EntryFeeMinor, &item.Currency, &item.StartsAt, &item.CreatedAt); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load registrations.")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load registrations.")
		return
	}
	page := registrationPage{Data: items}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next := encodeCompetitionCursor(registrationCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.Page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

// probeCompetitionEntry is the unlocked membership probe that runs before the
// competition gate: a caller without an entry gets pgx.ErrNoRows, so spam
// withdrawals never queue behind a competition's finalization (D28).
func probeCompetitionEntry(ctx context.Context, tx pgx.Tx, competitionID, userID string) error {
	var found int
	return tx.QueryRow(ctx, `SELECT 1 FROM competition_entries
		WHERE competition_id=$1 AND captain_user_id=$2`, competitionID, userID).Scan(&found)
}

func (s *Server) withdrawRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	competitionID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(competitionID) {
		writeError(w, http.StatusNotFound, "registration_not_found", "Registration not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw right now.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	// Only an entrant may queue on the gate (D28); the locked read below
	// repeats the check authoritatively.
	err = probeCompetitionEntry(r.Context(), tx, competitionID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "registration_not_found", "Registration not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the registration.")
		return
	}
	// The entry and competition locks below would otherwise invert the gate ->
	// entry order of a concurrent result finalizer that removes this entry.
	if err = lockCompetitionProgressionGate(r.Context(), tx, competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw right now.")
		return
	}
	var entryID, status, organizationID string
	var feeMinor int64
	var closesAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT entry.id,entry.status,competition.entry_fee_minor,
		competition.registration_closes_at,competition.organization_id
		FROM competition_entries entry JOIN competitions competition ON competition.id=entry.competition_id
		WHERE entry.competition_id=$1 AND entry.captain_user_id=$2 FOR UPDATE OF entry,competition`, competitionID, userID).
		Scan(&entryID, &status, &feeMinor, &closesAt, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "registration_not_found", "Registration not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the registration.")
		return
	}
	if status == "withdrawn" {
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to confirm withdrawal.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if feeMinor > 0 {
		writeError(w, http.StatusConflict, "refund_review_required", "Paid registrations require organizer refund review before withdrawal.")
		return
	}
	if status == "checked_in" || status == "disqualified" || !time.Now().Before(closesAt) {
		writeError(w, http.StatusConflict, "withdrawal_closed", "This registration can no longer be withdrawn.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE competition_entries SET status='withdrawn',updated_at=now()
		WHERE id=$1`, entryID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw the registration.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,subject_id,request_id,
		before_state,after_state) VALUES ($1,$2,'competition.withdrawn','competition_entry',$3,$4,
		jsonb_build_object('status',$5::text),jsonb_build_object('status','withdrawn'))`,
		organizationID, userID, entryID, r.Header.Get("X-Request-ID"), status); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the withdrawal.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('competition_entry',$1,'competition.entry_withdrawn',jsonb_build_object('competitionId',$2::text,'userId',$3::text))`,
		entryID, competitionID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the withdrawal.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish the withdrawal.")
		return
	}
	s.invalidateCompetitionCachesContext(r.Context(), competitionID)
	w.WriteHeader(http.StatusNoContent)
}

func parseCompetitionFilter(w http.ResponseWriter, r *http.Request) (competitionListFilter, bool) {
	query := r.URL.Query()
	limit, ok := parseCompetitionLimit(w, r, 20, 50)
	if !ok {
		return competitionListFilter{}, false
	}
	filter := competitionListFilter{
		GameID: strings.TrimSpace(query.Get("gameId")), Status: strings.TrimSpace(query.Get("status")),
		EntryType: strings.TrimSpace(query.Get("entryType")), Query: strings.TrimSpace(query.Get("q")), Limit: limit,
	}
	if filter.Status != "" && !containsCompetitionStatus(publicCompetitionStatuses, filter.Status) {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose a public competition status.")
		return competitionListFilter{}, false
	}
	if filter.EntryType != "" && filter.EntryType != "free" && filter.EntryType != "paid" {
		writeError(w, http.StatusBadRequest, "invalid_entry_type", "Entry type must be free or paid.")
		return competitionListFilter{}, false
	}
	if len(filter.GameID) > 64 || len(filter.Query) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_filter", "Competition filters are too long.")
		return competitionListFilter{}, false
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		filter.Cursor = &competitionCursor{}
		if !decodeCompetitionCursor(raw, filter.Cursor) || !uuidPattern.MatchString(filter.Cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid.")
			return competitionListFilter{}, false
		}
	}
	return filter, true
}

func parseCompetitionLimit(w http.ResponseWriter, r *http.Request, fallback, maximum int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		writeError(w, http.StatusBadRequest, "invalid_limit", "Limit is outside the allowed range.")
		return 0, false
	}
	return value, true
}

func (s *Server) loadCompetitionPage(ctx context.Context, filter competitionListFilter) (competitionPage, error) {
	if time.Now().UnixNano() >= s.readerUnavailableUntil.Load() {
		if lag, err := s.db.ReaderLag(ctx); err == nil && lag <= s.config.DatabaseMaxReplicaLag {
			if page, err := queryCompetitionPage(ctx, s.db.Reader, filter); err == nil {
				return page, nil
			} else {
				s.logger.Warn("reader query failed; using writer circuit", "operation", "list_competitions", "error", err)
			}
		}
		s.readerUnavailableUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
	}
	if err := s.acquireCompetitionWriterFallback(ctx); err != nil {
		return competitionPage{}, err
	}
	defer func() { <-s.writerFallback }()
	return queryCompetitionPage(ctx, s.db.Writer, filter)
}

func queryCompetitionPage(ctx context.Context, pool *pgxpool.Pool, filter competitionListFilter) (competitionPage, error) {
	statuses := publicCompetitionStatuses
	if filter.Status != "" {
		statuses = []string{filter.Status}
	}
	var cursorTime *time.Time
	var cursorID *string
	if filter.Cursor != nil {
		cursorTime, cursorID = &filter.Cursor.StartsAt, &filter.Cursor.ID
	}
	rows, err := pool.Query(ctx, `SELECT competition.id,competition.slug,competition.name,competition.description,
		competition.game_id,game.name,organization.id,organization.name,organization.slug,
		competition.format,competition.status,competition.max_entries,
		(SELECT count(*) FROM competition_entries entry WHERE entry.competition_id=competition.id
		 AND entry.status NOT IN ('withdrawn','disqualified')) AS entry_count,
		competition.entry_fee_minor,competition.fee_purpose,competition.currency,
		competition.prize_amount_minor,competition.prize_funding,competition.registration_opens_at,
		competition.registration_closes_at,competition.check_in_opens_at,competition.starts_at,competition.rules_version
		FROM competitions competition JOIN games game ON game.id=competition.game_id AND game.active=true
		JOIN organizations organization ON organization.id=competition.organization_id AND organization.status='active'
		WHERE competition.status=ANY($1::text[]) AND ($2='' OR competition.game_id=$2)
		AND ($3='' OR ($3='free' AND competition.entry_fee_minor=0) OR ($3='paid' AND competition.entry_fee_minor>0))
		AND ($4='' OR lower(competition.name) LIKE '%'||lower($4)||'%' OR lower(organization.name) LIKE '%'||lower($4)||'%')
		AND ($5::timestamptz IS NULL OR (competition.starts_at,competition.id)>($5,$6::uuid))
		ORDER BY competition.starts_at,competition.id LIMIT $7`, statuses, filter.GameID, filter.EntryType,
		filter.Query, cursorTime, cursorID, filter.Limit+1)
	if err != nil {
		return competitionPage{}, err
	}
	defer rows.Close()
	items := make([]competitionSummary, 0, filter.Limit)
	for rows.Next() {
		var item competitionSummary
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.GameID, &item.GameName,
			&item.OrganizerID, &item.OrganizerName, &item.OrganizerSlug, &item.Format, &item.Status, &item.MaxEntries,
			&item.EntryCount, &item.EntryFeeMinor, &item.FeePurpose, &item.Currency, &item.PrizeAmountMinor,
			&item.PrizeFunding, &item.RegistrationOpensAt, &item.RegistrationClosesAt, &item.CheckInOpensAt,
			&item.StartsAt, &item.RulesVersion); err != nil {
			return competitionPage{}, err
		}
		finalizeCompetitionSummary(&item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return competitionPage{}, err
	}
	page := competitionPage{Data: items}
	if len(page.Data) > filter.Limit {
		page.Data = page.Data[:filter.Limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next := encodeCompetitionCursor(competitionCursor{StartsAt: last.StartsAt, ID: last.ID})
		page.Page.NextCursor = &next
	}
	return page, nil
}

func (s *Server) loadCompetitionDetail(ctx context.Context, id string) (competitionDetail, error) {
	query := func(pool *pgxpool.Pool) (competitionDetail, error) {
		var detail competitionDetail
		err := pool.QueryRow(ctx, `SELECT competition.id,competition.slug,competition.name,competition.description,
			competition.game_id,game.name,organization.id,organization.name,organization.slug,
			competition.format,competition.status,competition.max_entries,
			(SELECT count(*) FROM competition_entries entry WHERE entry.competition_id=competition.id
			 AND entry.status NOT IN ('withdrawn','disqualified')),
			competition.entry_fee_minor,competition.fee_purpose,competition.currency,
			competition.prize_amount_minor,competition.prize_funding,competition.registration_opens_at,
			competition.registration_closes_at,competition.check_in_opens_at,competition.starts_at,
			competition.rules_version,competition.rules_snapshot
			FROM competitions competition JOIN games game ON game.id=competition.game_id AND game.active=true
			JOIN organizations organization ON organization.id=competition.organization_id AND organization.status='active'
			WHERE competition.id=$1 AND `+readableCompetitionSQL, id).
			Scan(&detail.ID, &detail.Slug, &detail.Name, &detail.Description, &detail.GameID, &detail.GameName,
				&detail.OrganizerID, &detail.OrganizerName, &detail.OrganizerSlug, &detail.Format, &detail.Status,
				&detail.MaxEntries, &detail.EntryCount, &detail.EntryFeeMinor, &detail.FeePurpose, &detail.Currency,
				&detail.PrizeAmountMinor, &detail.PrizeFunding, &detail.RegistrationOpensAt,
				&detail.RegistrationClosesAt, &detail.CheckInOpensAt, &detail.StartsAt, &detail.RulesVersion, &detail.Rules)
		if err == nil {
			finalizeCompetitionSummary(&detail.competitionSummary)
		}
		return detail, err
	}
	return readPublicCompetition(ctx, s, query)
}

func (s *Server) loadCompetitionBracket(ctx context.Context, id string) ([]bracketStage, error) {
	return readPublicCompetition(ctx, s, func(pool *pgxpool.Pool) ([]bracketStage, error) {
		return queryCompetitionBracket(ctx, pool, id)
	})
}

// readPublicCompetition runs an addressable public read on the replica while
// its lag is within budget, and otherwise on the writer behind the bounded
// fallback. pgx.ErrNoRows is an answer, not a replica fault, so it is returned
// as it is rather than retried on the writer.
func readPublicCompetition[T any](ctx context.Context, s *Server, query func(*pgxpool.Pool) (T, error)) (T, error) {
	if time.Now().UnixNano() >= s.readerUnavailableUntil.Load() {
		if lag, err := s.db.ReaderLag(ctx); err == nil && lag <= s.config.DatabaseMaxReplicaLag {
			if value, err := query(s.db.Reader); err == nil || errors.Is(err, pgx.ErrNoRows) {
				return value, err
			}
		}
		s.readerUnavailableUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
	}
	if err := s.acquireCompetitionWriterFallback(ctx); err != nil {
		var zero T
		return zero, err
	}
	defer func() { <-s.writerFallback }()
	return query(s.db.Writer)
}

// competitionIsReadable applies readableCompetitionSQL, so every public read
// below the detail resolves the same competitions as the detail itself.
func competitionIsReadable(ctx context.Context, queryer eligibilityQueryer, id string) (bool, error) {
	var readable bool
	err := queryer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competitions competition
		WHERE competition.id=$1 AND `+readableCompetitionSQL+`)`, id).Scan(&readable)
	return readable, err
}

func queryCompetitionBracket(ctx context.Context, pool *pgxpool.Pool, id string) ([]bracketStage, error) {
	readable, err := competitionIsReadable(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if !readable {
		return nil, pgx.ErrNoRows
	}
	rows, err := pool.Query(ctx, `SELECT
		stage.id::text,stage.name,stage.position,stage.format,stage.best_of,stage.status,
		match.id::text,match.bracket,match.round_number,match.match_number,match.state,
		match.graph_rank,match.activation_rule,match.activation_source_match_id::text,match.completion_reason,
		home.id::text,home.display_name,home.seed,
		away.id::text,away.display_name,away.seed,match.winner_entry_id::text,
		match.scheduled_at,match.result_due_at,match.completed_at,match.version,
		confirmed.id::text,confirmed.home_score,confirmed.away_score,confirmed.tiebreak_type,
		confirmed.home_tiebreak_score,confirmed.away_tiebreak_score,confirmed.decided_at,
		home_slot.source_kind,home_slot.source_entry_id::text,home_slot.source_match_id::text,
		home_slot.source_rank,home_slot.resolved_entry_id::text,home_slot.resolved_at,home_slot.voided_at,
		away_slot.source_kind,away_slot.source_entry_id::text,away_slot.source_match_id::text,
		away_slot.source_rank,away_slot.resolved_entry_id::text,away_slot.resolved_at,away_slot.voided_at
	FROM competition_stages stage
	LEFT JOIN matches match ON match.stage_id=stage.id
	LEFT JOIN match_slots home_slot ON home_slot.match_id=match.id AND home_slot.slot='home'
	LEFT JOIN match_slots away_slot ON away_slot.match_id=match.id AND away_slot.slot='away'
	LEFT JOIN competition_entries home ON home.id=COALESCE(match.home_entry_id,home_slot.resolved_entry_id)
	LEFT JOIN competition_entries away ON away.id=COALESCE(match.away_entry_id,away_slot.resolved_entry_id)
	LEFT JOIN LATERAL (
		SELECT submission.id,submission.home_score,submission.away_score,submission.tiebreak_type,
			submission.home_tiebreak_score,submission.away_tiebreak_score,submission.decided_at
		FROM result_submissions submission
		WHERE submission.match_id=match.id AND submission.status='confirmed'
		ORDER BY submission.decided_at DESC NULLS LAST,submission.submitted_at DESC,submission.id DESC
		LIMIT 1
	) confirmed ON true
	WHERE stage.competition_id=$1
	ORDER BY stage.position,match.bracket,match.round_number,match.match_number
	LIMIT 1000`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := make([]bracketStage, 0)
	stageIndex := make(map[string]int)
	roundIndex := make(map[string]int)
	for rows.Next() {
		var row bracketQueryRow
		if err := rows.Scan(
			&row.StageID, &row.StageName, &row.StagePosition, &row.StageFormat, &row.StageBestOf, &row.StageStatus,
			&row.MatchID, &row.Bracket, &row.RoundNumber, &row.MatchNumber, &row.MatchState,
			&row.GraphRank, &row.ActivationRule, &row.ActivationSourceMatchID, &row.CompletionReason,
			&row.HomeEntryID, &row.HomeDisplayName, &row.HomeSeed,
			&row.AwayEntryID, &row.AwayDisplayName, &row.AwaySeed, &row.WinnerEntryID,
			&row.ScheduledAt, &row.ResultDueAt, &row.CompletedAt, &row.Version,
			&row.ScoreSubmissionID, &row.HomeScore, &row.AwayScore, &row.TiebreakType,
			&row.HomeTiebreakScore, &row.AwayTiebreakScore, &row.ScoreConfirmedAt,
			&row.HomeSource.SourceKind, &row.HomeSource.SourceEntryID, &row.HomeSource.SourceMatchID,
			&row.HomeSource.SourceGraphRank, &row.HomeSource.ResolvedEntryID, &row.HomeSource.ResolvedAt, &row.HomeSource.VoidedAt,
			&row.AwaySource.SourceKind, &row.AwaySource.SourceEntryID, &row.AwaySource.SourceMatchID,
			&row.AwaySource.SourceGraphRank, &row.AwaySource.ResolvedEntryID, &row.AwaySource.ResolvedAt, &row.AwaySource.VoidedAt,
		); err != nil {
			return nil, err
		}
		appendBracketRow(&stages, stageIndex, roundIndex, row)
	}
	return stages, rows.Err()
}

func appendBracketRow(stages *[]bracketStage, stageIndex, roundIndex map[string]int, row bracketQueryRow) {
	stagePosition, exists := stageIndex[row.StageID]
	if !exists {
		*stages = append(*stages, bracketStage{
			ID: row.StageID, Name: row.StageName, Position: row.StagePosition,
			Format: row.StageFormat, BestOf: row.StageBestOf, Status: row.StageStatus,
			Rounds: []bracketRound{},
		})
		stagePosition = len(*stages) - 1
		stageIndex[row.StageID] = stagePosition
	}
	if row.MatchID == nil || row.Bracket == nil || row.RoundNumber == nil || row.MatchNumber == nil ||
		row.MatchState == nil || row.GraphRank == nil || row.ActivationRule == nil || row.Version == nil {
		return
	}
	roundKey := row.StageID + "\x00" + *row.Bracket + "\x00" + strconv.Itoa(*row.RoundNumber)
	roundPosition, exists := roundIndex[roundKey]
	if !exists {
		(*stages)[stagePosition].Rounds = append((*stages)[stagePosition].Rounds, bracketRound{
			Number: *row.RoundNumber, Name: bracketRoundName(*row.Bracket, *row.RoundNumber),
			Bracket: *row.Bracket, Matches: []bracketMatch{},
		})
		roundPosition = len((*stages)[stagePosition].Rounds) - 1
		roundIndex[roundKey] = roundPosition
	}
	match := bracketMatch{
		ID: *row.MatchID, Code: fmt.Sprintf("R%02d-M%02d", *row.RoundNumber, *row.MatchNumber),
		MatchNumber: *row.MatchNumber, State: *row.MatchState,
		Home:          bracketSlot{Side: "home", Participant: bracketParticipantFromRow(row.HomeEntryID, row.HomeDisplayName, row.HomeSeed), Source: bracketSourceFromRecord(row.HomeSource)},
		Away:          bracketSlot{Side: "away", Participant: bracketParticipantFromRow(row.AwayEntryID, row.AwayDisplayName, row.AwaySeed), Source: bracketSourceFromRecord(row.AwaySource)},
		WinnerEntryID: row.WinnerEntryID, ScheduledAt: utcTime(row.ScheduledAt), ResultDueAt: utcTime(row.ResultDueAt),
		CompletedAt: utcTime(row.CompletedAt), Version: *row.Version,
		Progression: bracketProgression{
			GraphRank: *row.GraphRank, ActivationRule: *row.ActivationRule,
			ActivationSourceMatchID: row.ActivationSourceMatchID, CompletionReason: row.CompletionReason,
		},
	}
	if row.ScoreSubmissionID != nil && row.HomeScore != nil && row.AwayScore != nil && row.ScoreConfirmedAt != nil {
		match.Score = &bracketScore{
			SubmissionID: *row.ScoreSubmissionID, HomeScore: *row.HomeScore, AwayScore: *row.AwayScore,
			ConfirmedAt: row.ScoreConfirmedAt.UTC(),
		}
		if row.TiebreakType != nil && row.HomeTiebreakScore != nil && row.AwayTiebreakScore != nil {
			match.Score.Tiebreak = &tiebreakScoreInput{
				Type: *row.TiebreakType, HomeScore: *row.HomeTiebreakScore, AwayScore: *row.AwayTiebreakScore,
			}
		}
	}
	(*stages)[stagePosition].Rounds[roundPosition].Matches = append(
		(*stages)[stagePosition].Rounds[roundPosition].Matches, match,
	)
}

func bracketParticipantFromRow(entryID, displayName *string, seed *int) *bracketParticipant {
	if entryID == nil || displayName == nil {
		return nil
	}
	return &bracketParticipant{EntryID: *entryID, DisplayName: *displayName, Seed: seed}
}

func bracketSourceFromRecord(record bracketSlotRecord) *bracketSlotSource {
	if record.SourceKind == nil {
		return nil
	}
	return &bracketSlotSource{
		Kind: *record.SourceKind, EntryID: record.SourceEntryID, MatchID: record.SourceMatchID,
		SourceGraphRank: record.SourceGraphRank, ResolvedEntryID: record.ResolvedEntryID,
		ResolvedAt: utcTime(record.ResolvedAt), VoidedAt: utcTime(record.VoidedAt),
	}
}

func bracketRoundName(bracket string, number int) string {
	switch bracket {
	case "winners":
		return fmt.Sprintf("Winners round %d", number)
	case "losers":
		return fmt.Sprintf("Losers round %d", number)
	case "grand_final":
		if number == 2 {
			return "Grand final reset"
		}
		return "Grand final"
	case "bronze":
		return "Third-place match"
	default:
		if strings.HasPrefix(bracket, "group_") {
			return fmt.Sprintf("Group %s matchday %d", strings.TrimPrefix(bracket, "group_"), number)
		}
		return fmt.Sprintf("Round %d", number)
	}
}

func (s *Server) acquireCompetitionWriterFallback(ctx context.Context) error {
	select {
	case s.writerFallback <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errWriterFallbackBusy
	}
}

func recordRegistrationIdempotency(ctx context.Context, tx pgx.Tx, scope, key, requestHash string, status int, body []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(scope,key,request_hash,response_status,response_body,expires_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,now()+interval '24 hours')
		ON CONFLICT (scope,key) DO UPDATE SET response_status=EXCLUDED.response_status,
		response_body=EXCLUDED.response_body,expires_at=EXCLUDED.expires_at
		WHERE idempotency_keys.request_hash=EXCLUDED.request_hash`, scope, key, requestHash, status, body)
	return err
}

func finalizeCompetitionSummary(item *competitionSummary) {
	item.AvailableSlots = max(0, item.MaxEntries-item.EntryCount)
	if item.EntryFeeMinor > 0 {
		item.EntryType = "paid"
	} else {
		item.EntryType = "free"
	}
}

func writeCompetitionCacheResponse(w http.ResponseWriter, r *http.Request, response gamicscache.Response, cacheControl string) {
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", response.ETag)
	w.Header().Set("X-Cache", string(response.State))
	if etagMatches(r.Header.Get("If-None-Match"), response.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response.Body)
}

func encodeCompetitionCursor(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCompetitionCursor(raw string, destination any) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil && len(decoded) <= 512 && json.Unmarshal(decoded, destination) == nil
}

func containsCompetitionStatus(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
