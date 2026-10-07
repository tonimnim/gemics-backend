package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Contact details are added after registration: an email for communication
// and password recovery, and a phone number for payments. Neither is needed
// to register or sign in.

const (
	emailPurposeVerification  = "email_verification"
	emailPurposePasswordReset = "password_reset"
)

// internationalPhonePattern is E.164: a plus, a country code and up to 15
// digits in all. It is deliberately not tied to one country.
var internationalPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

var errEmailUnavailable = errors.New("email delivery unavailable")

type emailInput struct {
	Email string `json:"email"`
}

type emailCodeInput struct {
	Code string `json:"code"`
}

type phoneInput struct {
	PhoneNumber string `json:"phoneNumber"`
}

type passwordChangeInput struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type passwordResetRequestInput struct {
	KonamiID string `json:"konamiId"`
}

type passwordResetConfirmInput struct {
	KonamiID    string `json:"konamiId"`
	Code        string `json:"code"`
	NewPassword string `json:"newPassword"`
}

// normalizeInternationalPhone accepts common separators and a 00 prefix and
// returns E.164. A number without a country code is rejected rather than
// guessed, since players register from many countries.
func normalizeInternationalPhone(raw string) (string, bool) {
	value := strings.NewReplacer(" ", "", "-", "", ".", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	if rest, ok := strings.CutPrefix(value, "00"); ok {
		value = "+" + rest
	}
	return value, internationalPhonePattern.MatchString(value)
}

func (s *Server) requestEmailVerification(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input emailInput
	if !decodeJSON(w, r, &input) {
		return
	}
	email, ok := normalizeEmail(input.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_email", "Enter a valid email address.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	var current *string
	var taken bool
	if err := s.db.Writer.QueryRow(r.Context(), `SELECT
		(SELECT email FROM users WHERE id=$1),
		EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($2) AND id<>$1)`, userID, email).Scan(&current, &taken); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to add the email address.")
		return
	}
	if current != nil && strings.EqualFold(*current, email) {
		writeError(w, http.StatusConflict, "email_already_verified", "This email address is already verified on your account.")
		return
	}
	if taken {
		writeError(w, http.StatusConflict, "email_taken", "That email address is used by another Gamics account.")
		return
	}
	ip := s.clientIP(r)
	if !s.allowOTPRate(r.Context(), "email", email, s.config.OTPEmailLimit) || !s.allowOTPRate(r.Context(), "ip", ip, s.config.OTPIPLimit) {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.OTPRequestWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many codes requested. Try again later.")
		return
	}
	if err := s.issueEmailCode(r.Context(), userID, emailPurposeVerification, email, ip); err != nil {
		writeEmailCodeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "expiresInSeconds": int(s.config.OTPTTL.Seconds())})
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input emailCodeInput
	if !decodeJSON(w, r, &input) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the code.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	email, ok, err := s.consumeEmailCode(r.Context(), tx, userID, emailPurposeVerification, input.Code)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the code.")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE users SET email=$2,email_verified_at=now(),updated_at=now()
		WHERE id=$1 AND status='active'`, userID, email)
	if uniqueViolationOn(err, "users_email_unique") {
		writeError(w, http.StatusConflict, "email_taken", "That email address is used by another Gamics account.")
		return
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the code.")
		return
	}
	s.getMe(w, r)
}

func (s *Server) putPhone(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input phoneInput
	if !decodeJSON(w, r, &input) {
		return
	}
	phone, ok := normalizeInternationalPhone(input.PhoneNumber)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_phone",
			"Enter the phone number with its country code, for example +254712345678 or +919812345678.")
		return
	}
	_, err := s.db.Writer.Exec(r.Context(), `UPDATE users SET phone_e164=$2,updated_at=now()
		WHERE id=$1 AND status='active'`, identityFromContext(r.Context()).UserID, phone)
	if uniqueViolationOn(err, "users_phone_unique") {
		writeError(w, http.StatusConflict, "phone_taken",
			"That phone number is linked to another Gamics account. Contact Gamics support if it is yours.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the phone number.")
		return
	}
	s.getMe(w, r)
}

func (s *Server) deletePhone(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if _, err := s.db.Writer.Exec(r.Context(), `UPDATE users SET phone_e164=NULL,updated_at=now() WHERE id=$1`,
		identityFromContext(r.Context()).UserID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to remove the phone number.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// changePassword keeps the current session and signs every other device out.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input passwordChangeInput
	if !decodeJSON(w, r, &input) {
		return
	}
	current := identityFromContext(r.Context())
	// Guesses at the current password count like sign-in failures, so a
	// stolen session cannot brute-force its way to the password.
	failureKey := "user:" + current.UserID
	ip := s.clientIP(r)
	allowed, err := s.loginAllowed(r.Context(), failureKey, ip)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the password.")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.LoginFailureWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the password.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var username string
	var passwordHash, konamiID *string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(profile.handle,''),player.password_hash,
		(SELECT account.publisher_player_id FROM game_accounts account
		 WHERE account.user_id=player.id AND account.game_id=$2 AND account.publisher_player_id IS NOT NULL
		 ORDER BY account.created_at LIMIT 1)
		FROM users player LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1 AND player.status='active' FOR NO KEY UPDATE OF player`, current.UserID, registrationGameID).
		Scan(&username, &passwordHash, &konamiID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session is no longer valid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the password.")
		return
	}
	if !gamicsauth.VerifyPassword(valueOrEmpty(passwordHash), input.CurrentPassword) {
		tx.Rollback(r.Context()) //nolint:errcheck
		s.recordLoginFailure(r.Context(), failureKey, ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The current password is incorrect.")
		return
	}
	if !validNewPassword(w, input.NewPassword, username, konamiKey(valueOrEmpty(konamiID))) {
		return
	}
	sessionIDs, err := s.setPassword(r.Context(), tx, current.UserID, input.NewPassword, current.SessionID)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to change the password.")
		return
	}
	if err := s.propagateSessionRevocations(r.Context(), sessionIDs); err != nil {
		s.logger.Warn("password changed but session revocation propagation failed", "user_id", current.UserID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestPasswordReset emails a code to the account's verified address. It
// answers the same whether or not the Konami ID has an account or a verified
// email, so it does not reveal which Konami IDs are registered.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input passwordResetRequestInput
	if !decodeJSON(w, r, &input) {
		return
	}
	_, key, ok := normalizeKonamiID(input.KonamiID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the Konami ID shown in eFootball.")
		return
	}
	ip := s.clientIP(r)
	if !s.allowOTPRate(r.Context(), "ip", ip, s.config.OTPIPLimit) {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.OTPRequestWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many codes requested. Try again later.")
		return
	}
	accepted := map[string]any{"status": "accepted", "expiresInSeconds": int(s.config.OTPTTL.Seconds())}
	userID, email, err := s.lookupRecoveryEmail(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request a code.")
		return
	}
	if userID == "" || !s.allowOTPRate(r.Context(), "email", email, s.config.OTPEmailLimit) {
		writeJSON(w, http.StatusAccepted, accepted)
		return
	}
	if err := s.issueEmailCode(r.Context(), userID, emailPurposePasswordReset, email, ip); err != nil {
		s.logger.Error("send password reset code", "error", err)
	}
	writeJSON(w, http.StatusAccepted, accepted)
}

// confirmPasswordReset sets a new password and signs every device out.
func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input passwordResetConfirmInput
	if !decodeJSON(w, r, &input) {
		return
	}
	_, key, ok := normalizeKonamiID(input.KonamiID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
		return
	}
	userID, _, err := s.lookupRecoveryEmail(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var username string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(profile.handle,'') FROM users player
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1 AND player.status='active' FOR NO KEY UPDATE OF player`, userID).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	// The new password is checked before the code is spent, so a rejected
	// password does not burn the code.
	if !validNewPassword(w, input.NewPassword, username, key) {
		return
	}
	_, ok, err = s.consumeEmailCode(r.Context(), tx, userID, emailPurposePasswordReset, input.Code)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
		return
	}
	sessionIDs, err := s.setPassword(r.Context(), tx, userID, input.NewPassword, "")
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM login_failures WHERE konami_key=$1`, loginFailureKey(key))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reset the password.")
		return
	}
	if err := s.propagateSessionRevocations(r.Context(), sessionIDs); err != nil {
		s.logger.Warn("password reset but session revocation propagation failed", "user_id", userID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func validNewPassword(w http.ResponseWriter, password, username, konamiKeyValue string) bool {
	if !gamicsauth.ValidPasswordLength(password) {
		writeError(w, http.StatusBadRequest, "invalid_password", "Password must be 8-128 characters.")
		return false
	}
	if passwordRepeatsIdentity(password, username, konamiKeyValue) {
		writeError(w, http.StatusBadRequest, "weak_password", "Choose a password that is not your username or Konami ID.")
		return false
	}
	return true
}

// setPassword stores a new hash and revokes every session except keep, in
// the caller's transaction, which must already hold the player row. It
// returns the revoked session IDs for cache propagation after commit.
func (s *Server) setPassword(ctx context.Context, tx pgx.Tx, userID, password, keep string) ([]string, error) {
	passwordHash, err := gamicsauth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,password_changed_at=now(),updated_at=now() WHERE id=$1`,
		userID, passwordHash); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE refresh_sessions SET revoked_at=now()
		WHERE user_id=$1 AND revoked_at IS NULL AND id::text<>$2 RETURNING id::text`, userID, keep)
	if err != nil {
		return nil, err
	}
	sessionIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(sessionIDs) > 0 {
		err = revokeSessionPushTokens(ctx, tx, sessionIDs)
	}
	return sessionIDs, err
}

// lookupRecoveryEmail returns the active player and verified email for a
// Konami key, or an empty user ID when either is missing.
func (s *Server) lookupRecoveryEmail(ctx context.Context, key string) (string, string, error) {
	var userID string
	var email *string
	err := s.db.Writer.QueryRow(ctx, `SELECT player.id,player.email
		FROM game_accounts account JOIN users player ON player.id=account.user_id
		WHERE account.game_id=$1 AND account.publisher_player_id IS NOT NULL
		AND `+konamiKeyExpression+`=$2
		AND player.status='active' AND player.email_verified_at IS NOT NULL`, registrationGameID, key).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && email == nil) {
		return "", "", nil
	}
	return userID, valueOrEmpty(email), err
}

