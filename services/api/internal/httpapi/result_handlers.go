package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/jackc/pgx/v5"
)

var supportedEvidenceMediaTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/heic": ".heic",
	"image/heif": ".heif",
}

type createEvidenceInput struct {
	MediaType string `json:"mediaType"`
	ByteSize  int64  `json:"byteSize"`
	SHA256    string `json:"sha256"`
}

type evidenceView struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	MediaType   string     `json:"mediaType"`
	ByteSize    int64      `json:"byteSize"`
	CompletedAt *time.Time `json:"completedAt"`
}

type evidenceUploadIntentView struct {
	ID              string            `json:"id"`
	UploadURL       string            `json:"uploadUrl"`
	RequiredHeaders map[string]string `json:"requiredHeaders"`
	ExpiresAt       time.Time         `json:"expiresAt"`
}

type evidenceAccessView struct {
	ID          string    `json:"id"`
	MediaType   string    `json:"mediaType"`
	ByteSize    int64     `json:"byteSize"`
	DownloadURL string    `json:"downloadUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type gameScoreInput struct {
	HomeScore int `json:"homeScore"`
	AwayScore int `json:"awayScore"`
}

type tiebreakScoreInput struct {
	Type      string `json:"type"`
	HomeScore int    `json:"homeScore"`
	AwayScore int    `json:"awayScore"`
}

type submitResultInput struct {
	HomeScore           int                 `json:"homeScore"`
	AwayScore           int                 `json:"awayScore"`
	Tiebreak            *tiebreakScoreInput `json:"tiebreak,omitempty"`
	Games               []gameScoreInput    `json:"games"`
	EvidenceIDs         []string            `json:"evidenceIds"`
	DeclarationAccepted bool                `json:"declarationAccepted"`
}

type decideResultInput struct {
	Decision    string   `json:"decision"`
	ReasonCode  string   `json:"reasonCode"`
	Note        string   `json:"note"`
	EvidenceIDs []string `json:"evidenceIds"`
}

type resultSubmissionView struct {
	ID          string              `json:"id"`
	MatchID     string              `json:"matchId"`
	SubmittedBy string              `json:"submittedBy"`
	HomeScore   int                 `json:"homeScore"`
	AwayScore   int                 `json:"awayScore"`
	Tiebreak    *tiebreakScoreInput `json:"tiebreak,omitempty"`
	Games       []gameScoreInput    `json:"games"`
	EvidenceIDs []string            `json:"evidenceIds"`
	Status      string              `json:"status"`
	SubmittedAt time.Time           `json:"submittedAt"`
	DecidedAt   *time.Time          `json:"decidedAt"`
	DecidedBy   *string             `json:"decidedBy"`
}

type resultMatchStateView struct {
	ID            string     `json:"id"`
	State         string     `json:"state"`
	Version       int        `json:"version"`
	WinnerEntryID *string    `json:"winnerEntryId"`
	CompletedAt   *time.Time `json:"completedAt"`
}

type resultDecisionView struct {
	Match         matchRoomResponse    `json:"match"`
	Submission    resultSubmissionView `json:"submission"`
	RatingChanges []ratingChangeView   `json:"ratingChanges,omitempty"`
}

type ratingChangeView struct {
	PlayerID string `json:"playerId"`
	GameID   string `json:"gameId"`
	Before   int    `json:"before"`
	After    int    `json:"after"`
	Delta    int    `json:"delta"`
}

type evidenceRecord struct {
	ID            string
	ObjectKey     string
	MediaType     string
	ByteSize      int64
	Checksum      []byte
	Status        string
	UploadExpires time.Time
	CompletedAt   *time.Time
	BoundKind     *string
	BoundID       *string
}

func (s *Server) createEvidenceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Result screenshot storage is not configured.")
		return
	}
	var input createEvidenceInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.MediaType = strings.ToLower(strings.TrimSpace(input.MediaType))
	extension, mediaAllowed := supportedEvidenceMediaTypes[input.MediaType]
	checksum, checksumErr := hex.DecodeString(strings.ToLower(strings.TrimSpace(input.SHA256)))
	if !mediaAllowed || input.ByteSize < 1 || input.ByteSize > s.config.EvidenceMaxBytes || checksumErr != nil || len(checksum) != sha256.Size {
		writeError(w, http.StatusBadRequest, "invalid_evidence_upload", "Provide a supported image type, safe byte size and 64-character SHA-256 checksum.")
		return
	}
	checksumBase64, err := storage.HexToBase64SHA256(input.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_evidence_upload", "The screenshot checksum is invalid.")
		return
	}
	evidenceID, err := randomUUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create an evidence upload.")
		return
	}
	objectKey := path.Join("evidence", time.Now().UTC().Format("2006/01"), evidenceID+extension)
	intent, err := s.evidenceStore.PresignPut(r.Context(), objectKey, input.MediaType, checksumBase64, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign evidence upload", "request_id", r.Header.Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Unable to prepare the screenshot upload.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	_, err = s.db.Writer.Exec(r.Context(), `INSERT INTO evidence_uploads
		(id,owner_user_id,object_key,media_type,byte_size,checksum_sha256,upload_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, evidenceID, userID, objectKey, input.MediaType, input.ByteSize, checksum, intent.ExpiresAt)
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
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Result screenshot storage is not configured.")
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
	if record.Status == "completed" {
		writeJSON(w, http.StatusOK, map[string]any{"data": evidenceView{ID: record.ID, Status: record.Status, MediaType: record.MediaType, ByteSize: record.ByteSize, CompletedAt: record.CompletedAt}})
		return
	}
	if record.Status != "pending" {
		writeError(w, http.StatusConflict, "evidence_not_pending", "This evidence upload can no longer be completed.")
		return
	}
	if !time.Now().Before(record.UploadExpires) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE evidence_uploads SET status='expired',updated_at=now()
			WHERE id=$1 AND owner_user_id=$2 AND status='pending'`, evidenceID, userID)
		writeError(w, http.StatusGone, "evidence_upload_expired", "The screenshot upload link has expired.")
		return
	}

	info, err := s.evidenceStore.Stat(r.Context(), record.ObjectKey)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusConflict, "evidence_upload_incomplete", "Upload the screenshot before completing the evidence.")
		return
	}
	if err != nil {
		s.logger.Error("verify evidence object", "request_id", r.Header.Get("X-Request-ID"), "evidence_id", evidenceID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "evidence_verification_unavailable", "Unable to verify the screenshot with storage.")
		return
	}
	expectedChecksum := storageChecksumBase64(record.Checksum)
	if info.Size != record.ByteSize || !storage.EqualChecksum(info.ChecksumSHA256, expectedChecksum) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE evidence_uploads SET status='failed',updated_at=now()
			WHERE id=$1 AND owner_user_id=$2 AND status='pending'`, evidenceID, userID)
		writeError(w, http.StatusUnprocessableEntity, "evidence_verification_failed", "The uploaded screenshot does not match its declared size and checksum.")
		return
	}

	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	record, err = s.loadEvidence(r.Context(), tx, evidenceID, userID, true)
	if err != nil || (record.Status != "pending" && record.Status != "completed") {
		writeError(w, http.StatusConflict, "evidence_not_pending", "This evidence upload can no longer be completed.")
		return
	}
	if record.Status == "pending" {
		providerETag := truncate(strings.TrimSpace(info.ETag), 512)
		err = tx.QueryRow(r.Context(), `UPDATE evidence_uploads SET status='completed',completed_at=now(),provider_etag=$1,updated_at=now()
			WHERE id=$2 AND status='pending' RETURNING completed_at`, providerETag, evidenceID).Scan(&record.CompletedAt)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
			return
		}
		record.Status = "completed"
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to complete the evidence upload.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": evidenceView{ID: record.ID, Status: record.Status, MediaType: record.MediaType, ByteSize: record.ByteSize, CompletedAt: record.CompletedAt}})
}

