package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Players register with exactly three things: a username, their eFootball
// Konami ID and a password. They sign in with the Konami ID and password.
// Email and phone are added later, from the signed-in account.

// registrationGameID is the game whose publisher ID is the Konami ID.
const registrationGameID = "efootball-mobile"

// konamiIDPattern bounds what players may type. Konami publishes no format,
// so this stays permissive; comparisons use konamiKey's normalized form.
var konamiIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{5,63}$`)

// konamiKeyExpression is the SQL twin of konamiKey; the unique index on
// game_accounts is built on exactly this expression.
const konamiKeyExpression = `upper(regexp_replace(account.publisher_player_id,'[^A-Za-z0-9]+','','g'))`

type registerRequest struct {
	Username   string `json:"username"`
	KonamiID   string `json:"konamiId"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

type loginRequest struct {
	KonamiID   string `json:"konamiId"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

// normalizeKonamiID returns the ID as the player typed it, trimmed, and the
// uppercase alphanumeric key used for uniqueness and sign-in.
func normalizeKonamiID(raw string) (string, string, bool) {
	value := strings.TrimSpace(raw)
	if !konamiIDPattern.MatchString(value) {
		return "", "", false
	}
	key := konamiKey(value)
	if len(key) < 6 {
		return "", "", false
	}
	return value, key, true
}

func konamiKey(value string) string {
	var key strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			key.WriteRune(char - 'a' + 'A')
		case (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9'):
			key.WriteRune(char)
		}
	}
	return key.String()
}

// passwordRepeatsIdentity rejects a password that is just the username or
// Konami ID, the first two guesses anyone would make.
func passwordRepeatsIdentity(password, username, konamiKeyValue string) bool {
	return strings.EqualFold(strings.TrimSpace(password), username) || konamiKey(password) == konamiKeyValue
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input registerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	username := strings.TrimSpace(input.Username)
	if !handlePattern.MatchString(username) {
		writeError(w, http.StatusBadRequest, "invalid_username", "Username must be 3-24 letters, numbers, underscores or dots.")
		return
	}
	konamiID, key, ok := normalizeKonamiID(input.KonamiID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the Konami ID shown in eFootball.")
		return
	}
	if !gamicsauth.ValidPasswordLength(input.Password) {
		writeError(w, http.StatusBadRequest, "invalid_password", "Password must be 8-128 characters.")
		return
	}
	if passwordRepeatsIdentity(input.Password, username, key) {
		writeError(w, http.StatusBadRequest, "weak_password", "Choose a password that is not your username or Konami ID.")
		return
	}
	if len(input.DeviceName) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_device_name", "Device name cannot exceed 120 characters.")
		return
	}
	ip := s.clientIP(r)
	if !s.allowRegistration(r.Context(), ip) {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.RegistrationWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many accounts created from this network. Try again later.")
		return
	}
	passwordHash, err := gamicsauth.HashPassword(input.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create the player account.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the player account.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	userID, err := createRegisteredPlayer(r.Context(), tx, username, konamiID, passwordHash, ip, r.UserAgent())
	switch {
	case uniqueViolationOn(err, "player_profiles_handle_unique"):
		writeError(w, http.StatusConflict, "username_taken", "That username is already taken.")
		return
	case uniqueViolationOn(err, "game_accounts_publisher_id_unique"):
		writeError(w, http.StatusConflict, "konami_id_taken", "That Konami ID already has a Gamics account. Sign in instead.")
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the player account.")
		return
	}
	sessionID, refreshToken, err := s.createSession(r.Context(), tx, r, userID, input.DeviceName)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a session.")
		return
	}
	s.writeSessionResponse(w, r, http.StatusCreated, userID, sessionID, refreshToken)
}

// createRegisteredPlayer writes the whole account in one transaction: the
// username is the handle and display name, the Konami ID is the eFootball
// game account, and the current terms and privacy notice, presented on the
// registration screen, are recorded as accepted.
func createRegisteredPlayer(ctx context.Context, tx pgx.Tx, username, konamiID, passwordHash, ip, userAgent string) (string, error) {
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO users(display_name,password_hash,password_changed_at,registration_ip,status)
		VALUES ($1,$2,now(),$3,'active') RETURNING id`, username, passwordHash, ip).Scan(&userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, username); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO game_accounts(user_id,game_id,in_game_name,publisher_player_id)
		VALUES ($1,$2,'',$3)`, userID, registrationGameID, konamiID); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `WITH accepted AS (
			INSERT INTO legal_acceptances(user_id,document_type,version,accepted_ip,user_agent)
			SELECT $1,document_type,version,$2,$3 FROM legal_documents
			WHERE is_current=true AND effective_at<=now()
			RETURNING document_type)
		UPDATE users SET
			terms_accepted_at=CASE WHEN EXISTS(SELECT 1 FROM accepted WHERE document_type='terms') THEN now() END,
			privacy_accepted_at=CASE WHEN EXISTS(SELECT 1 FROM accepted WHERE document_type='privacy') THEN now() END
		WHERE id=$1`, userID, ip, userAgent)
	return userID, err
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input loginRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	_, key, ok := normalizeKonamiID(input.KonamiID)
	if !ok || input.Password == "" || len(input.Password) > 4*gamicsauth.PasswordMaxLength || len(input.DeviceName) > 120 {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The Konami ID or password is incorrect.")
		return
	}
	ip := s.clientIP(r)
	allowed, err := s.loginAllowed(r.Context(), key, ip)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.LoginFailureWindow))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Try again later.")
		return
	}
	userID, status, passwordHash, err := s.lookupKonamiAccount(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
		return
	}
	// A miss still verifies against a dummy hash, so it takes as long as a
	// wrong password.
	if !gamicsauth.VerifyPassword(passwordHash, input.Password) {
		s.recordLoginFailure(r.Context(), key, ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The Konami ID or password is incorrect.")
		return
	}
	if status != "active" {
		writeError(w, http.StatusForbidden, "account_unavailable", "This player account is not available.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a session.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = tx.Exec(r.Context(), `DELETE FROM login_failures WHERE konami_key=$1`, loginFailureKey(key)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a session.")
		return
	}
	sessionID, refreshToken, err := s.createSession(r.Context(), tx, r, userID, input.DeviceName)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a session.")
		return
	}
	s.writeSessionResponse(w, r, http.StatusOK, userID, sessionID, refreshToken)
}

// lookupKonamiAccount resolves a Konami key to its player. A miss returns no
// error and an empty password hash.
func (s *Server) lookupKonamiAccount(ctx context.Context, key string) (string, string, string, error) {
	var userID, status string
	var passwordHash *string
	err := s.db.Writer.QueryRow(ctx, `SELECT player.id,player.status,player.password_hash
		FROM game_accounts account JOIN users player ON player.id=account.user_id
		WHERE account.game_id=$1 AND account.publisher_player_id IS NOT NULL
		AND `+konamiKeyExpression+`=$2`, registrationGameID, key).Scan(&userID, &status, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil
	}
	return userID, status, valueOrEmpty(passwordHash), err
}

// createSession inserts a refresh session in the caller's transaction.
func (s *Server) createSession(ctx context.Context, tx pgx.Tx, r *http.Request, userID, deviceName string) (string, string, error) {
	refreshToken, refreshHash, err := gamicsauth.NewRefreshToken()
	if err != nil {
		return "", "", err
	}
	sessionID := gamicsauth.RandomID()
	_, err = tx.Exec(ctx, `INSERT INTO refresh_sessions
		(id,user_id,token_hash,device_name,user_agent,created_ip,last_used_ip)
		VALUES ($1,$2,$3,$4,$5,$6,$6)`, sessionID, userID, refreshHash, strings.TrimSpace(deviceName), r.UserAgent(), s.clientIP(r))
	return sessionID, refreshToken, err
}

// loginFailureKey stores a digest rather than the Konami ID itself.
func loginFailureKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

// loginAllowed counts recent failures for the Konami ID and for the IP. The
// counters live in PostgreSQL, so every API replica shares them and they keep
// working without Redis.
func (s *Server) loginAllowed(ctx context.Context, key, ip string) (bool, error) {
	var byKey, byIP int
	err := s.db.Writer.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM login_failures WHERE konami_key=$1 AND created_at>now()-$3::interval),
		(SELECT count(*) FROM login_failures WHERE request_ip=$2 AND created_at>now()-$3::interval)`,
		loginFailureKey(key), ip, s.config.LoginFailureWindow.String()).Scan(&byKey, &byIP)
	return byKey < s.config.LoginAccountFailures && byIP < s.config.LoginIPFailures, err
}

