package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	stdmail "net/mail"
	"strings"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
)

type identityContextKey struct{}

type identity struct {
	UserID    string
	SessionID string
	ExpiresAt time.Time
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

const refreshTokenRetryGrace = 5 * time.Minute

func (s *Server) refreshSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input refreshRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token_required", "Refresh token is required.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to refresh the session.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	presentedHash := gamicsauth.HashRefreshToken(input.RefreshToken)
	var sessionID, userID string
	var currentHash, previousHash []byte
	var previousValidUntil *time.Time
	// Only the session row is locked. Locking the player too would invert the
	// player-then-session order that push token registration and account
	// deletion use, and a deadlock here would sign the player out.
	err = tx.QueryRow(r.Context(), `SELECT s.id,s.user_id,s.token_hash,s.previous_token_hash,s.previous_token_valid_until
		FROM refresh_sessions s
		JOIN users u ON u.id=s.user_id
		WHERE (s.token_hash=$1 OR (s.previous_token_hash=$1 AND s.previous_token_valid_until>now()))
		AND s.revoked_at IS NULL AND u.status='active' FOR UPDATE OF s`, presentedHash).
		Scan(&sessionID, &userID, &currentHash, &previousHash, &previousValidUntil)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session is no longer valid.")
		return
	}
	newToken, newHash, err := gamicsauth.NewRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to refresh the session.")
		return
	}
	nextPreviousHash := currentHash
	nextPreviousValidUntil := time.Now().UTC().Add(refreshTokenRetryGrace)
	if len(previousHash) > 0 && previousValidUntil != nil && subtle.ConstantTimeCompare(presentedHash, previousHash) == 1 {
		// Preserve the originally presented token during the bounded retry window.
		// If the previous refresh response was lost, the client can safely retry and
		// receive another rotated token instead of being forced to sign in again.
		nextPreviousHash = previousHash
		nextPreviousValidUntil = *previousValidUntil
	}
	_, err = tx.Exec(r.Context(), `UPDATE refresh_sessions SET token_hash=$1,previous_token_hash=$2,
		previous_token_valid_until=$3,last_used_at=now(),last_used_ip=$4 WHERE id=$5`,
		newHash, nextPreviousHash, nextPreviousValidUntil, s.clientIP(r), sessionID)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to refresh the session.")
		return
	}
	s.writeSessionResponse(w, r, http.StatusOK, userID, sessionID, newToken)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	current := identityFromContext(r.Context())
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to log out.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = tx.Exec(r.Context(), `UPDATE refresh_sessions SET revoked_at=COALESCE(revoked_at,now())
        WHERE id=$1 AND user_id=$2`, current.SessionID, current.UserID); err == nil {
		err = revokeSessionPushTokens(r.Context(), tx, []string{current.SessionID})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to log out.")
		return
	}
	if s.redis != nil {
		ttl := time.Until(current.ExpiresAt)
		if ttl > 0 {
			pipe := s.redis.Pipeline()
			pipe.Set(r.Context(), s.securityKey("auth:session:"+current.SessionID), "revoked", ttl)
			pipe.Set(r.Context(), "auth:revoked:"+current.SessionID, "1", ttl)
			if _, err := pipe.Exec(r.Context()); err != nil {
				s.logger.Warn("cache session revocation", "session_id", current.SessionID, "error", err)
				writeError(w, http.StatusServiceUnavailable, "logout_propagation_failed", "The session was revoked, but logout propagation is still completing. Please retry.")
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeSessionResponse(w http.ResponseWriter, r *http.Request, status int, userID, sessionID, refreshToken string) {
	accessToken, expiresAt, err := s.tokens.Issue(userID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to issue an access token.")
		return
	}
	if s.redis != nil {
		ttl := minDuration(s.config.AuthSessionCacheTTL, maxPositiveSessionCacheTTL, time.Until(expiresAt))
		if ttl > 0 {
			_ = s.redis.Set(r.Context(), s.securityKey("auth:session:"+sessionID), "active", ttl).Err()
		}
	}
	player, err := s.loadMe(r.Context(), s.db.Writer, userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player account.")
		return
	}
	writeJSON(w, status, map[string]any{
		"accessToken": accessToken, "refreshToken": refreshToken, "tokenType": "Bearer",
		"expiresInSeconds": int(time.Until(expiresAt).Seconds()), "player": player,
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		claims, err := s.tokens.Parse(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_access_token", "The access token is invalid or expired.")
			return
		}
		active, sessionErr := s.sessionActive(r.Context(), claims.UserID, claims.SessionID, time.Unix(claims.ExpiresAt, 0))
		if sessionErr != nil {
			writeError(w, http.StatusServiceUnavailable, "session_check_unavailable", "Unable to validate the session.")
			return
		}
		if !active {
			writeError(w, http.StatusUnauthorized, "invalid_session", "The session has been logged out.")
			return
		}
		current := identity{UserID: claims.UserID, SessionID: claims.SessionID, ExpiresAt: time.Unix(claims.ExpiresAt, 0)}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey{}, current)))
	})
}

func identityFromContext(ctx context.Context) identity {
	value, _ := ctx.Value(identityContextKey{}).(identity)
	return value
}

func normalizeEmail(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	address, err := stdmail.ParseAddress(raw)
	if err != nil || strings.ToLower(address.Address) != raw || len(raw) > 254 || !strings.Contains(raw, "@") {
		return "", false
	}
	return raw, true
}

func (s *Server) allowOTPRate(ctx context.Context, kind, value string, limit int) bool {
	digest := sha256.Sum256([]byte(strings.ToLower(value)))
	if s.redis != nil {
		key := s.securityKey("rate:otp:" + kind + ":" + hex.EncodeToString(digest[:]))
		if allowed, err := redisFixedWindow(ctx, s.redis, key, limit, s.config.OTPRequestWindow); err == nil {
			return allowed
		}
	}
	column := "request_ip"
	if kind == "email" {
		column = "lower(email)"
		value = strings.ToLower(value)
	}
	var count int
	query := `SELECT count(*) FROM email_otp_challenges WHERE ` + column + `=$1 AND created_at > now()-$2::interval`
	if err := s.db.Writer.QueryRow(ctx, query, value, s.config.OTPRequestWindow.String()).Scan(&count); err != nil {
		return false
	}
	return count < limit
}
