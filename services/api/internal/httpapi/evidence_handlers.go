package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/jackc/pgx/v5"
)

const defaultImageEvidenceMaxBytes int64 = 10 << 20

// Evidence is screenshot-only: the image worker verifies JPEG and PNG, and no
// verifier exists for video.
const unsupportedEvidenceMediaMessage = "Evidence must be a JPEG or PNG screenshot. Convert HEIC/HEIF to JPEG first. Video evidence is not accepted."

type evidenceMediaPolicy struct {
	Extension string
	Kind      string
}

var supportedEvidenceMediaTypes = map[string]evidenceMediaPolicy{
	"image/jpeg": {Extension: ".jpg", Kind: "image"},
	"image/png":  {Extension: ".png", Kind: "image"},
}

// screenshotMediaTypes are the upload policy's formats, in a stable order.
// Final score reports and game-account verification attach only these.
var screenshotMediaTypes = slices.Sorted(maps.Keys(supportedEvidenceMediaTypes))

// DurationSeconds is accepted only to be rejected: dropping it would turn the
// explicit video rejection into a generic unknown-field error.
type createEvidenceInput struct {
	MediaType       string   `json:"mediaType"`
	ByteSize        int64    `json:"byteSize"`
	SHA256          string   `json:"sha256"`
	DurationSeconds *float64 `json:"durationSeconds,omitempty"`
}

type evidenceView struct {
	ID                      string     `json:"id"`
	Status                  string     `json:"status"`
	MediaKind               string     `json:"mediaKind"`
	MediaType               string     `json:"mediaType"`
	ByteSize                int64      `json:"byteSize"`
	DeclaredDurationSeconds *float64   `json:"declaredDurationSeconds,omitempty"`
	VerifiedDurationSeconds *float64   `json:"verifiedDurationSeconds,omitempty"`
	ProcessingStatus        *string    `json:"processingStatus,omitempty"`
	ProcessingErrorCode     *string    `json:"processingErrorCode,omitempty"`
	Ready                   bool       `json:"ready"`
	CompletedAt             *time.Time `json:"completedAt"`
	ProcessedAt             *time.Time `json:"processedAt"`
}

type evidenceUploadIntentView struct {
	ID              string            `json:"id"`
	UploadURL       string            `json:"uploadUrl"`
	RequiredHeaders map[string]string `json:"requiredHeaders"`
	ExpiresAt       time.Time         `json:"expiresAt"`
}

type evidenceRecord struct {
	ID                      string
	ObjectKey               string
	MediaKind               string
	MediaType               string
	ByteSize                int64
	Checksum                []byte
	Status                  string
	UploadExpires           time.Time
	DeclaredDurationSeconds *float64
	VerifiedDurationSeconds *float64
	ProcessingStatus        *string
	ProcessingErrorCode     *string
	CompletedAt             *time.Time
	ProcessedAt             *time.Time
	BoundKind               *string
	BoundID                 *string
}

func (s *Server) validateEvidenceUploadMetadata(input createEvidenceInput) (evidenceMediaPolicy, string) {
	policy, supported := supportedEvidenceMediaTypes[input.MediaType]
	if !supported || input.DurationSeconds != nil {
		return evidenceMediaPolicy{}, unsupportedEvidenceMediaMessage
	}
	maxBytes := s.config.EvidenceMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultImageEvidenceMaxBytes
	} else if maxBytes > 25<<20 {
		maxBytes = 25 << 20
	}
	if input.ByteSize < 1 || input.ByteSize > maxBytes {
		return evidenceMediaPolicy{}, fmt.Sprintf("The selected media must be between 1 byte and %d bytes.", maxBytes)
	}
	return policy, ""
}

func evidenceRecordView(record evidenceRecord) evidenceView {
	return evidenceView{
		ID: record.ID, Status: record.Status, MediaKind: record.MediaKind, MediaType: record.MediaType,
		ByteSize: record.ByteSize, DeclaredDurationSeconds: record.DeclaredDurationSeconds,
		VerifiedDurationSeconds: record.VerifiedDurationSeconds, ProcessingStatus: record.ProcessingStatus,
		ProcessingErrorCode: record.ProcessingErrorCode,
		Ready:               record.Status == "completed", CompletedAt: record.CompletedAt, ProcessedAt: record.ProcessedAt,
	}
}

func (s *Server) createEvidenceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Private evidence storage is not configured.")
		return
	}
	var input createEvidenceInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.MediaType = strings.ToLower(strings.TrimSpace(input.MediaType))
	policy, validationMessage := s.validateEvidenceUploadMetadata(input)
	checksum, checksumErr := hex.DecodeString(strings.ToLower(strings.TrimSpace(input.SHA256)))
	if validationMessage != "" || checksumErr != nil || len(checksum) != sha256.Size {
		if validationMessage == "" {
			validationMessage = "Provide a 64-character SHA-256 checksum."
		}
		writeError(w, http.StatusBadRequest, "invalid_evidence_upload", validationMessage)
		return
	}
	checksumBase64, err := storage.HexToBase64SHA256(input.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_evidence_upload", "The media checksum is invalid.")
		return
	}
	if !s.allowEvidenceUpload(w, r, input.ByteSize) {
		return
	}
	evidenceID, err := randomUUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create an evidence upload.")
		return
	}
	objectKey := path.Join("evidence", time.Now().UTC().Format("2006/01"), evidenceID+policy.Extension)
	intent, err := s.evidenceStore.PresignPut(r.Context(), objectKey, input.MediaType, checksumBase64, input.ByteSize, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign evidence upload", "request_id", r.Header.Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Unable to prepare the private evidence upload.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	_, err = s.db.Writer.Exec(r.Context(), `INSERT INTO evidence_uploads
		(id,owner_user_id,object_key,media_kind,media_type,byte_size,checksum_sha256,upload_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, evidenceID, userID, objectKey, policy.Kind,
		input.MediaType, input.ByteSize, checksum, intent.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the evidence upload.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": evidenceUploadIntentView{
		ID: evidenceID, UploadURL: intent.URL, RequiredHeaders: intent.RequiredHeaders, ExpiresAt: intent.ExpiresAt,
	}})
}

