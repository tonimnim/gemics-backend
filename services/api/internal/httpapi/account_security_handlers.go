package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const accountDeletionCoolingOffPeriod = 24 * time.Hour

type sessionView struct {
	ID         string     `json:"id"`
	DeviceName string     `json:"deviceName"`
	UserAgent  string     `json:"userAgent"`
	CreatedIP  string     `json:"createdIp"`
	LastUsedIP string     `json:"lastUsedIp"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt time.Time  `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	Current    bool       `json:"current"`
}

type accountDeletionView struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	RequestedAt  time.Time  `json:"requestedAt"`
	ExecuteAfter time.Time  `json:"executeAfter"`
	CancelledAt  *time.Time `json:"cancelledAt"`
	CompletedAt  *time.Time `json:"completedAt"`
}

type executeAccountDeletionInput struct {
	RequestID    string `json:"requestId"`
	Confirmation string `json:"confirmation"`
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	current := identityFromContext(r.Context())
	rows, err := s.db.Writer.Query(r.Context(), `SELECT id,device_name,user_agent,created_ip,last_used_ip,
		created_at,last_used_at,revoked_at FROM refresh_sessions
		WHERE user_id=$1 ORDER BY (revoked_at IS NULL) DESC,last_used_at DESC,id DESC LIMIT 100`, current.UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load sessions.")
		return
	}
	defer rows.Close()
	data := []sessionView{}
	for rows.Next() {
		var item sessionView
		if err := rows.Scan(&item.ID, &item.DeviceName, &item.UserAgent, &item.CreatedIP, &item.LastUsedIP,
			&item.CreatedAt, &item.LastUsedAt, &item.RevokedAt); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load sessions.")
			return
		}
		item.Current = item.ID == current.SessionID
		data = append(data, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load sessions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(sessionID) {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "Session ID is invalid.")
		return
	}
	current := identityFromContext(r.Context())
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke this session.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	command, err := tx.Exec(r.Context(), `UPDATE refresh_sessions
		SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND user_id=$2`, sessionID, current.UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke this session.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "session_not_found", "Session not found.")
		return
	}
	// A lost phone signed out from another device stops receiving pushes too.
	if err = revokeSessionPushTokens(r.Context(), tx, []string{sessionID}); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke this session.")
		return
	}
	if err := s.propagateSessionRevocations(r.Context(), []string{sessionID}); err != nil {
		s.logger.Warn("cache session revocation", "session_id", sessionID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "revocation_propagation_failed", "The session was revoked, but propagation is still completing. Please retry.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) propagateSessionRevocations(ctx context.Context, sessionIDs []string) error {
	if s.redis == nil || len(sessionIDs) == 0 {
		return nil
	}
	ttl := max(time.Second, s.config.AccessTokenTTL)
	pipe := s.redis.Pipeline()
	for _, sessionID := range sessionIDs {
		pipe.Set(ctx, s.securityKey("auth:session:"+sessionID), "revoked", ttl)
		pipe.Set(ctx, "auth:revoked:"+sessionID, "1", ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *Server) getAccountDeletionRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	result, err := loadLatestAccountDeletion(r.Context(), s.db.Writer, identityFromContext(r.Context()).UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"data": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the deletion request.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) requestAccountDeletion(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	var result accountDeletionView
	err := s.db.Writer.QueryRow(r.Context(), `INSERT INTO account_deletion_requests
		(user_id,execute_after,request_ip) VALUES ($1,now()+$2::interval,$3)
		ON CONFLICT (user_id) WHERE status='pending' DO UPDATE SET user_id=EXCLUDED.user_id
		RETURNING id,status,requested_at,execute_after,cancelled_at,completed_at`,
		userID, accountDeletionCoolingOffPeriod.String(), s.clientIP(r)).
		Scan(&result.ID, &result.Status, &result.RequestedAt, &result.ExecuteAfter, &result.CancelledAt, &result.CompletedAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request account deletion.")
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) cancelAccountDeletion(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	command, err := s.db.Writer.Exec(r.Context(), `UPDATE account_deletion_requests
		SET status='cancelled',cancelled_at=now() WHERE user_id=$1 AND status='pending'`,
		identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to cancel account deletion.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "deletion_request_not_found", "No pending deletion request was found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) executeAccountDeletion(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input executeAccountDeletionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if !validAccountDeletionConfirmation(input.RequestID, input.Confirmation) {
		writeError(w, http.StatusBadRequest, "invalid_deletion_confirmation", "Provide the pending request ID and the exact confirmation DELETE.")
		return
	}
	current := identityFromContext(r.Context())
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	var executeAfter time.Time
	var ready bool
	err = tx.QueryRow(r.Context(), `SELECT execute_after,execute_after<=now()
		FROM account_deletion_requests WHERE id=$1 AND user_id=$2 AND status='pending' FOR UPDATE`,
		input.RequestID, current.UserID).Scan(&executeAfter, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deletion_request_not_found", "No pending deletion request was found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}
	if !ready {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "deletion_not_ready", "message": "The account deletion cooling-off period has not ended.",
			"executeAfter": executeAfter,
		})
		return
	}
	var ownsOrganization bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_members
		WHERE user_id=$1 AND role='owner')`, current.UserID).Scan(&ownsOrganization); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify account ownership.")
		return
	}
	if ownsOrganization {
		writeError(w, http.StatusConflict, "organization_ownership_transfer_required", "Transfer or close owned organizations before deleting this account.")
		return
	}
	var hasActiveCompetition bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(
		SELECT 1 FROM competition_entries entry
		JOIN competitions competition ON competition.id=entry.competition_id
		WHERE (entry.status='withdrawal_pending' OR (
			entry.status IN ('registered','checked_in','accepted')
			AND competition.status NOT IN ('completed','cancelled')
		  ))
		  AND (entry.captain_user_id=$1 OR EXISTS(
			SELECT 1 FROM entry_members member WHERE member.entry_id=entry.id AND member.user_id=$1
		  )))`, current.UserID).Scan(&hasActiveCompetition); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify competition participation.")
		return
	}
	if hasActiveCompetition {
		writeError(w, http.StatusConflict, "active_competition_blocks_deletion", "Withdraw from or finish active competitions before deleting this account.")
		return
	}
	var hasUnsettledPayment bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM payment_intents
		WHERE user_id=$1 AND status IN ('initiating','pending','callback_received','review'))`, current.UserID).
		Scan(&hasUnsettledPayment); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify pending payments.")
		return
	}
	if hasUnsettledPayment {
		writeError(w, http.StatusConflict, "unsettled_payment_blocks_deletion", "Wait for pending M-Pesa payments to settle before deleting this account.")
		return
	}

	var locked bool
	if err = tx.QueryRow(r.Context(), `SELECT true FROM users WHERE id=$1 AND status='active' FOR UPDATE`, current.UserID).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "account_unavailable", "This account can no longer be deleted.")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM email_otp_challenges WHERE user_id=$1`, current.UserID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}

	suffix := strings.ReplaceAll(current.UserID, "-", "")
	if len(suffix) > 16 {
		suffix = suffix[:16]
	}
	syntheticEmail := "deleted+" + suffix + "@deleted.invalid"
	syntheticHandle := "deleted_" + suffix
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM notifications WHERE user_id=$1`, []any{current.UserID}},
		{`DELETE FROM notification_preferences WHERE user_id=$1`, []any{current.UserID}},
		{`DELETE FROM organization_members WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE platform_staff_roles SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE legal_acceptances SET accepted_ip='',user_agent='' WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE game_account_verification_requests SET
			status='withdrawn',
			publisher_verified=false,player_note='',decision_reason='',updated_at=now() WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE player_profiles SET handle=$2,bio='',avatar_object_key=NULL,discoverable=false,
			analytics_consent_at=NULL,scouting_consent_at=NULL,updated_at=now() WHERE user_id=$1`, []any{current.UserID, syntheticHandle}},
		{`UPDATE game_accounts SET in_game_name='Deleted player',publisher_player_id=NULL,
			verification_status='unverified',verified_at=NULL,verification_method=NULL,publisher_verified=false,
			updated_at=now() WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE competition_entries SET display_name='Deleted player',updated_at=now() WHERE captain_user_id=$1`, []any{current.UserID}},
		{`UPDATE payment_intents SET entry_display_name='Deleted player',updated_at=now() WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE payment_refunds SET player_note='',updated_at=now() WHERE user_id=$1`, []any{current.UserID}},
		{`UPDATE users SET email=$2,email_verified_at=NULL,phone_e164=NULL,display_name='Deleted player',country_code='ZZ',country_chosen_at=NULL,
			birth_date=NULL,password_hash=NULL,password_changed_at=NULL,registration_ip=NULL,
			status='deleted',terms_accepted_at=NULL,privacy_accepted_at=NULL,updated_at=now() WHERE id=$1`, []any{current.UserID, syntheticEmail}},
	}
	for _, statement := range statements {
		if _, err = tx.Exec(r.Context(), statement.query, statement.args...); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
			return
		}
	}

	rows, err := tx.Query(r.Context(), `UPDATE refresh_sessions SET revoked_at=COALESCE(revoked_at,now()),
		device_name='',user_agent='',created_ip='',last_used_ip='',previous_token_hash=NULL,previous_token_valid_until=NULL
		WHERE user_id=$1 RETURNING id`, current.UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}
	sessionIDs := []string{}
	for rows.Next() {
		var sessionID string
		if err = rows.Scan(&sessionID); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
			return
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		// Installations are deleted once every session has ended. A registration
		// locks the player row before its session row, so one that started first
		// has committed by now and one that started later waits for this
		// transaction and then finds no active player: none outlives the account.
		_, err = tx.Exec(r.Context(), `DELETE FROM push_tokens WHERE user_id=$1`, current.UserID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE account_deletion_requests
			SET status='completed',completed_at=now() WHERE id=$1`, input.RequestID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events
			(actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1,'account.deleted','user',$1::text,$2,'{"status":"deleted","pii":"anonymized"}'::jsonb)`,
			current.UserID, r.Header.Get("X-Request-ID"))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events
			(aggregate_type,aggregate_id,event_type,payload)
			VALUES ('user',$1::text,'account.deleted',jsonb_build_object('userId',$1::text))`, current.UserID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to delete this account.")
		return
	}
	if err := s.propagateSessionRevocations(r.Context(), sessionIDs); err != nil {
		s.logger.Warn("account deleted but cache revocation propagation failed", "user_id", current.UserID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func validAccountDeletionConfirmation(requestID, confirmation string) bool {
	return uuidPattern.MatchString(strings.TrimSpace(requestID)) && confirmation == "DELETE"
}

func loadLatestAccountDeletion(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID string) (accountDeletionView, error) {
	var result accountDeletionView
	err := queryer.QueryRow(ctx, `SELECT id,status,requested_at,execute_after,cancelled_at,completed_at
		FROM account_deletion_requests WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, userID).
		Scan(&result.ID, &result.Status, &result.RequestedAt, &result.ExecuteAfter, &result.CancelledAt, &result.CompletedAt)
	return result, err
}