// recordLoginFailure also prunes a bounded batch of day-old rows, so the
// table stays small without a sweeper process.
func (s *Server) recordLoginFailure(ctx context.Context, key, ip string) {
	if _, err := s.db.Writer.Exec(ctx, `WITH pruned AS (
			DELETE FROM login_failures WHERE id IN (
				SELECT id FROM login_failures WHERE created_at<now()-interval '1 day' ORDER BY created_at LIMIT 100))
		INSERT INTO login_failures(konami_key,request_ip) VALUES ($1,$2)`, loginFailureKey(key), ip); err != nil {
		s.logger.Warn("record login failure", "error", err)
	}
}

// allowRegistration limits new accounts per IP, in Redis when available and
// otherwise from the registrations PostgreSQL already holds.
func (s *Server) allowRegistration(ctx context.Context, ip string) bool {
	if s.redis != nil {
		digest := sha256.Sum256([]byte(ip))
		key := s.securityKey("rate:register:ip:" + hex.EncodeToString(digest[:]))
		if allowed, err := redisFixedWindow(ctx, s.redis, key, s.config.RegistrationIPLimit, s.config.RegistrationWindow); err == nil {
			return allowed
		}
	}
	var count int
	if err := s.db.Writer.QueryRow(ctx, `SELECT count(*) FROM users
		WHERE registration_ip=$1 AND created_at>now()-$2::interval`, ip, s.config.RegistrationWindow.String()).Scan(&count); err != nil {
		return false
	}
	return count < s.config.RegistrationIPLimit
}

func uniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