func (s *Server) completeEvidenceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Private evidence storage is not configured.")
		return
	}
	if !s.allowEvidenceAction(w, r) {
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(evidenceID) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence upload not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	record, err := s.loadEvidence(r.Context(), s.db.Writer, evidenceID, userID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence upload not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the evidence upload.")
		return
	}
	if record.Status == "completed" || record.Status == "processing" {
		writeJSON(w, http.StatusOK, map[string]any{"data": evidenceRecordView(record)})
		return
	}
	if record.Status != "pending" {
		writeError(w, http.StatusConflict, "evidence_not_pending", "This evidence upload can no longer be completed.")
		return
	}
	if record.MediaKind != "image" {
		// Only the settlement is acknowledged: a failed write answers 503 so the
		// client retries instead of leaving an uncompletable row pending.
		if err = s.failUnsupportedEvidence(r.Context(), evidenceID, userID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "evidence_media_unsupported", unsupportedEvidenceMediaMessage)
		return
	}
	// A mobile upload can finish after the PUT URL expires, or the app can lose
	// the completion response. Expiry prevents new PUTs, not verification retries.
	if !time.Now().Before(record.UploadExpires.Add(time.Hour)) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE evidence_uploads SET status='expired',updated_at=now()
			WHERE id=$1 AND owner_user_id=$2 AND status='pending'`, evidenceID, userID)
		writeError(w, http.StatusGone, "evidence_upload_expired", "The private media upload link has expired.")
		return
	}

	// Screenshots defer all storage I/O to the independently scaled image
	// worker, so completion only queues verification.
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	record, err = s.loadEvidence(r.Context(), tx, evidenceID, userID, true)
	if err != nil || (record.Status != "pending" && record.Status != "completed" && record.Status != "processing") {
		writeError(w, http.StatusConflict, "evidence_not_pending", "This evidence upload can no longer be completed.")
		return
	}
	if record.Status == "pending" {
		if err = queueEvidenceProcessing(r.Context(), tx, &record); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue media verification.")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": evidenceRecordView(record)})
}

// failUnsupportedEvidence settles a pending upload created before evidence
// became screenshot-only. The worker claims images only, so such an upload
// could never become ready.
func (s *Server) failUnsupportedEvidence(ctx context.Context, evidenceID, userID string) error {
	_, err := s.db.Writer.Exec(ctx, `UPDATE evidence_uploads
		SET status='failed',processing_error_code='video_unsupported',updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND status='pending' AND media_kind<>'image'`, evidenceID, userID)
	return err
}

// queueEvidenceProcessing moves a locked pending screenshot to processing and
// enqueues its verification job and outbox event in the caller's transaction.
func queueEvidenceProcessing(ctx context.Context, tx pgx.Tx, record *evidenceRecord) error {
	err := tx.QueryRow(ctx, `UPDATE evidence_uploads
		SET status='processing',completed_at=now(),updated_at=now()
		WHERE id=$1 AND status='pending' RETURNING completed_at,processed_at`, record.ID).
		Scan(&record.CompletedAt, &record.ProcessedAt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO evidence_media_processing_jobs(evidence_id) VALUES ($1)
		ON CONFLICT (evidence_id) DO NOTHING`, record.ID); err != nil {
		return err
	}
	// jsonb_build_object is polymorphic: bind the ID parameter explicitly as
	// text so PostgreSQL can prepare this statement.
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('evidence',$1::text,'evidence.image_processing_requested',jsonb_build_object('evidenceId',$1::text))`,
		record.ID); err != nil {
		return err
	}
	queued := "queued"
	record.Status = "processing"
	record.ProcessingStatus = &queued
	return nil
}

func (s *Server) getEvidenceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if !s.allowEvidenceAction(w, r) {
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(evidenceID) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence upload not found.")
		return
	}
	record, err := s.loadEvidence(r.Context(), s.db.Writer, evidenceID, identityFromContext(r.Context()).UserID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence upload not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the evidence upload.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": evidenceRecordView(record)})
}

func (s *Server) loadEvidence(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, evidenceID, userID string, lock bool) (evidenceRecord, error) {
	query := `SELECT evidence.id,evidence.object_key,evidence.media_kind,evidence.media_type,evidence.byte_size,
		evidence.checksum_sha256,evidence.status,evidence.upload_expires_at,evidence.declared_duration_seconds,
		evidence.verified_duration_seconds,
		(SELECT job.status FROM evidence_media_processing_jobs job WHERE job.evidence_id=evidence.id),
		evidence.processing_error_code,evidence.completed_at,evidence.processed_at,evidence.bound_kind,evidence.bound_id
		FROM evidence_uploads evidence WHERE evidence.id=$1 AND evidence.owner_user_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var record evidenceRecord
	err := querier.QueryRow(ctx, query, evidenceID, userID).Scan(&record.ID, &record.ObjectKey, &record.MediaKind, &record.MediaType,
		&record.ByteSize, &record.Checksum, &record.Status, &record.UploadExpires, &record.DeclaredDurationSeconds,
		&record.VerifiedDurationSeconds, &record.ProcessingStatus, &record.ProcessingErrorCode, &record.CompletedAt, &record.ProcessedAt,
		&record.BoundKind, &record.BoundID)
	return record, err
}

func storageChecksumBase64(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}