func (s *Server) getEvidenceAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Result screenshot storage is not configured.")
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(evidenceID) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	query := `SELECT evidence.object_key,evidence.media_type,evidence.byte_size
		FROM evidence_uploads evidence
		JOIN result_submissions submission ON submission.id=evidence.bound_id
		JOIN matches match ON match.id=submission.match_id
		JOIN competitions competition ON competition.id=match.competition_id
		WHERE evidence.id=$1 AND evidence.status='completed'
		  AND evidence.bound_kind IN ('result_submission','result_dispute')
		  AND (
			EXISTS (SELECT 1 FROM entry_members member
				WHERE member.user_id=$2 AND member.roster_role IN ('starter','substitute')
				  AND member.entry_id IN (match.home_entry_id,match.away_entry_id))
			OR EXISTS (SELECT 1 FROM organization_members staff
				WHERE staff.organization_id=competition.organization_id AND staff.user_id=$2
				  AND staff.role IN ('owner','admin','referee'))
		  )`
	var objectKey, mediaType string
	var byteSize int64
	err := s.db.Reader.QueryRow(r.Context(), query, evidenceID, userID).Scan(&objectKey, &mediaType, &byteSize)
	if err != nil && s.db.Reader != s.db.Writer {
		err = s.db.Writer.QueryRow(r.Context(), query, evidenceID, userID).Scan(&objectKey, &mediaType, &byteSize)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to authorize the evidence.")
		return
	}
	intent, err := s.evidenceStore.PresignGet(r.Context(), objectKey, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign evidence download", "request_id", r.Header.Get("X-Request-ID"), "evidence_id", evidenceID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Unable to prepare the screenshot view.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": evidenceAccessView{
		ID: evidenceID, MediaType: mediaType, ByteSize: byteSize, DownloadURL: intent.URL, ExpiresAt: intent.ExpiresAt,
	}})
}

