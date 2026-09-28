package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type gameAccountVerificationInput struct {
	EvidenceIDs []string `json:"evidenceIds"`
	Note        string   `json:"note"`
}

type gameAccountVerificationDecisionInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func gameAccountVerificationDecisionScope(actorID, requestID string) string {
	return "game-account-verification-decision:" + actorID + ":" + requestID
}

type gameAccountVerificationView struct {
	ID                string     `json:"id"`
	GameAccountID     string     `json:"gameAccountId"`
	UserID            string     `json:"userId"`
	Method            string     `json:"method"`
	Status            string     `json:"status"`
	PlayerNote        string     `json:"playerNote"`
	DecisionReason    string     `json:"decisionReason"`
	PublisherVerified bool       `json:"publisherVerified"`
	EvidenceIDs       []string   `json:"evidenceIds"`
	RequestedAt       time.Time  `json:"requestedAt"`
	ReviewedAt        *time.Time `json:"reviewedAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (s *Server) registerGameAccountVerificationRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/me/game-accounts/{id}/verification-requests",
		s.requireAuth(http.HandlerFunc(s.requestGameAccountVerification)))
	mux.Handle("GET /v1/me/game-accounts/{id}/verification",
		s.requireAuth(http.HandlerFunc(s.getGameAccountVerification)))
	mux.Handle("DELETE /v1/me/game-accounts/{id}/verification-requests/{requestId}",
		s.requireAuth(http.HandlerFunc(s.withdrawGameAccountVerification)))
	mux.Handle("GET /v1/admin/game-account-verifications",
		s.platformRoute(platformVerificationManage, s.listGameAccountVerificationQueue))
	mux.Handle("POST /v1/admin/game-account-verifications/{id}/decisions",
		s.platformRoute(platformVerificationManage, s.decideGameAccountVerification))
}

func (s *Server) requestGameAccountVerification(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	accountID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(accountID) {
		writeError(w, http.StatusNotFound, "game_account_not_found", "Game account not found.")
		return
	}
	var input gameAccountVerificationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Note = strings.TrimSpace(input.Note)
	normalizeUUIDList(input.EvidenceIDs)
	if len(input.EvidenceIDs) < 1 || len(input.EvidenceIDs) > 3 || !validUUIDList(input.EvidenceIDs) || len(input.Note) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_verification_request", "Attach one to three completed evidence items and an optional 500-character note.")
		return
	}
	requestHash, _ := hashRequest(input)
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request account verification.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	scope := "game-account-verification:" + userID + ":" + accountID
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another verification request.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request account verification.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	var publisherPlayerID *string
	var accountStatus string
	err = tx.QueryRow(r.Context(), `SELECT publisher_player_id,verification_status FROM game_accounts
		WHERE id=$1 AND user_id=$2 FOR UPDATE`, accountID, userID).Scan(&publisherPlayerID, &accountStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "game_account_not_found", "Game account not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the game account.")
		return
	}
	if publisherPlayerID == nil || strings.TrimSpace(*publisherPlayerID) == "" {
		writeError(w, http.StatusConflict, "publisher_player_id_required", "Add the publisher player ID before requesting verification.")
		return
	}
	if accountStatus == "verified" {
		writeError(w, http.StatusConflict, "game_account_already_verified", "This game account is already verified.")
		return
	}
	if err = lockCompletedEvidence(r.Context(), tx, input.EvidenceIDs, userID, screenshotMediaTypes); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "evidence_not_ready", "Every evidence item must be completed, owned by you and unused.")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the evidence.")
		return
	}
	requestID, err := randomUUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create the verification request.")
		return
	}
	var view gameAccountVerificationView
	err = tx.QueryRow(r.Context(), `INSERT INTO game_account_verification_requests
		(id,game_account_id,user_id,method,status,player_note)
		VALUES ($1,$2,$3,'manual_evidence','requested',$4)
		RETURNING id,game_account_id,user_id,method,status,player_note,decision_reason,publisher_verified,
		requested_at,reviewed_at,updated_at`, requestID, accountID, userID, input.Note).
		Scan(verificationScanTargets(&view)...)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "verification_request_exists", "An active verification request already exists.")
		} else {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the verification request.")
		}
		return
	}
	view.EvidenceIDs = append([]string(nil), input.EvidenceIDs...)
	for position, evidenceID := range input.EvidenceIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO game_account_verification_evidence(request_id,evidence_id,position)
			VALUES ($1,$2,$3)`, requestID, evidenceID, position); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to attach verification evidence.")
			return
		}
		command, bindErr := tx.Exec(r.Context(), `UPDATE evidence_uploads SET bound_kind='game_account_verification',
			bound_id=$1,bound_at=now(),updated_at=now() WHERE id=$2 AND owner_user_id=$3
			AND status='completed' AND bound_id IS NULL`, requestID, evidenceID, userID)
		if bindErr != nil || command.RowsAffected() != 1 {
			writeError(w, http.StatusConflict, "evidence_already_used", "An evidence item is already attached elsewhere.")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE game_accounts SET verification_status='pending',
		verification_method='manual_evidence',publisher_verified=false,verified_at=NULL,updated_at=now()
		WHERE id=$1 AND user_id=$2`, accountID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to mark the account pending.")
		return
	}
	if err = appendPlatformAudit(r, tx, userID, "game_account.verification_requested", "game_account_verification", requestID,
		nil, map[string]any{"gameAccountId": accountID, "status": view.Status}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the verification request.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('game_account_verification',$1,'game_account.verification_requested',
		jsonb_build_object('requestId',$1::text,'gameAccountId',$2::text,'userId',$3::text))`, requestID, accountID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the verification request.")
		return
	}
	body, _ := json.Marshal(map[string]any{"data": view})
	if finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusCreated, body) != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the verification request.")
		return
	}
	writeResultRawJSON(w, http.StatusCreated, body)
}

