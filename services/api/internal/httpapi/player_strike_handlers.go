package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	playerStrikeCursorKind     = "player-strike-feed"
	playerStrikeRevokeMinRunes = 10
	playerStrikeRevokeMaxRunes = 500
)

// playerStrikeView is one conduct strike. Strikes stay active until an admin
// revokes them; active strikes count towards the registration ban.
type playerStrikeView struct {
	ID            string     `json:"id"`
	UserID        string     `json:"userId"`
	MatchID       string     `json:"matchId"`
	ReviewID      string     `json:"reviewId"`
	ReasonCode    string     `json:"reasonCode"`
	Note          string     `json:"note"`
	CreatedByKind string     `json:"createdByKind"`
	CreatedBy     *string    `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	RevokedAt     *time.Time `json:"revokedAt"`
	RevokedBy     *string    `json:"revokedBy"`
	RevokeReason  *string    `json:"revokeReason"`
}

type playerStrikeRevocationInput struct {
	Reason string `json:"reason"`
}

func playerStrikeRevocationScope(actorID, strikeID string) string {
	return "strike-revoke:" + actorID + ":" + strikeID
}

const playerStrikeColumns = `strike.id::text,strike.user_id::text,strike.match_id::text,strike.review_id::text,
	strike.reason_code,strike.note,strike.created_by_kind,strike.created_by::text,strike.created_at,strike.revoked_at,
	strike.revoked_by::text,strike.revoke_reason`

const playerStrikeSelect = `SELECT ` + playerStrikeColumns + ` FROM player_strikes strike `

func scanPlayerStrike(row accountScanner) (playerStrikeView, error) {
	var strike playerStrikeView
	err := row.Scan(&strike.ID, &strike.UserID, &strike.MatchID, &strike.ReviewID, &strike.ReasonCode, &strike.Note,
		&strike.CreatedByKind, &strike.CreatedBy, &strike.CreatedAt, &strike.RevokedAt, &strike.RevokedBy,
		&strike.RevokeReason)
	return strike, err
}

func queryPlayerStrikes(ctx context.Context, queryer rowsQueryer, query string, args ...any) ([]playerStrikeView, error) {
	rows, err := queryer.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	strikes := make([]playerStrikeView, 0)
	for rows.Next() {
		strike, scanErr := scanPlayerStrike(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		strikes = append(strikes, strike)
	}
	return strikes, rows.Err()
}

type playerStrikeFilters struct {
	UserID string
	Status string
}

// scope binds a cursor to every filter of the feed.
func (filters playerStrikeFilters) scope() string {
	return filters.UserID + "|" + filters.Status
}

// playerStrikePageInput validates the feed filters and a cursor bound to the
// operator and to every filter.
func (s *Server) playerStrikePageInput(w http.ResponseWriter, r *http.Request) (playerStrikeFilters, int, *publicCursor, bool) {
	filters := playerStrikeFilters{
		UserID: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("userId"))),
		Status: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))),
	}
	if filters.UserID != "" && !uuidPattern.MatchString(filters.UserID) {
		writeError(w, http.StatusBadRequest, "invalid_user", "Choose a valid user.")
		return playerStrikeFilters{}, 0, nil, false
	}
	if filters.Status == "" {
		filters.Status = "active"
	}
	if filters.Status != "active" && filters.Status != "revoked" && filters.Status != "all" {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose active, revoked or all.")
		return playerStrikeFilters{}, 0, nil, false
	}
	limit, cursor, ok := s.staffQueuePageInput(w, r, playerStrikeCursorKind, filters.scope())
	if !ok {
		return playerStrikeFilters{}, 0, nil, false
	}
	return filters, limit, cursor, true
}

// listPlayerStrikes is the Gamics strikes feed, newest first, optionally for
// one player. Like the review queue, it silently omits strikes from matches
// the operator plays in, captains in or organizes (D27), since a strike
// carries the decider's review note.
func (s *Server) listPlayerStrikes(w http.ResponseWriter, r *http.Request) {
	filters, limit, cursor, ok := s.playerStrikePageInput(w, r)
	if !ok {
		return
	}
	var userFilter any
	if filters.UserID != "" {
		userFilter = filters.UserID
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		beforeTime, beforeID = &value, &cursor.ID
	}
	viewerID := identityFromContext(r.Context()).UserID
	strikes, err := queryPlayerStrikes(r.Context(), s.db.Writer, playerStrikeSelect+`JOIN matches m ON m.id=strike.match_id
		WHERE ($1::uuid IS NULL OR strike.user_id=$1::uuid)
		AND ($2='all' OR ($2='active' AND strike.revoked_at IS NULL) OR ($2='revoked' AND strike.revoked_at IS NOT NULL))
		AND ($3::timestamptz IS NULL OR (strike.created_at,strike.id)<($3,$4::uuid))
		AND strike.user_id<>$6::uuid AND NOT `+resultReviewConflictClause("m", "$6::uuid")+`
		ORDER BY strike.created_at DESC,strike.id DESC LIMIT $5`,
		userFilter, filters.Status, beforeTime, beforeID, limit+1, viewerID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load player strikes.")
		return
	}
	page := staffQueuePage{}
	if len(strikes) > limit {
		strikes = strikes[:limit]
		page.HasMore = true
		last := strikes[len(strikes)-1]
		next, encodeErr := s.encodeStaffQueueCursor(playerStrikeCursorKind, filters.scope(),
			viewerID, last.CreatedAt, last.ID)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate player strikes.")
			return
		}
		page.NextCursor = &next
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": strikes, "page": page})
}

// revokePlayerStrike withdraws one strike (admin only). The strike stays in
// the feed as history but no longer counts towards the ban. An admin can
// never revoke their own strike or one from a match they play in, captain
// in or organize (D27).
func (s *Server) revokePlayerStrike(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	strikeID := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !uuidPattern.MatchString(strikeID) {
		writeError(w, http.StatusNotFound, "player_strike_not_found", "Player strike not found.")
		return
	}
	var input playerStrikeRevocationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if runes := utf8.RuneCountInString(input.Reason); runes < playerStrikeRevokeMinRunes || runes > playerStrikeRevokeMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_strike_revocation", "Explain the revocation in 10 to 500 characters.")
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to revoke the strike.")
		return
	}
	ctx := r.Context()
	actorID := identityFromContext(ctx).UserID
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke the strike.")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	scope := playerStrikeRevocationScope(actorID, strikeID)
	replay, err := beginIdempotentRequest(ctx, tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another strike revocation.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke the strike.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	current, err := scanPlayerStrike(tx.QueryRow(ctx, playerStrikeSelect+`WHERE strike.id=$1 FOR UPDATE`, strikeID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_strike_not_found", "Player strike not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the strike.")
		return
	}
	conflicted := current.UserID == actorID
	if !conflicted {
		conflicted, err = resultReviewConflicted(ctx, tx, current.MatchID, actorID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the strike.")
			return
		}
	}
	if conflicted {
		writeError(w, http.StatusForbidden, "result_review_conflict", "You can't revoke a strike from a match you play in or organize.")
		return
	}
	if current.RevokedAt != nil {
		writeError(w, http.StatusConflict, "strike_already_revoked", "This strike has already been revoked.")
		return
	}
	revoked, err := scanPlayerStrike(tx.QueryRow(ctx, `UPDATE player_strikes strike
		SET revoked_at=now(),revoked_by=$2,revoke_reason=$3
		WHERE strike.id=$1 AND strike.revoked_at IS NULL RETURNING `+playerStrikeColumns, strikeID, actorID, input.Reason))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "strike_already_revoked", "This strike has already been revoked.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke the strike.")
		return
	}
	if err = appendPlatformAudit(r, tx, actorID, "player.strike_revoked", "player_strike", strikeID,
		map[string]any{"status": "active"},
		map[string]any{"status": "revoked", "userId": revoked.UserID, "reason": input.Reason}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the strike revocation.")
		return
	}
	if err = insertProgressionOutbox(ctx, tx, "user", revoked.UserID, "player.strike_revoked", map[string]any{
		"strikeId": revoked.ID, "userId": revoked.UserID, "matchId": revoked.MatchID, "reviewId": revoked.ReviewID,
	}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the strike revocation.")
		return
	}
	body, err := json.Marshal(map[string]any{"data": revoked})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to revoke the strike.")
		return
	}
	if err = finishIdempotentRequest(ctx, tx, scope, idempotencyKey, http.StatusOK, body); err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke the strike.")
		return
	}
	writeResultRawJSON(w, http.StatusOK, body)
}