func (s *Server) submitMatchResult(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	matchID := strings.TrimSpace(r.PathValue("matchId"))
	if !uuidPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input submitResultInput
	if !decodeJSON(w, r, &input) {
		return
	}
	normalizeUUIDList(input.EvidenceIDs)
	if input.Tiebreak != nil {
		input.Tiebreak.Type = strings.ToLower(strings.TrimSpace(input.Tiebreak.Type))
	}
	if !input.DeclarationAccepted || input.HomeScore < 0 || input.HomeScore > 99 || input.AwayScore < 0 || input.AwayScore > 99 || len(input.EvidenceIDs) < 1 || len(input.EvidenceIDs) > 5 || !validUUIDList(input.EvidenceIDs) {
		writeError(w, http.StatusBadRequest, "invalid_result", "Provide valid scores, completed evidence and accept the declaration.")
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to submit the result.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to submit the result.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	replay, err := beginIdempotentRequest(r.Context(), tx, "result-submit:"+userID+":"+matchID, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another result.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to submit the result.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}

	match, err := lockMatchForResult(r.Context(), tx, matchID, userID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !match.ActorHome && !match.ActorAway) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the match.")
		return
	}
	if match.HomeEntryID == nil || match.AwayEntryID == nil || match.State != "in_progress" {
		writeError(w, http.StatusConflict, "result_not_allowed", "This match is not accepting a result.")
		return
	}
	if match.ResultDueAt != nil && time.Now().After(*match.ResultDueAt) {
		writeError(w, http.StatusConflict, "result_deadline_passed", "The result deadline has passed.")
		return
	}
	if validationErr := validateScorePolicy(input, match.BestOf, match.Format); validationErr != "" {
		writeError(w, http.StatusBadRequest, "invalid_score", validationErr)
		return
	}
	if err = lockCompletedEvidence(r.Context(), tx, input.EvidenceIDs, userID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "evidence_not_ready", "Every evidence item must be completed, owned by you and unused.")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the result evidence.")
		return
	}

	gamesJSON, _ := json.Marshal(input.Games)
	evidenceJSON, _ := json.Marshal(input.EvidenceIDs)
	var submission resultSubmissionView
	var tiebreakType *string
	var homeTiebreakScore, awayTiebreakScore *int
	if input.Tiebreak != nil {
		tiebreakType = &input.Tiebreak.Type
		homeTiebreakScore = &input.Tiebreak.HomeScore
		awayTiebreakScore = &input.Tiebreak.AwayScore
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO result_submissions
		(match_id,submitted_by,home_score,away_score,tiebreak_type,home_tiebreak_score,away_tiebreak_score,game_results,evidence_objects,status,match_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending_confirmation',$10)
		RETURNING id,submitted_at`, matchID, userID, input.HomeScore, input.AwayScore, tiebreakType, homeTiebreakScore, awayTiebreakScore,
		json.RawMessage(gamesJSON), json.RawMessage(evidenceJSON), match.Version).
		Scan(&submission.ID, &submission.SubmittedAt)
	if err != nil {
		writeError(w, http.StatusConflict, "result_already_submitted", "This match already has a result awaiting confirmation.")
		return
	}
	for position, evidenceID := range input.EvidenceIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO result_submission_evidence(submission_id,evidence_id,position) VALUES ($1,$2,$3)`, submission.ID, evidenceID, position); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to attach the result evidence.")
			return
		}
		command, bindErr := tx.Exec(r.Context(), `UPDATE evidence_uploads SET bound_kind='result_submission',bound_id=$1,bound_at=now(),updated_at=now()
			WHERE id=$2 AND bound_id IS NULL AND status='completed'`, submission.ID, evidenceID)
		if bindErr != nil || command.RowsAffected() != 1 {
			writeError(w, http.StatusConflict, "evidence_already_used", "An evidence item is already attached to another result.")
			return
		}
	}
	var nextVersion int
	err = tx.QueryRow(r.Context(), `UPDATE matches SET state='awaiting_confirmation',version=version+1,updated_at=now()
		WHERE id=$1 AND version=$2 RETURNING version`, matchID, match.Version).Scan(&nextVersion)
	if err != nil {
		writeError(w, http.StatusConflict, "match_changed", "The match changed while the result was being submitted.")
		return
	}
	submission.MatchID = matchID
	submission.SubmittedBy = userID
	submission.HomeScore = input.HomeScore
	submission.AwayScore = input.AwayScore
	submission.Tiebreak = input.Tiebreak
	submission.Games = input.Games
	submission.EvidenceIDs = append([]string(nil), input.EvidenceIDs...)
	submission.Status = "pending_confirmation"

	if err = writeResultAuditAndOutbox(r.Context(), tx, match.OrganizationID, userID, "result.submitted", submission.ID, r.Header.Get("X-Request-ID"), "result.submitted", matchID,
		map[string]any{"submissionId": submission.ID, "matchId": matchID, "matchVersion": nextVersion}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record the result event.")
		return
	}
	responseBody, _ := json.Marshal(map[string]any{"data": submission})
	if err = finishIdempotentRequest(r.Context(), tx, "result-submit:"+userID+":"+matchID, idempotencyKey, http.StatusCreated, responseBody); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to submit the result.")
		return
	}
	writeResultRawJSON(w, http.StatusCreated, responseBody)
}