func (s *Server) getGameAccountVerification(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	accountID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(accountID) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	view, err := loadGameAccountVerification(r, s.db.Writer, accountID, identityFromContext(r.Context()).UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load account verification.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": view})
}

func (s *Server) withdrawGameAccountVerification(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	accountID, requestID := strings.TrimSpace(r.PathValue("id")), strings.TrimSpace(r.PathValue("requestId"))
	if !uuidPattern.MatchString(accountID) || !uuidPattern.MatchString(requestID) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	// Keep the lock order consistent with account edits and review decisions:
	// game account first, verification request second.
	var accountExists bool
	if err = tx.QueryRow(r.Context(), `SELECT true FROM game_accounts
		WHERE id=$1 AND user_id=$2 FOR UPDATE`, accountID, userID).Scan(&accountExists); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	var previous string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM game_account_verification_requests
		WHERE id=$1 AND game_account_id=$2 AND user_id=$3 FOR UPDATE`, requestID, accountID, userID).Scan(&previous); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "verification_not_withdrawable", "This verification request can no longer be withdrawn.")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	if previous != "requested" && previous != "under_review" {
		writeError(w, http.StatusConflict, "verification_not_withdrawable", "This verification request can no longer be withdrawn.")
		return
	}
	command, err := tx.Exec(r.Context(), `UPDATE game_account_verification_requests SET status='withdrawn',updated_at=now()
		WHERE id=$1 AND game_account_id=$2 AND user_id=$3 AND status=$4`, requestID, accountID, userID, previous)
	if err != nil || command.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "verification_not_withdrawable", "This verification request can no longer be withdrawn.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE game_accounts SET verification_status='unverified',verification_method=NULL,
		publisher_verified=false,verified_at=NULL,updated_at=now() WHERE id=$1 AND user_id=$2`, accountID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	if err = appendPlatformAudit(r, tx, userID, "game_account.verification_withdrawn", "game_account_verification", requestID,
		map[string]any{"status": previous}, map[string]any{"status": "withdrawn"}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('game_account_verification',$1,'game_account.verification_withdrawn',
		jsonb_build_object('requestId',$1::text,'gameAccountId',$2::text,'userId',$3::text))`,
		requestID, accountID, userID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to withdraw verification.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGameAccountVerificationQueue(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "requested"
	}
	if status != "requested" && status != "under_review" && status != "approved" && status != "rejected" {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose a valid verification status.")
		return
	}
	limit, cursor, ok := s.staffQueuePageInput(w, r, "game-account-verification-queue", status)
	if !ok {
		return
	}
	var afterTime *time.Time
	var afterID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		afterTime, afterID = &value, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), verificationSelect+`WHERE request.status=$1
		AND ($2::timestamptz IS NULL OR (request.requested_at,request.id)>($2,$3::uuid))
		ORDER BY request.requested_at,request.id LIMIT $4`, status, afterTime, afterID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the verification queue.")
		return
	}
	defer rows.Close()
	data := []gameAccountVerificationView{}
	for rows.Next() {
		view, scanErr := scanGameAccountVerification(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the verification queue.")
			return
		}
		data = append(data, view)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the verification queue.")
		return
	}
	page := staffQueuePage{}
	if len(data) > limit {
		data = data[:limit]
		page.HasMore = true
		last := data[len(data)-1]
		next, encodeErr := s.encodeStaffQueueCursor("game-account-verification-queue", status,
			identityFromContext(r.Context()).UserID, last.RequestedAt, last.ID)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate the verification queue.")
			return
		}
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": page})
}

func (s *Server) decideGameAccountVerification(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(requestID) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	var input gameAccountVerificationDecisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Decision, input.Reason = strings.TrimSpace(input.Decision), strings.TrimSpace(input.Reason)
	if (input.Decision != "approve" && input.Decision != "reject") || len(input.Reason) > 1000 ||
		(input.Decision == "reject" && input.Reason == "") {
		writeError(w, http.StatusBadRequest, "invalid_verification_decision", "Choose approve or reject; rejection requires a reason.")
		return
	}
	requestHash, _ := hashRequest(input)
	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide verification.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	scope := gameAccountVerificationDecisionScope(actorID, requestID)
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another verification decision.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide verification.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	var lockedGameAccountID string
	err = tx.QueryRow(r.Context(), `SELECT account.id
		FROM game_account_verification_requests request
		JOIN game_accounts account ON account.id=request.game_account_id
		WHERE request.id=$1 FOR UPDATE OF account`, requestID).Scan(&lockedGameAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load verification.")
		return
	}
	var view gameAccountVerificationView
	view, err = scanGameAccountVerification(tx.QueryRow(r.Context(), verificationSelect+`WHERE request.id=$1 FOR UPDATE OF request`, requestID))
	previous := view.Status
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "verification_not_found", "Verification request not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load verification.")
		return
	}
	if view.GameAccountID != lockedGameAccountID {
		writeError(w, http.StatusConflict, "verification_changed", "This verification request changed while it was being reviewed.")
		return
	}
	if previous != "requested" && previous != "under_review" {
		writeError(w, http.StatusConflict, "verification_not_pending", "This verification request has already been decided.")
		return
	}
	next := "approved"
	accountStatus := "verified"
	if input.Decision == "reject" {
		next, accountStatus = "rejected", "rejected"
	}
	err = tx.QueryRow(r.Context(), `UPDATE game_account_verification_requests SET status=$2,decision_reason=$3,
		publisher_verified=false,reviewed_by=$4,reviewed_at=now(),updated_at=now() WHERE id=$1
		RETURNING id,game_account_id,user_id,method,status,player_note,decision_reason,publisher_verified,
		requested_at,reviewed_at,updated_at`, requestID, next, input.Reason, actorID).Scan(verificationScanTargets(&view)...)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save verification.")
		return
	}
	view.EvidenceIDs, err = loadVerificationEvidenceIDs(r, tx, requestID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load verification evidence.")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE game_accounts SET verification_status=$2,verification_method='manual_evidence',
		publisher_verified=false,verified_at=CASE WHEN $2='verified' THEN now() ELSE NULL END,updated_at=now()
		WHERE id=$1`, view.GameAccountID, accountStatus)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the game account.")
		return
	}
	if err = appendPlatformAudit(r, tx, actorID, "game_account.verification_"+next, "game_account_verification", requestID,
		map[string]any{"status": previous}, map[string]any{"status": next, "reason": input.Reason}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit verification.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('game_account_verification',$1,$2,jsonb_build_object('requestId',$1::text,
		'gameAccountId',$3::text,'userId',$4::text,'status',$5::text))`, requestID,
		"game_account.verification_"+next, view.GameAccountID, view.UserID, next); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue verification.")
		return
	}
	body, _ := json.Marshal(map[string]any{"data": view})
	if finishIdempotentRequest(r.Context(), tx, scope,
		idempotencyKey, http.StatusOK, body) != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide verification.")
		return
	}
	writeResultRawJSON(w, http.StatusOK, body)
}

const verificationSelect = `SELECT request.id,request.game_account_id,request.user_id,request.method,request.status,
	request.player_note,request.decision_reason,request.publisher_verified,request.requested_at,request.reviewed_at,
	request.updated_at,COALESCE((SELECT jsonb_agg(link.evidence_id ORDER BY link.position)
	FROM game_account_verification_evidence link WHERE link.request_id=request.id),'[]'::jsonb)
	FROM game_account_verification_requests request `

func verificationScanTargets(value *gameAccountVerificationView) []any {
	return []any{&value.ID, &value.GameAccountID, &value.UserID, &value.Method, &value.Status, &value.PlayerNote,
		&value.DecisionReason, &value.PublisherVerified, &value.RequestedAt, &value.ReviewedAt, &value.UpdatedAt}
}

func scanGameAccountVerification(row accountScanner) (gameAccountVerificationView, error) {
	var value gameAccountVerificationView
	var evidenceJSON []byte
	err := row.Scan(append(verificationScanTargets(&value), &evidenceJSON)...)
	if err == nil {
		value.EvidenceIDs = []string{}
		err = json.Unmarshal(evidenceJSON, &value.EvidenceIDs)
	}
	return value, err
}

func loadGameAccountVerification(r *http.Request, queryer queryRower, accountID, userID string) (gameAccountVerificationView, error) {
	return scanGameAccountVerification(queryer.QueryRow(r.Context(), verificationSelect+`
		WHERE request.game_account_id=$1 AND request.user_id=$2
		ORDER BY request.requested_at DESC,request.id DESC LIMIT 1`, accountID, userID))
}

func loadVerificationEvidenceIDs(r *http.Request, queryer queryRower, requestID string) ([]string, error) {
	var raw []byte
	err := queryer.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(evidence_id ORDER BY position),'[]'::jsonb)
		FROM game_account_verification_evidence WHERE request_id=$1`, requestID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	values := []string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func appendPlatformAudit(r *http.Request, tx pgx.Tx, actorID, action, subjectType, subjectID string,
	before, after map[string]any) error {
	return appendPlatformAuditContext(r.Context(), tx, r.Header.Get("X-Request-ID"), &actorID, action, subjectType,
		subjectID, before, after)
}

// appendPlatformAuditContext is the one Gamics staff audit writer; its rows
// belong to no organization. A nil actor records an automated decision, such
// as a future system result reviewer, with actor_user_id NULL.
func appendPlatformAuditContext(ctx context.Context, tx pgx.Tx, requestID string, actorID *string,
	action, subjectType, subjectID string, before, after map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,
		subject_id,request_id,before_state,after_state) VALUES (NULL,$1,$2,$3,$4,$5,$6,$7)`,
		actorID, action, subjectType, subjectID, requestID, auditState(before), auditState(after))
	return err
}
