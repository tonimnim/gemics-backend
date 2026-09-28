package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/jackc/pgx/v5"
)

const avatarMaxBytes int64 = 10 << 20

var avatarMediaTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type avatarUploadInput struct {
	MediaType string `json:"mediaType"`
	ByteSize  int64  `json:"byteSize"`
	SHA256    string `json:"sha256"`
}

type avatarSelectionInput struct {
	UploadID string `json:"uploadId"`
}

type avatarUploadView struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	MediaType   string     `json:"mediaType"`
	ByteSize    int64      `json:"byteSize"`
	CompletedAt *time.Time `json:"completedAt"`
}

type avatarAccessView struct {
	MediaType   string    `json:"mediaType"`
	DownloadURL string    `json:"downloadUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type avatarUploadRecord struct {
	ID            string
	ObjectKey     string
	MediaType     string
	ByteSize      int64
	Checksum      []byte
	Status        string
	UploadExpires time.Time
	CompletedAt   *time.Time
}

func (s *Server) createAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Private avatar storage is not configured.")
		return
	}
	var input avatarUploadInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.MediaType = strings.ToLower(strings.TrimSpace(input.MediaType))
	extension, supported := validateAvatarUploadMetadata(input.MediaType, input.ByteSize)
	checksum, checksumErr := hex.DecodeString(strings.ToLower(strings.TrimSpace(input.SHA256)))
	if !supported || checksumErr != nil || len(checksum) != sha256.Size {
		writeError(w, http.StatusBadRequest, "invalid_avatar_upload", "Use JPEG, PNG or WebP up to 10 MiB with a 64-character SHA-256 checksum.")
		return
	}
	checksumBase64, err := storage.HexToBase64SHA256(input.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_avatar_upload", "The avatar checksum is invalid.")
		return
	}
	if !s.allowAvatarUpload(w, r, input.ByteSize) {
		return
	}
	uploadID, err := randomUUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create the avatar upload.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	objectKey := path.Join("avatars", userID, uploadID+extension)
	intent, err := s.evidenceStore.PresignPut(r.Context(), objectKey, input.MediaType, checksumBase64, input.ByteSize, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign avatar upload", "request_id", r.Header.Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Unable to prepare the private avatar upload.")
		return
	}
	_, err = s.db.Writer.Exec(r.Context(), `INSERT INTO profile_media_uploads
		(id,user_id,object_key,media_type,byte_size,checksum_sha256,upload_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, uploadID, userID, objectKey, input.MediaType, input.ByteSize, checksum, intent.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the avatar upload.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": evidenceUploadIntentView{
		ID: uploadID, UploadURL: intent.URL, RequiredHeaders: intent.RequiredHeaders, ExpiresAt: intent.ExpiresAt,
	}})
}

func validateAvatarUploadMetadata(mediaType string, byteSize int64) (string, bool) {
	extension, supported := avatarMediaTypes[mediaType]
	return extension, supported && byteSize > 0 && byteSize <= avatarMaxBytes
}

func (s *Server) completeAvatarUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Private avatar storage is not configured.")
		return
	}
	if !s.allowAvatarAction(w, r) {
		return
	}
	uploadID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(uploadID) {
		writeError(w, http.StatusNotFound, "avatar_upload_not_found", "Avatar upload not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	record, err := loadAvatarUpload(r.Context(), s.db.Writer, uploadID, userID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "avatar_upload_not_found", "Avatar upload not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the avatar upload.")
		return
	}
	if record.Status == "completed" || record.Status == "attached" {
		writeJSON(w, http.StatusOK, map[string]any{"data": avatarRecordView(record)})
		return
	}
	if record.Status != "pending" {
		writeError(w, http.StatusConflict, "avatar_upload_not_pending", "This avatar upload can no longer be completed.")
		return
	}
	if !time.Now().UTC().Before(record.UploadExpires) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE profile_media_uploads SET status='expired',updated_at=now()
			WHERE id=$1 AND user_id=$2 AND status='pending'`, uploadID, userID)
		writeError(w, http.StatusGone, "avatar_upload_expired", "The avatar upload link has expired.")
		return
	}
	info, err := s.evidenceStore.Stat(r.Context(), record.ObjectKey)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusConflict, "avatar_upload_incomplete", "Upload the avatar before completing it.")
		return
	}
	if err != nil {
		s.logger.Error("verify avatar object", "request_id", r.Header.Get("X-Request-ID"), "upload_id", uploadID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "avatar_verification_unavailable", "Unable to verify the avatar with storage.")
		return
	}
	if info.Size != record.ByteSize || !storage.EqualChecksum(info.ChecksumSHA256, storageChecksumBase64(record.Checksum)) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE profile_media_uploads SET status='failed',updated_at=now()
			WHERE id=$1 AND user_id=$2 AND status='pending'`, uploadID, userID)
		writeError(w, http.StatusUnprocessableEntity, "avatar_verification_failed", "The uploaded avatar does not match its declared size and checksum.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the avatar upload.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	record, err = loadAvatarUpload(r.Context(), tx, uploadID, userID, true)
	if err != nil || record.Status != "pending" && record.Status != "completed" && record.Status != "attached" {
		writeError(w, http.StatusConflict, "avatar_upload_not_pending", "This avatar upload can no longer be completed.")
		return
	}
	if record.Status == "pending" {
		providerETag := truncate(strings.TrimSpace(info.ETag), 512)
		err = tx.QueryRow(r.Context(), `UPDATE profile_media_uploads
			SET status='completed',completed_at=now(),provider_etag=$1,updated_at=now()
			WHERE id=$2 AND status='pending' RETURNING completed_at`, providerETag, uploadID).Scan(&record.CompletedAt)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the avatar upload.")
			return
		}
		record.Status = "completed"
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the avatar upload.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": avatarRecordView(record)})
}