func (s *Server) decideResultSubmission(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	submissionID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(submissionID) {
		writeError(w, http.StatusNotFound, "result_submission_not_found", "Result submission not found.")
		return
	}
	var input decideResultInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.ReasonCode = strings.ToLower(strings.TrimSpace(input.ReasonCode))
	input.Note = strings.TrimSpace(input.Note)
	normalizeUUIDList(input.EvidenceIDs)
	if validationErr := validateDecisionInput(input); validationErr != "" {
		writeError(w, http.StatusBadRequest, "invalid_decision", validationErr)
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to record the result decision.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record the result decision.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	scope := "result-decision:" + userID + ":" + submissionID
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another decision.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record the result decision.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}

	locked, err := lockSubmissionForDecision(r.Context(), tx, submissionID, userID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!locked.ActorHome && !locked.ActorAway)) {
		writeError(w, http.StatusNotFound, "result_submission_not_found", "Result submission not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the result submission.")
		return
	}
	actorIsSubmitterSide := locked.ActorHome && locked.SubmitterHome || locked.ActorAway && locked.SubmitterAway
	if userID == locked.Submission.SubmittedBy || actorIsSubmitterSide {
		writeError(w, http.StatusForbidden, "opponent_decision_required", "Only the opposing player can confirm or dispute this result.")
		return
	}
	if locked.Submission.Status != "pending_confirmation" || locked.Match.State != "awaiting_confirmation" || locked.Match.Version != locked.SubmissionMatchVersion+1 {
		writeError(w, http.StatusConflict, "result_not_pending", "This result is no longer awaiting an opponent decision.")
		return
	}
	if input.Decision == "dispute" && len(input.EvidenceIDs) > 0 {
		if err = lockCompletedEvidence(r.Context(), tx, input.EvidenceIDs, userID); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "evidence_not_ready", "Every dispute evidence item must be completed, owned by you and unused.")
			return
		} else if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the dispute evidence.")
			return
		}
	}

	_, err = tx.Exec(r.Context(), `INSERT INTO result_confirmations(submission_id,user_id,decision,note,reason_code)
		VALUES ($1,$2,$3,$4,NULLIF($5,''))`, submissionID, userID, input.Decision, input.Note, input.ReasonCode)
	if err != nil {
		writeError(w, http.StatusConflict, "decision_already_recorded", "You already decided this result.")
		return
	}
	for position, evidenceID := range input.EvidenceIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO result_confirmation_evidence(submission_id,user_id,evidence_id,position)
			VALUES ($1,$2,$3,$4)`, submissionID, userID, evidenceID, position); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to attach the dispute evidence.")
			return
		}
		command, bindErr := tx.Exec(r.Context(), `UPDATE evidence_uploads SET bound_kind='result_dispute',bound_id=$1,bound_at=now(),updated_at=now()
			WHERE id=$2 AND bound_id IS NULL AND status='completed'`, submissionID, evidenceID)
		if bindErr != nil || command.RowsAffected() != 1 {
			writeError(w, http.StatusConflict, "evidence_already_used", "An evidence item is already attached to another result.")
			return
		}
	}

	decisionTime := time.Now().UTC()
	locked.Submission.DecidedAt = &decisionTime
	locked.Submission.DecidedBy = &userID
	locked.Match.Version++
	var ratingChanges []ratingChangeView
	if input.Decision == "confirm" {
		locked.Submission.Status = "confirmed"
		locked.Match.State = "completed"
		locked.Match.CompletedAt = &decisionTime
		homeWon, awayWon := resultWinner(locked.Submission.HomeScore, locked.Submission.AwayScore, locked.Submission.Tiebreak)
		if homeWon {
			locked.Match.WinnerEntryID = locked.HomeEntryID
		} else if awayWon {
			locked.Match.WinnerEntryID = locked.AwayEntryID
		}
		_, err = tx.Exec(r.Context(), `UPDATE result_submissions SET status='confirmed',decided_at=now(),decided_by=$1 WHERE id=$2 AND status='pending_confirmation'`, userID, submissionID)
		if err == nil {
			command, updateErr := tx.Exec(r.Context(), `UPDATE matches SET state='completed',winner_entry_id=$1,completed_at=now(),version=version+1,updated_at=now()
				WHERE id=$2 AND version=$3 AND state='awaiting_confirmation'`, locked.Match.WinnerEntryID, locked.Match.ID, locked.Match.Version-1)
			err = updateErr
			if err == nil && command.RowsAffected() != 1 {
				err = errors.New("match version changed")
			}
		}
		if err == nil {
			ratingChanges, err = applyConfirmedResultRatings(r.Context(), tx, locked, decisionTime)
		}
		if err == nil {
			err = writeResultAuditAndOutbox(r.Context(), tx, locked.OrganizationID, userID, "result.confirmed", submissionID, r.Header.Get("X-Request-ID"), "match.result_confirmed", locked.Match.ID,
				map[string]any{"submissionId": submissionID, "matchId": locked.Match.ID, "winnerEntryId": locked.Match.WinnerEntryID, "matchVersion": locked.Match.Version, "ratingChanges": ratingChanges})
		}
	} else {
		locked.Submission.Status = "disputed"
		locked.Match.State = "disputed"
		_, err = tx.Exec(r.Context(), `UPDATE result_submissions SET status='disputed',decided_at=now(),decided_by=$1 WHERE id=$2 AND status='pending_confirmation'`, userID, submissionID)
		if err == nil {
			command, updateErr := tx.Exec(r.Context(), `UPDATE matches SET state='disputed',version=version+1,updated_at=now()
				WHERE id=$1 AND version=$2 AND state='awaiting_confirmation'`, locked.Match.ID, locked.Match.Version-1)
			err = updateErr
			if err == nil && command.RowsAffected() != 1 {
				err = errors.New("match version changed")
			}
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO disputes(match_id,submission_id,opened_by,reason_code,description,status)
				VALUES ($1,$2,$3,$4,$5,'open')`, locked.Match.ID, submissionID, userID, input.ReasonCode, input.Note)
		}
		if err == nil {
			err = writeResultAuditAndOutbox(r.Context(), tx, locked.OrganizationID, userID, "result.disputed", submissionID, r.Header.Get("X-Request-ID"), "result.disputed", locked.Match.ID,
				map[string]any{"submissionId": submissionID, "matchId": locked.Match.ID, "reasonCode": input.ReasonCode, "matchVersion": locked.Match.Version})
		}
	}
	if err != nil {
		writeError(w, http.StatusConflict, "result_changed", "The result changed while the decision was being recorded.")
		return
	}
	roomRecord, roomErr := loadMatchRecord(r.Context(), tx, userID, locked.Match.ID, false)
	if roomErr != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the updated match.")
		return
	}
	response := resultDecisionView{Match: roomRecord.response(userID, decisionTime), Submission: locked.Submission, RatingChanges: ratingChanges}
	responseBody, _ := json.Marshal(map[string]any{"data": response})
	if err = finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusOK, responseBody); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record the result decision.")
		return
	}
	writeResultRawJSON(w, http.StatusOK, responseBody)
}

