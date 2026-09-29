package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const notificationCursorLifetime = 24 * time.Hour

var expoPushTokenPattern = regexp.MustCompile(`^(?:ExponentPushToken|ExpoPushToken)\[[A-Za-z0-9_-]{20,480}\]$`)

type pushTokenInput struct {
	DeviceID   string `json:"deviceId"`
	Token      string `json:"token"`
	Platform   string `json:"platform"`
	AppVersion string `json:"appVersion"`
}

type pushTokenView struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"deviceId"`
	Platform   string    `json:"platform"`
	AppVersion string    `json:"appVersion"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type notificationView struct {
	ID        string          `json:"id"`
	Category  string          `json:"category"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Data      json.RawMessage `json:"data"`
	ActionURL *string         `json:"actionUrl"`
	ReadAt    *time.Time      `json:"readAt"`
	CreatedAt time.Time       `json:"createdAt"`
}

type notificationPageOptions struct {
	Limit      int
	UnreadOnly bool
	SnapshotAt time.Time
	Cursor     *publicCursor
}

func (s *Server) upsertPushToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input pushTokenInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	input.Token = strings.TrimSpace(input.Token)
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.AppVersion = strings.TrimSpace(input.AppVersion)
	if len(input.DeviceID) < 1 || len(input.DeviceID) > 160 {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be between 1 and 160 characters.")
		return
	}
	if !expoPushTokenPattern.MatchString(input.Token) {
		writeError(w, http.StatusBadRequest, "invalid_push_token", "Provide a valid Expo push token.")
		return
	}
	if input.Platform != "android" && input.Platform != "ios" {
		writeError(w, http.StatusBadRequest, "invalid_platform", "Platform must be android or ios.")
		return
	}
	if len(input.AppVersion) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_app_version", "App version cannot exceed 64 characters.")
		return
	}

	current := identityFromContext(r.Context())
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to register this device.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	// The installation is bound to the caller's session. Holding the session row
	// makes a concurrent logout or remote revoke wait for this registration and
	// then revoke it; a session that already ended cannot register. The player
	// row is locked first, the order account deletion takes them in, so a
	// registration racing a deletion waits for it and then finds no active
	// player instead of deadlocking. Token refresh locks only the session row,
	// so it cannot deadlock with this order either.
	var playerID, sessionID string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM users
		WHERE id=$1 AND status='active' FOR KEY SHARE`, current.UserID).Scan(&playerID)
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT id::text FROM refresh_sessions
		WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL FOR SHARE`, current.SessionID, current.UserID).Scan(&sessionID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session has been logged out.")
		return
	}
	// A provider token represents one app installation. Serialize token moves and
	// device rotations so a stale login cannot continue receiving another user's
	// notifications.
	if err == nil {
		_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 910310))`, input.Token)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE push_tokens SET revoked_at=COALESCE(revoked_at,now()),updated_at=now()
			WHERE expo_push_token=$1 AND (user_id<>$2 OR device_id<>$3) AND revoked_at IS NULL`, input.Token, current.UserID, input.DeviceID)
	}
	var result pushTokenView
	if err == nil {
		err = tx.QueryRow(r.Context(), `INSERT INTO push_tokens
			(user_id,device_id,expo_push_token,platform,app_version,user_agent,session_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_id,device_id) DO UPDATE SET
				expo_push_token=EXCLUDED.expo_push_token,platform=EXCLUDED.platform,
				app_version=EXCLUDED.app_version,user_agent=EXCLUDED.user_agent,
				session_id=EXCLUDED.session_id,last_seen_at=now(),revoked_at=NULL,updated_at=now()
			RETURNING id,device_id,platform,app_version,last_seen_at,created_at,updated_at`,
			current.UserID, input.DeviceID, input.Token, input.Platform, input.AppVersion, r.UserAgent(), sessionID).
			Scan(&result.ID, &result.DeviceID, &result.Platform, &result.AppVersion, &result.LastSeenAt, &result.CreatedAt, &result.UpdatedAt)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to register this device.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) revokePushToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_push_token_id", "Push token ID is invalid.")
		return
	}
	command, err := s.db.Writer.Exec(r.Context(), `UPDATE push_tokens
		SET revoked_at=COALESCE(revoked_at,now()),updated_at=now()
		WHERE id=$1 AND user_id=$2`, id, identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to revoke this device.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "push_token_not_found", "Push token not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeSessionPushTokens revokes the push installations registered with the
// given sessions, in the transaction that ends them, so a signed-out device
// stops receiving pushes. Account deletion deletes every installation instead.
func revokeSessionPushTokens(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	_, err := tx.Exec(ctx, `UPDATE push_tokens SET revoked_at=now(),updated_at=now()
		WHERE session_id=ANY($1::text[]::uuid[]) AND revoked_at IS NULL`, sessionIDs)
	return err
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	options, err := s.parseNotificationPageOptions(r, userID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	data, err := queryNotifications(r.Context(), s.db.Reader, userID, options, options.Limit+1)
	if err != nil {
		s.logger.Warn("reader query failed; falling back to writer", "operation", "list_notifications", "error", err)
		data, err = queryNotifications(r.Context(), s.db.Writer, userID, options, options.Limit+1)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load notifications.")
		return
	}
	hasMore := len(data) > options.Limit
	if hasMore {
		data = data[:options.Limit]
	}
	var nextCursor *string
	if hasMore && len(data) > 0 {
		last := data[len(data)-1]
		scope := "all"
		if options.UnreadOnly {
			scope = "unread"
		}
		token, encodeErr := encodePublicCursor(publicCursor{
			Kind: "my_notifications", ExpiresAt: time.Now().UTC().Add(notificationCursorLifetime).Unix(),
			SnapshotAt: options.SnapshotAt.UnixNano(), Query: userID, Scope: scope,
			SortTime: last.CreatedAt.UnixNano(), ID: last.ID,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate notifications.")
			return
		}
		nextCursor = &token
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data,
		"page": map[string]any{"hasMore": hasMore, "nextCursor": nextCursor},
	})
}

func (s *Server) parseNotificationPageOptions(r *http.Request, userID string, now time.Time) (notificationPageOptions, error) {
	limit, err := parsePublicLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return notificationPageOptions{}, errors.New("limit must be between 1 and 50")
	}
	unreadOnly := false
	switch raw := strings.TrimSpace(r.URL.Query().Get("unreadOnly")); raw {
	case "", "false":
	case "true":
		unreadOnly = true
	default:
		return notificationPageOptions{}, errors.New("unreadOnly must be true or false")
	}
	result := notificationPageOptions{Limit: limit, UnreadOnly: unreadOnly, SnapshotAt: now}
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor == "" {
		return result, nil
	}
	cursor, err := decodePublicCursor(rawCursor, "my_notifications", s.config.AccessTokenSecret, now)
	scope := "all"
	if unreadOnly {
		scope = "unread"
	}
	if err != nil || cursor.Query != userID || cursor.Scope != scope || cursor.SnapshotAt <= 0 || cursor.SortTime <= 0 || !uuidPattern.MatchString(cursor.ID) {
		return notificationPageOptions{}, errors.New("cursor is invalid or expired")
	}
	result.Cursor = &cursor
	result.SnapshotAt = cursorTime(cursor.SnapshotAt)
	return result, nil
}

func queryNotifications(ctx context.Context, pool *pgxpool.Pool, userID string, options notificationPageOptions, limit int) ([]notificationView, error) {
	var cursorAt any
	var cursorID any
	if options.Cursor != nil {
		cursorAt = cursorTime(options.Cursor.SortTime)
		cursorID = options.Cursor.ID
	}
	rows, err := pool.Query(ctx, `SELECT id,category,title,body,data,action_url,read_at,created_at
		FROM notifications
		WHERE user_id=$1 AND created_at<=$2
		  AND (expires_at IS NULL OR expires_at>now())
		  AND (NOT $3::boolean OR read_at IS NULL)
		  AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5::uuid))
		ORDER BY created_at DESC,id DESC LIMIT $6`, userID, options.SnapshotAt, options.UnreadOnly, cursorAt, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := make([]notificationView, 0, min(limit, 50))
	for rows.Next() {
		var item notificationView
		if err := rows.Scan(&item.ID, &item.Category, &item.Title, &item.Body, &item.Data, &item.ActionURL, &item.ReadAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		data = append(data, item)
	}
	return data, rows.Err()
}

func (s *Server) getUnreadNotificationCount(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var count int64
	err := s.db.Writer.QueryRow(r.Context(), `SELECT count(*) FROM notifications
		WHERE user_id=$1 AND read_at IS NULL AND (expires_at IS NULL OR expires_at>now())`,
		identityFromContext(r.Context()).UserID).Scan(&count)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the unread count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unreadCount": count})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_notification_id", "Notification ID is invalid.")
		return
	}
	var readAt time.Time
	err := s.db.Writer.QueryRow(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now())
		WHERE id=$1 AND user_id=$2 RETURNING read_at`, id, identityFromContext(r.Context()).UserID).Scan(&readAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "notification_not_found", "Notification not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to mark this notification as read.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "readAt": readAt})
}

func (s *Server) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	command, err := s.db.Writer.Exec(r.Context(), `UPDATE notifications SET read_at=now()
		WHERE user_id=$1 AND read_at IS NULL AND (expires_at IS NULL OR expires_at>now())`,
		identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to mark notifications as read.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updatedCount": command.RowsAffected(), "unreadCount": 0})
}