// issueEmailCode replaces any active code for the address and emails a new
// one. Only one code per address is live at a time.
func (s *Server) issueEmailCode(ctx context.Context, userID, purpose, email, ip string) error {
	if s.mailer == nil {
		return errEmailUnavailable
	}
	code, err := gamicsauth.GenerateOTP()
	if err != nil {
		return err
	}
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))", email); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE email_otp_challenges SET consumed_at=now()
		WHERE lower(email)=lower($1) AND consumed_at IS NULL`, email); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO email_otp_challenges(user_id,purpose,email,code_hash,request_ip,expires_at)
		VALUES ($1,$2,$3,$4,$5,now()+$6::interval)`, userID, purpose, email,
		gamicsauth.HashOTP(s.config.OTPHashSecret, email, code), ip, s.config.OTPTTL.String()); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if err = s.mailer.SendOTP(ctx, email, code); err != nil {
		s.logger.Error("send email code", "purpose", purpose, "error", err)
		s.db.Writer.Exec(ctx, `UPDATE email_otp_challenges SET consumed_at=now()
			WHERE lower(email)=lower($1) AND consumed_at IS NULL`, email) //nolint:errcheck
		return errEmailUnavailable
	}
	return nil
}

// consumeEmailCode checks the player's latest active code for a purpose. A
// right code is consumed in the caller's transaction and its address is
// returned. A wrong code counts an attempt and commits the transaction, so
// callers must not have written anything before calling it.
func (s *Server) consumeEmailCode(ctx context.Context, tx pgx.Tx, userID, purpose, code string) (string, bool, error) {
	if len(code) != 6 {
		return "", false, nil
	}
	var challengeID, email string
	var expected []byte
	var expiresAt time.Time
	var attempts int
	err := tx.QueryRow(ctx, `SELECT id,email,code_hash,expires_at,attempts FROM email_otp_challenges
		WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, userID, purpose).Scan(&challengeID, &email, &expected, &expiresAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if time.Now().After(expiresAt) || attempts >= s.config.OTPMaxAttempts {
		return "", false, nil
	}
	if !gamicsauth.VerifyOTP(expected, gamicsauth.HashOTP(s.config.OTPHashSecret, email, code)) {
		// A rejected code must still count, so the caller's transaction, which
		// has written nothing else yet, is committed here.
		if _, err = tx.Exec(ctx, `UPDATE email_otp_challenges SET attempts=attempts+1 WHERE id=$1`, challengeID); err != nil {
			return "", false, err
		}
		return "", false, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE email_otp_challenges SET consumed_at=now() WHERE id=$1`, challengeID)
	return email, err == nil, err
}

func writeEmailCodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errEmailUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "email_unavailable", "Unable to send the email. Try again later.")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to send the code.")
}