type lockedMatch struct {
	ID             string
	OrganizationID string
	HomeEntryID    *string
	AwayEntryID    *string
	State          string
	ResultDueAt    *time.Time
	Version        int
	BestOf         int
	Format         string
	ActorHome      bool
	ActorAway      bool
}

func lockMatchForResult(ctx context.Context, tx pgx.Tx, matchID, actorID string) (lockedMatch, error) {
	var result lockedMatch
	result.ID = matchID
	err := tx.QueryRow(ctx, `SELECT competition.organization_id,match.home_entry_id,match.away_entry_id,match.state,
		match.result_due_at,match.version,stage.best_of,stage.format,
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.home_entry_id AND member.user_id=$2),
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.away_entry_id AND member.user_id=$2)
		FROM matches match
		JOIN competition_stages stage ON stage.id=match.stage_id
		JOIN competitions competition ON competition.id=match.competition_id
		WHERE match.id=$1 FOR UPDATE OF match`, matchID, actorID).
		Scan(&result.OrganizationID, &result.HomeEntryID, &result.AwayEntryID, &result.State, &result.ResultDueAt,
			&result.Version, &result.BestOf, &result.Format, &result.ActorHome, &result.ActorAway)
	return result, err
}

type lockedSubmission struct {
	Submission             resultSubmissionView
	SubmissionMatchVersion int
	Match                  resultMatchStateView
	OrganizationID         string
	GameID                 string
	HomeEntryID            *string
	AwayEntryID            *string
	HomePlayerID           string
	AwayPlayerID           string
	ActorHome              bool
	ActorAway              bool
	SubmitterHome          bool
	SubmitterAway          bool
}