func (s *Server) updateProfileAvatar(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input avatarSelectionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.UploadID = strings.TrimSpace(input.UploadID)
	if !uuidPattern.MatchString(input.UploadID) {
		writeError(w, http.StatusBadRequest, "invalid_avatar_upload", "Choose a completed avatar upload.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the profile avatar.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	record, err := loadAvatarUpload(r.Context(), tx, input.UploadID, userID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "avatar_upload_not_found", "Avatar upload not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the avatar upload.")
		return
	}
	if record.Status != "completed" && record.Status != "attached" {
		writeError(w, http.StatusConflict, "avatar_not_ready", "Complete the avatar upload before selecting it.")
		return
	}
	var updatedAt time.Time
	err = tx.QueryRow(r.Context(), `UPDATE player_profiles SET avatar_object_key=$1,updated_at=now()
		WHERE user_id=$2 RETURNING updated_at`, record.ObjectKey, userID).Scan(&updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "profile_required", "Create the player profile before selecting an avatar.")
		return
	}
	if err == nil && record.Status == "completed" {
		_, err = tx.Exec(r.Context(), `UPDATE profile_media_uploads SET status='attached',attached_at=now(),updated_at=now()
			WHERE id=$1 AND user_id=$2 AND status='completed'`, input.UploadID, userID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1,'profile.avatar_updated','player_profile',$1,$2,jsonb_build_object('uploadId',$3))`,
			userID, r.Header.Get("X-Request-ID"), input.UploadID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the profile avatar.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"uploadId": input.UploadID, "status": "active", "updatedAt": updatedAt,
		"accessEndpoint": "/v1/me/avatar",
	}})
}

func (s *Server) getMyAvatarAccess(w http.ResponseWriter, r *http.Request) {
	s.writeAvatarAccess(w, r, `SELECT profile.avatar_object_key,upload.media_type
		FROM player_profiles profile
		JOIN profile_media_uploads upload ON upload.object_key=profile.avatar_object_key AND upload.status='attached'
		WHERE profile.user_id=$1`, identityFromContext(r.Context()).UserID)
}

func (s *Server) getPublicAvatarAccess(w http.ResponseWriter, r *http.Request) {
	playerID := strings.TrimSpace(r.PathValue("playerId"))
	if !uuidPattern.MatchString(playerID) {
		writeError(w, http.StatusNotFound, "avatar_not_found", "Player avatar not found.")
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Private avatar storage is not configured.")
		return
	}
	var objectKey string
	err := s.db.Writer.QueryRow(r.Context(), `SELECT profile.avatar_object_key
		FROM player_profiles profile
		JOIN users player ON player.id=profile.user_id AND player.status='active'
		JOIN profile_media_uploads upload ON upload.object_key=profile.avatar_object_key AND upload.status='attached'
		WHERE profile.user_id=$1 AND profile.discoverable=true`, playerID).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "avatar_not_found", "Player avatar not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player avatar.")
		return
	}
	intent, err := s.evidenceStore.PresignGet(r.Context(), objectKey, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign public avatar", "request_id", r.Header.Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Unable to prepare the avatar view.")
		return
	}
	maxAge := s.config.StoragePresignTTL / 2
	if maxAge > 5*time.Minute {
		maxAge = 5 * time.Minute
	}
	if maxAge < time.Second {
		maxAge = time.Second
	}
	w.Header().Set("Cache-Control", "private, max-age="+strconv.FormatInt(int64(maxAge/time.Second), 10))
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, intent.URL, http.StatusTemporaryRedirect)
}

func (s *Server) writeAvatarAccess(w http.ResponseWriter, r *http.Request, query, ownerID string) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Private avatar storage is not configured.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var objectKey, mediaType string
	// Discoverability and active-avatar selection are access-control state. Use
	// the writer so a lagging replica cannot mint a URL after either is revoked.
	err := s.db.Writer.QueryRow(r.Context(), query, ownerID).Scan(&objectKey, &mediaType)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "avatar_not_found", "Player avatar not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player avatar.")
		return
	}
	intent, err := s.evidenceStore.PresignGet(r.Context(), objectKey, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign avatar view", "request_id", r.Header.Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusServiceUnavailable, "avatar_storage_unavailable", "Unable to prepare the avatar view.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": avatarAccessView{
		MediaType: mediaType, DownloadURL: intent.URL, ExpiresAt: intent.ExpiresAt,
	}})
}

func loadAvatarUpload(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, uploadID, userID string, lock bool) (avatarUploadRecord, error) {
	query := `SELECT id,object_key,media_type,byte_size,checksum_sha256,status,upload_expires_at,completed_at
		FROM profile_media_uploads WHERE id=$1 AND user_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var record avatarUploadRecord
	err := querier.QueryRow(ctx, query, uploadID, userID).Scan(&record.ID, &record.ObjectKey, &record.MediaType,
		&record.ByteSize, &record.Checksum, &record.Status, &record.UploadExpires, &record.CompletedAt)
	return record, err
}

func avatarRecordView(record avatarUploadRecord) avatarUploadView {
	return avatarUploadView{
		ID: record.ID, Status: record.Status, MediaType: record.MediaType,
		ByteSize: record.ByteSize, CompletedAt: record.CompletedAt,
	}
}
