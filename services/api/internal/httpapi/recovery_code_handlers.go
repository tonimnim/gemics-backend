package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Players sign up without an email, so a forgotten password would otherwise
// lose the account. Sign-up hands out a recovery code. With it and the Konami
// ID a player sets a new password, every other device is signed out, and a
// fresh code replaces the spent one. A signed-in player can replace the code
// at any time after confirming their password.

type recoveryCodeResetInput struct {
	KonamiID     string `json:"konamiId"`
	RecoveryCode string `json:"recoveryCode"`
	NewPassword  string `json:"newPassword"`
	DeviceName   string `json:"deviceName"`
}

type recoveryCodeCreateInput struct {
	Password string `json:"password"`
}

// resetPasswordWithRecoveryCode answers the same for an unknown Konami ID, an
// account without a code and a wrong code, and every miss counts toward the
// sign-in lockout for that Konami ID and network, so codes can't be guessed.
func (s *Server) resetPasswordWithRecoveryCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input recoveryCodeResetInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.DeviceName) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_device_name", "Device name cannot exceed 120 characters.")
		return
	}
	_, key, konamiOK := normalizeKonamiID(input.KonamiID)
	codeHash, codeOK := gamicsauth.HashRecoveryCode(input.RecoveryCode)
	if !konamiOK || !codeOK {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_code", "The Konami ID or recovery code is incorrect.")
		return
	}
	ip := s.clientIP(r)
	allowed, err := s.loginAllowed(r.Context(), key, ip)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.LoginFailureWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
		return
	}

	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var userID, username string
	var storedHash []byte
	err = tx.QueryRow(r.Context(), `SELECT player.id::text,COALESCE(profile.handle,''),player.recovery_code_hash
		FROM game_accounts account JOIN users player ON player.id=account.user_id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE account.game_id=$1 AND account.publisher_player_id IS NOT NULL
		AND `+konamiKeyExpression+`=$2 AND player.status='active'
		FOR NO KEY UPDATE OF player`, registrationGameID, key).Scan(&userID, &username, &storedHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if len(storedHash) == 0 || subtle.ConstantTimeCompare(storedHash, codeHash) != 1 {
		tx.Rollback(r.Context()) //nolint:errcheck
		s.recordLoginFailure(r.Context(), key, ip)
		writeError(w, http.StatusUnauthorized, "invalid_recovery_code", "The Konami ID or recovery code is incorrect.")
		return
	}
	// The new password is checked before the code is spent, so a rejected
	// password does not burn the code.
	if !validNewPassword(w, input.NewPassword, username, key) {
		return
	}
	nextCode, nextHash, err := gamicsauth.NewRecoveryCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to reset the password.")
		return
	}
	revoked, err := s.setPassword(r.Context(), tx, userID, input.NewPassword, "")
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE users SET recovery_code_hash=$2,recovery_code_created_at=now() WHERE id=$1`,
			userID, nextHash)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM login_failures WHERE konami_key=$1`, loginFailureKey(key))
	}
	var sessionID, refreshToken string
	if err == nil {
		sessionID, refreshToken, err = s.createSession(r.Context(), tx, r, userID, input.DeviceName)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if err := s.propagateSessionRevocations(r.Context(), revoked); err != nil {
		s.logger.Warn("password reset with recovery code but session revocation propagation failed", "user_id", userID, "error", err)
	}
	s.markRecentWrite(r.Context(), userID)
	s.writeSessionResponseWith(w, r, http.StatusOK, userID, sessionID, refreshToken,
		map[string]any{"recoveryCode": nextCode})
}

// createRecoveryCode replaces the signed-in player's recovery code. The
// password is asked for, and wrong guesses count like failed sign-ins, so a
// stolen session cannot quietly take over account recovery.
func (s *Server) createRecoveryCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input recoveryCodeCreateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	current := identityFromContext(r.Context())
	failureKey := "user:" + current.UserID
	ip := s.clientIP(r)
	allowed, err := s.loginAllowed(r.Context(), failureKey, ip)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a recovery code.")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.LoginFailureWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a recovery code.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var passwordHash *string
	err = tx.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE`,
		current.UserID).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session is no longer valid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a recovery code.")
		return
	}
	if !gamicsauth.VerifyPassword(valueOrEmpty(passwordHash), input.Password) {
		tx.Rollback(r.Context()) //nolint:errcheck
		s.recordLoginFailure(r.Context(), failureKey, ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The password is incorrect.")
		return
	}
	code, hash, err := gamicsauth.NewRecoveryCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create a recovery code.")
		return
	}
	var createdAt time.Time
	if err = tx.QueryRow(r.Context(), `UPDATE users SET recovery_code_hash=$2,recovery_code_created_at=now(),updated_at=now()
		WHERE id=$1 RETURNING recovery_code_created_at`, current.UserID, hash).Scan(&createdAt); err != nil ||
		tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a recovery code.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCode": code, "createdAt": createdAt.UTC()})
}