func lockSubmissionForDecision(ctx context.Context, tx pgx.Tx, submissionID, actorID string) (lockedSubmission, error) {
	var result lockedSubmission
	var gamesJSON, evidenceJSON []byte
	var tiebreakType *string
	var homeTiebreakScore, awayTiebreakScore *int
	err := tx.QueryRow(ctx, `SELECT submission.id,submission.match_id,submission.submitted_by,submission.home_score,submission.away_score,
		submission.tiebreak_type,submission.home_tiebreak_score,submission.away_tiebreak_score,
		submission.game_results,submission.evidence_objects,submission.status,submission.submitted_at,submission.decided_at,submission.decided_by,
		submission.match_version,match.state,match.version,match.winner_entry_id,match.completed_at,competition.organization_id,competition.game_id,
		match.home_entry_id,match.away_entry_id,home_entry.captain_user_id,away_entry.captain_user_id,
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.home_entry_id AND member.user_id=$2),
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.away_entry_id AND member.user_id=$2),
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.home_entry_id AND member.user_id=submission.submitted_by),
		EXISTS (SELECT 1 FROM entry_members member WHERE member.entry_id=match.away_entry_id AND member.user_id=submission.submitted_by)
		FROM result_submissions submission
		JOIN matches match ON match.id=submission.match_id
		JOIN competitions competition ON competition.id=match.competition_id
		JOIN competition_entries home_entry ON home_entry.id=match.home_entry_id
		JOIN competition_entries away_entry ON away_entry.id=match.away_entry_id
		WHERE submission.id=$1 FOR UPDATE OF submission,match`, submissionID, actorID).
		Scan(&result.Submission.ID, &result.Submission.MatchID, &result.Submission.SubmittedBy,
			&result.Submission.HomeScore, &result.Submission.AwayScore, &tiebreakType, &homeTiebreakScore, &awayTiebreakScore,
			&gamesJSON, &evidenceJSON,
			&result.Submission.Status, &result.Submission.SubmittedAt, &result.Submission.DecidedAt, &result.Submission.DecidedBy,
			&result.SubmissionMatchVersion, &result.Match.State, &result.Match.Version, &result.Match.WinnerEntryID,
			&result.Match.CompletedAt, &result.OrganizationID, &result.GameID, &result.HomeEntryID, &result.AwayEntryID,
			&result.HomePlayerID, &result.AwayPlayerID,
			&result.ActorHome, &result.ActorAway, &result.SubmitterHome, &result.SubmitterAway)
	if err != nil {
		return result, err
	}
	if tiebreakType != nil && homeTiebreakScore != nil && awayTiebreakScore != nil {
		result.Submission.Tiebreak = &tiebreakScoreInput{Type: *tiebreakType, HomeScore: *homeTiebreakScore, AwayScore: *awayTiebreakScore}
	}
	result.Match.ID = result.Submission.MatchID
	if err = json.Unmarshal(gamesJSON, &result.Submission.Games); err != nil {
		return result, err
	}
	if err = json.Unmarshal(evidenceJSON, &result.Submission.EvidenceIDs); err != nil {
		return result, err
	}
	return result, nil
}

func applyConfirmedResultRatings(ctx context.Context, tx pgx.Tx, result lockedSubmission, occurredAt time.Time) ([]ratingChangeView, error) {
	playerIDs := []string{result.HomePlayerID, result.AwayPlayerID}
	sort.Strings(playerIDs)
	for _, playerID := range playerIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO player_game_ratings(user_id,game_id)
			VALUES ($1,$2) ON CONFLICT (user_id,game_id) DO NOTHING`, playerID, result.GameID); err != nil {
			return nil, err
		}
	}

	rows, err := tx.Query(ctx, `SELECT user_id::text,rating
		FROM player_game_ratings
		WHERE game_id=$1 AND user_id IN ($2::uuid,$3::uuid)
		ORDER BY user_id FOR UPDATE`, result.GameID, result.HomePlayerID, result.AwayPlayerID)
	if err != nil {
		return nil, err
	}
	ratings := make(map[string]int, 2)
	for rows.Next() {
		var playerID string
		var rating int
		if err = rows.Scan(&playerID, &rating); err != nil {
			rows.Close()
			return nil, err
		}
		ratings[playerID] = rating
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(ratings) != 2 {
		return nil, errors.New("rating rows are unavailable")
	}

	homeScore := 0.5
	homeWin, homeDraw, homeLoss := 0, 1, 0
	awayWin, awayDraw, awayLoss := 0, 1, 0
	homeWon, awayWon := resultWinner(result.Submission.HomeScore, result.Submission.AwayScore, result.Submission.Tiebreak)
	if homeWon {
		homeScore = 1
		homeWin, homeDraw, homeLoss = 1, 0, 0
		awayWin, awayDraw, awayLoss = 0, 0, 1
	} else if awayWon {
		homeScore = 0
		homeWin, homeDraw, homeLoss = 0, 0, 1
		awayWin, awayDraw, awayLoss = 1, 0, 0
	}
	homeBefore := ratings[result.HomePlayerID]
	awayBefore := ratings[result.AwayPlayerID]
	expectedHome := 1 / (1 + math.Pow(10, float64(awayBefore-homeBefore)/400))
	homeDelta := int(math.Round(32 * (homeScore - expectedHome)))
	awayDelta := -homeDelta
	homeAfter := min(10000, max(0, homeBefore+homeDelta))
	awayAfter := min(10000, max(0, awayBefore+awayDelta))

	type update struct {
		playerID            string
		after               int
		wins, draws, losses int
	}
	updates := []update{
		{playerID: result.HomePlayerID, after: homeAfter, wins: homeWin, draws: homeDraw, losses: homeLoss},
		{playerID: result.AwayPlayerID, after: awayAfter, wins: awayWin, draws: awayDraw, losses: awayLoss},
	}
	for _, item := range updates {
		tag, updateErr := tx.Exec(ctx, `UPDATE player_game_ratings
			SET rating=$1,matches_played=matches_played+1,wins=wins+$2,draws=draws+$3,losses=losses+$4,
				rating_version=rating_version+1,last_match_at=$5,updated_at=now()
			WHERE user_id=$6 AND game_id=$7`, item.after, item.wins, item.draws, item.losses, occurredAt, item.playerID, result.GameID)
		if updateErr != nil {
			return nil, updateErr
		}
		if tag.RowsAffected() != 1 {
			return nil, errors.New("rating update did not affect exactly one player")
		}
	}
	return []ratingChangeView{
		{PlayerID: result.HomePlayerID, GameID: result.GameID, Before: homeBefore, After: homeAfter, Delta: homeAfter - homeBefore},
		{PlayerID: result.AwayPlayerID, GameID: result.GameID, Before: awayBefore, After: awayAfter, Delta: awayAfter - awayBefore},
	}, nil
}

func (s *Server) loadEvidence(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, evidenceID, userID string, lock bool) (evidenceRecord, error) {
	query := `SELECT id,object_key,media_type,byte_size,checksum_sha256,status,upload_expires_at,completed_at,bound_kind,bound_id
		FROM evidence_uploads WHERE id=$1 AND owner_user_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var record evidenceRecord
	err := querier.QueryRow(ctx, query, evidenceID, userID).Scan(&record.ID, &record.ObjectKey, &record.MediaType, &record.ByteSize,
		&record.Checksum, &record.Status, &record.UploadExpires, &record.CompletedAt, &record.BoundKind, &record.BoundID)
	return record, err
}

func lockCompletedEvidence(ctx context.Context, tx pgx.Tx, evidenceIDs []string, ownerID string) error {
	seen := make(map[string]struct{}, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		if _, exists := seen[evidenceID]; exists {
			return pgx.ErrNoRows
		}
		seen[evidenceID] = struct{}{}
		var status string
		var boundID *string
		err := tx.QueryRow(ctx, `SELECT status,bound_id FROM evidence_uploads
			WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, evidenceID, ownerID).Scan(&status, &boundID)
		if err != nil {
			return err
		}
		if status != "completed" || boundID != nil {
			return pgx.ErrNoRows
		}
	}
	return nil
}

func validateScorePolicy(input submitResultInput, bestOf int, format string) string {
	if len(input.Games) < 1 || len(input.Games) > bestOf || bestOf < 1 {
		return "The game breakdown does not match this stage's best-of rule."
	}
	homeTotal, awayTotal := 0, 0
	for _, game := range input.Games {
		if game.HomeScore < 0 || game.HomeScore > 99 || game.AwayScore < 0 || game.AwayScore > 99 {
			return "Every game score must be between 0 and 99."
		}
		homeTotal += game.HomeScore
		awayTotal += game.AwayScore
	}
	if homeTotal != input.HomeScore || awayTotal != input.AwayScore {
		return "The final score must equal the submitted game-score totals."
	}
	if input.HomeScore == input.AwayScore {
		if format == "round_robin" {
			if input.Tiebreak != nil {
				return "Round-robin draws cannot include a penalty tiebreak."
			}
			return ""
		}
		if input.Tiebreak == nil || input.Tiebreak.Type != "penalties" || input.Tiebreak.HomeScore < 0 || input.Tiebreak.HomeScore > 99 ||
			input.Tiebreak.AwayScore < 0 || input.Tiebreak.AwayScore > 99 || input.Tiebreak.HomeScore == input.Tiebreak.AwayScore {
			return "This elimination match requires a valid penalty-shootout winner."
		}
		return ""
	}
	if input.Tiebreak != nil {
		return "A penalty tiebreak is only valid when the final score is tied."
	}
	return ""
}

func resultWinner(homeScore, awayScore int, tiebreak *tiebreakScoreInput) (bool, bool) {
	if homeScore > awayScore {
		return true, false
	}
	if awayScore > homeScore {
		return false, true
	}
	if tiebreak == nil {
		return false, false
	}
	return tiebreak.HomeScore > tiebreak.AwayScore, tiebreak.AwayScore > tiebreak.HomeScore
}

func validateDecisionInput(input decideResultInput) string {
	if input.Decision == "confirm" {
		if input.ReasonCode != "" || input.Note != "" || len(input.EvidenceIDs) != 0 {
			return "A confirmation only accepts the confirm decision."
		}
		return ""
	}
	if input.Decision != "dispute" {
		return "Decision must be confirm or dispute."
	}
	allowedReason := input.ReasonCode == "score_mismatch" || input.ReasonCode == "invalid_evidence" || input.ReasonCode == "match_not_played" || input.ReasonCode == "other"
	if !allowedReason || utf8.RuneCountInString(input.Note) < 10 || utf8.RuneCountInString(input.Note) > 1000 || len(input.EvidenceIDs) > 5 || !validUUIDList(input.EvidenceIDs) {
		return "A dispute needs a valid reason, a 10-1000 character note and up to five completed evidence IDs."
	}
	return ""
}

func validUUIDList(values []string) bool {
	for _, value := range values {
		if !uuidPattern.MatchString(strings.TrimSpace(value)) {
			return false
		}
	}
	return true
}

func normalizeUUIDList(values []string) {
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
}

func readIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Provide an Idempotency-Key between 8 and 128 characters.")
		return "", false
	}
	for _, character := range key {
		if character < 33 || character > 126 {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must use printable ASCII without spaces.")
			return "", false
		}
	}
	return key, true
}

var errIdempotencyConflict = errors.New("idempotency key request mismatch")

type idempotentReplay struct {
	Status int
	Body   []byte
}

func beginIdempotentRequest(ctx context.Context, tx pgx.Tx, scope, key, requestHash string) (*idempotentReplay, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope=$1 AND key=$2 AND expires_at<=now()`, scope, key); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(scope,key,request_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '24 hours') ON CONFLICT DO NOTHING`, scope, key, requestHash); err != nil {
		return nil, err
	}
	var storedHash string
	var responseStatus *int
	var responseBody []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response_status,response_body FROM idempotency_keys
		WHERE scope=$1 AND key=$2 FOR UPDATE`, scope, key).Scan(&storedHash, &responseStatus, &responseBody)
	if err != nil {
		return nil, err
	}
	if storedHash != requestHash {
		return nil, errIdempotencyConflict
	}
	if responseStatus != nil && len(responseBody) > 0 {
		return &idempotentReplay{Status: *responseStatus, Body: responseBody}, nil
	}
	return nil, nil
}

func finishIdempotentRequest(ctx context.Context, tx pgx.Tx, scope, key string, status int, body []byte) error {
	_, err := tx.Exec(ctx, `UPDATE idempotency_keys SET response_status=$1,response_body=$2
		WHERE scope=$3 AND key=$4`, status, json.RawMessage(body), scope, key)
	return err
}

func writeResultAuditAndOutbox(ctx context.Context, tx pgx.Tx, organizationID, actorID, action, subjectID, requestID, eventType, aggregateID string, payload map[string]any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,subject_id,request_id,after_state)
		VALUES ($1,$2,$3,'result_submission',$4,$5,$6)`, organizationID, actorID, action, subjectID, requestID, json.RawMessage(payloadJSON)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('match',$1,$2,$3)`, aggregateID, eventType, json.RawMessage(payloadJSON))
	return err
}

func hashRequest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func writeResultRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func storageChecksumBase64(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}
