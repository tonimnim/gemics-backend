package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const paymentCursorTTL = 15 * time.Minute

var paymentHistoryStatuses = map[string]struct{}{
	"initiating": {}, "pending": {}, "callback_received": {}, "succeeded": {}, "failed": {}, "review": {},
}

var refundStatuses = map[string]struct{}{
	"requested": {}, "approved": {}, "processing": {}, "manual_review": {}, "succeeded": {}, "rejected": {}, "failed": {},
}

type paymentRefundView struct {
	ID                        string     `json:"id"`
	PaymentID                 string     `json:"paymentId"`
	EntryID                   string     `json:"entryId"`
	AmountMinor               int64      `json:"amountMinor"`
	Currency                  string     `json:"currency"`
	ReasonCode                string     `json:"reasonCode"`
	Mandatory                 bool       `json:"mandatory"`
	PlayerNote                string     `json:"playerNote"`
	Status                    string     `json:"status"`
	ProviderReceipt           *string    `json:"providerReceipt"`
	ProviderResultDescription *string    `json:"providerResultDescription"`
	RequestedAt               time.Time  `json:"requestedAt"`
	ReviewedAt                *time.Time `json:"reviewedAt"`
	CompletedAt               *time.Time `json:"completedAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
}

type paymentHistoryItem struct {
	paymentIntent
	Refund *paymentRefundView `json:"refund"`
}

type paymentHistoryPage struct {
	Data []paymentHistoryItem `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type refundPage struct {
	Data []paymentRefundView `json:"data"`
	Page struct {
		NextCursor *string `json:"nextCursor"`
		HasMore    bool    `json:"hasMore"`
	} `json:"page"`
}

type staffQueuePage struct {
	NextCursor *string `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

type paidWithdrawalInput struct {
	ReasonCode string `json:"reasonCode"`
	Note       string `json:"note"`
}

type refundDecisionInput struct {
	Decision        string `json:"decision"`
	Note            string `json:"note"`
	ProviderReceipt string `json:"providerReceipt"`
}

func refundDecisionScope(actorID, refundID string) string {
	return "refund-decision:" + actorID + ":" + refundID
}

func (s *Server) registerPaymentLifecycleRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/me/payments", s.requireAuth(http.HandlerFunc(s.listMyPayments)))
	mux.Handle("GET /v1/me/refunds", s.requireAuth(http.HandlerFunc(s.listMyRefunds)))
	mux.Handle("POST /v1/competitions/{id}/registrations/me/withdrawal-requests",
		s.requireAuth(http.HandlerFunc(s.requestPaidWithdrawal)))
	mux.Handle("GET /v1/admin/refunds", s.platformRoute(platformRefundView, s.listRefundQueue))
	mux.Handle("POST /v1/admin/refunds/{id}/decisions", s.platformRoute(platformRefundManage, s.decideRefund))
}

func (s *Server) listMyPayments(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" {
		if _, valid := paymentHistoryStatuses[status]; !valid {
			writeError(w, http.StatusBadRequest, "invalid_status", "Choose a valid payment status.")
			return
		}
	}
	limit, cursor, ok := s.paymentPageInput(w, r, "my-payments", userID, status)
	if !ok {
		return
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		beforeTime, beforeID = &value, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), `SELECT payment.id,payment.competition_id,payment.entry_id,
		payment.amount_minor,payment.currency,payment.phone_e164,payment.status,payment.merchant_request_id,
		payment.checkout_request_id,payment.provider_receipt,payment.provider_result_code,
		payment.provider_result_description,payment.created_at,payment.updated_at,payment.completed_at,
		payment.query_attempts,payment.next_query_at,
		refund.id,refund.payment_id,refund.entry_id,refund.amount_minor,refund.currency,refund.reason_code,
		refund.mandatory,refund.player_note,refund.status,refund.provider_receipt,refund.provider_result_description,
		refund.requested_at,refund.reviewed_at,refund.completed_at,refund.updated_at
		FROM payment_intents payment
		LEFT JOIN LATERAL (
			SELECT * FROM payment_refunds candidate WHERE candidate.payment_id=payment.id
			ORDER BY candidate.requested_at DESC,candidate.id DESC LIMIT 1
		) refund ON true
		WHERE payment.user_id=$1 AND ($2='' OR payment.status=$2)
		AND ($3::timestamptz IS NULL OR (payment.created_at,payment.id)<($3,$4::uuid))
		ORDER BY payment.created_at DESC,payment.id DESC LIMIT $5`,
		userID, status, beforeTime, beforeID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment history.")
		return
	}
	defer rows.Close()
	page := paymentHistoryPage{Data: []paymentHistoryItem{}}
	for rows.Next() {
		item, scanErr := scanPaymentHistoryItem(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment history.")
			return
		}
		page.Data = append(page.Data, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment history.")
		return
	}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next, encodeErr := encodePublicCursor(publicCursor{Kind: "my-payments", ExpiresAt: time.Now().Add(paymentCursorTTL).Unix(),
			Query: userID, Scope: paymentCursorScope(status), SortTime: last.CreatedAt.UnixNano(), ID: last.ID}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate payment history.")
			return
		}
		page.Page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listMyRefunds(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" {
		if _, valid := refundStatuses[status]; !valid {
			writeError(w, http.StatusBadRequest, "invalid_status", "Choose a valid refund status.")
			return
		}
	}
	limit, cursor, ok := s.paymentPageInput(w, r, "my-refunds", userID, status)
	if !ok {
		return
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		beforeTime, beforeID = &value, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), refundSelect+`
		WHERE refund.user_id=$1 AND ($2='' OR refund.status=$2)
		AND ($3::timestamptz IS NULL OR (refund.requested_at,refund.id)<($3,$4::uuid))
		ORDER BY refund.requested_at DESC,refund.id DESC LIMIT $5`,
		userID, status, beforeTime, beforeID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load refunds.")
		return
	}
	defer rows.Close()
	page := refundPage{Data: []paymentRefundView{}}
	for rows.Next() {
		item, scanErr := scanRefund(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load refunds.")
			return
		}
		page.Data = append(page.Data, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load refunds.")
		return
	}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.Page.HasMore = true
		last := page.Data[len(page.Data)-1]
		next, encodeErr := encodePublicCursor(publicCursor{Kind: "my-refunds", ExpiresAt: time.Now().Add(paymentCursorTTL).Unix(),
			Query: userID, Scope: paymentCursorScope(status), SortTime: last.RequestedAt.UnixNano(), ID: last.ID}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate refunds.")
			return
		}
		page.Page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) paymentPageInput(w http.ResponseWriter, r *http.Request, kind, userID, status string) (int, *publicCursor, bool) {
	limit, err := parsePublicLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return 0, nil, false
	}
	raw := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if raw == "" {
		return limit, nil, true
	}
	decoded, err := decodePublicCursor(raw, kind, s.config.AccessTokenSecret, time.Now())
	if err != nil || !uuidPattern.MatchString(decoded.ID) || decoded.SortTime <= 0 ||
		decoded.Query != userID || decoded.Scope != paymentCursorScope(status) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid or expired.")
		return 0, nil, false
	}
	return limit, &decoded, true
}

func paymentCursorScope(status string) string {
	if status == "" {
		return "all"
	}
	return status
}

// probePaidCompetitionEntry is the unlocked probe that runs before the
// competition gate: a caller without a paid entry gets pgx.ErrNoRows, so spam
// withdrawal requests never queue behind a competition's finalization (D28).
func probePaidCompetitionEntry(ctx context.Context, tx pgx.Tx, competitionID, userID string) error {
	var found int
	return tx.QueryRow(ctx, `SELECT 1 FROM competition_entries entry
		JOIN payment_intents payment ON payment.entry_id=entry.id AND payment.user_id=entry.captain_user_id
		WHERE entry.competition_id=$1 AND entry.captain_user_id=$2 AND payment.status='succeeded'
		LIMIT 1`, competitionID, userID).Scan(&found)
}

func (s *Server) requestPaidWithdrawal(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	competitionID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(competitionID) {
		writeError(w, http.StatusNotFound, "registration_not_found", "Registration not found.")
		return
	}
	var input paidWithdrawalInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.Note = strings.TrimSpace(input.Note)
	if input.ReasonCode == "" {
		input.ReasonCode = "player_withdrawal"
	}
	if input.ReasonCode != "player_withdrawal" || len(input.Note) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_withdrawal", "Use player_withdrawal and a note of at most 500 characters.")
		return
	}
	requestHash, _ := hashRequest(input)
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request withdrawal.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,23))", userID+":"+competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request withdrawal.")
		return
	}
	scope := "paid-withdrawal:" + userID + ":" + competitionID
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another withdrawal.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request withdrawal.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	// Only a paid entrant may queue on the gate (D28); the locked read below
	// repeats the check authoritatively.
	err = probePaidCompetitionEntry(r.Context(), tx, competitionID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "paid_registration_not_found", "A paid registration was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the paid registration.")
		return
	}
	// The entry and competition locks below would otherwise invert the gate ->
	// entry order of a concurrent result finalizer that removes this entry.
	if err = lockCompetitionProgressionGate(r.Context(), tx, competitionID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request withdrawal.")
		return
	}
	var entryID, entryStatus, organizationID, competitionStatus, paymentID, currency string
	var closesAt time.Time
	var amountMinor int64
	err = tx.QueryRow(r.Context(), `SELECT entry.id,entry.status,competition.organization_id,competition.status,
		competition.registration_closes_at,payment.id,payment.amount_minor,payment.currency
		FROM competition_entries entry
		JOIN competitions competition ON competition.id=entry.competition_id
		JOIN payment_intents payment ON payment.entry_id=entry.id AND payment.user_id=entry.captain_user_id
		WHERE entry.competition_id=$1 AND entry.captain_user_id=$2 AND payment.status='succeeded'
		FOR UPDATE OF entry,competition,payment`, competitionID, userID).
		Scan(&entryID, &entryStatus, &organizationID, &competitionStatus, &closesAt, &paymentID, &amountMinor, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "paid_registration_not_found", "A paid registration was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the paid registration.")
		return
	}
	if entryStatus == "withdrawn" || entryStatus == "disqualified" {
		writeError(w, http.StatusConflict, "withdrawal_closed", "This registration can no longer be withdrawn.")
		return
	}
	if competitionStatus != "cancelled" && !time.Now().Before(closesAt) {
		writeError(w, http.StatusConflict, "withdrawal_closed", "Paid withdrawal requests close when registration closes.")
		return
	}
	refund, err := loadRefundByPayment(r, tx, paymentID, userID)
	if err == nil {
		body, _ := json.Marshal(map[string]any{"data": refund})
		if finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusOK, body) != nil || tx.Commit(r.Context()) != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund request.")
			return
		}
		writeResultRawJSON(w, http.StatusOK, body)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check the refund request.")
		return
	}
	reason := input.ReasonCode
	if competitionStatus == "cancelled" {
		reason = "competition_cancelled"
	}
	mandatory := reason == "competition_cancelled"
	err = tx.QueryRow(r.Context(), `INSERT INTO payment_refunds
		(payment_id,user_id,entry_id,amount_minor,currency,reason_code,mandatory,player_note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id,payment_id,entry_id,amount_minor,currency,reason_code,mandatory,player_note,status,
		provider_receipt,provider_result_description,requested_at,reviewed_at,completed_at,updated_at`,
		paymentID, userID, entryID, amountMinor, currency, reason, mandatory, input.Note).Scan(refundScanTargets(&refund)...)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "refund_request_exists", "A refund request already exists for this payment.")
		} else {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the refund request.")
		}
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE competition_entries SET status='withdrawal_pending',updated_at=now()
		WHERE id=$1 AND status NOT IN ('withdrawn','disqualified')`, entryID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to reserve the withdrawal request.")
		return
	}
	if err = appendAudit(r, tx, organizationID, userID, "payment.refund_requested", "payment_refund", refund.ID,
		nil, map[string]any{"paymentId": paymentID, "entryId": entryID, "status": refund.Status,
			"reasonCode": reason, "mandatory": mandatory}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the refund request.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('payment_refund',$1,'payment.refund_requested',jsonb_build_object('refundId',$1::text,
		'paymentId',$2::text,'entryId',$3::text,'userId',$4::text,'mandatory',$5::boolean))`,
		refund.ID, paymentID, entryID, userID, mandatory); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the refund request.")
		return
	}
	body, _ := json.Marshal(map[string]any{"data": refund})
	if finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusAccepted, body) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request the refund.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to request the refund.")
		return
	}
	s.invalidateCompetitionCachesContext(context.WithoutCancel(r.Context()), competitionID)
	writeResultRawJSON(w, http.StatusAccepted, body)
}

const refundSelect = `SELECT refund.id,refund.payment_id,refund.entry_id,refund.amount_minor,refund.currency,
	refund.reason_code,refund.mandatory,refund.player_note,refund.status,refund.provider_receipt,refund.provider_result_description,
	refund.requested_at,refund.reviewed_at,refund.completed_at,refund.updated_at FROM payment_refunds refund `

func scanPaymentHistoryItem(row accountScanner) (paymentHistoryItem, error) {
	var result paymentHistoryItem
	var phone string
	var refundID, refundPaymentID, refundEntryID, refundCurrency, refundReason, refundNote, refundStatus *string
	var refundMandatory *bool
	var refundReceipt, refundDescription *string
	var refundAmount *int64
	var refundRequested, refundReviewed, refundCompleted, refundUpdated *time.Time
	err := row.Scan(&result.ID, &result.CompetitionID, &result.EntryID, &result.AmountMinor, &result.Currency, &phone,
		&result.Status, &result.MerchantRequestID, &result.CheckoutRequestID, &result.ProviderReceipt,
		&result.ProviderResultCode, &result.ProviderResultDescription, &result.CreatedAt, &result.UpdatedAt,
		&result.CompletedAt, &result.QueryAttempts, &result.NextQueryAt,
		&refundID, &refundPaymentID, &refundEntryID, &refundAmount, &refundCurrency, &refundReason, &refundMandatory, &refundNote,
		&refundStatus, &refundReceipt, &refundDescription, &refundRequested, &refundReviewed, &refundCompleted, &refundUpdated)
	result.MaskedPhone = maskPhone(phone)
	if err == nil && refundID != nil {
		result.Refund = &paymentRefundView{ID: *refundID, PaymentID: *refundPaymentID, EntryID: *refundEntryID,
			AmountMinor: *refundAmount, Currency: *refundCurrency, ReasonCode: *refundReason, Mandatory: *refundMandatory, PlayerNote: *refundNote,
			Status: *refundStatus, ProviderReceipt: refundReceipt, ProviderResultDescription: refundDescription,
			RequestedAt: *refundRequested, ReviewedAt: refundReviewed, CompletedAt: refundCompleted, UpdatedAt: *refundUpdated}
	}
	return result, err
}

func refundScanTargets(value *paymentRefundView) []any {
	return []any{&value.ID, &value.PaymentID, &value.EntryID, &value.AmountMinor, &value.Currency, &value.ReasonCode,
		&value.Mandatory, &value.PlayerNote, &value.Status, &value.ProviderReceipt, &value.ProviderResultDescription, &value.RequestedAt,
		&value.ReviewedAt, &value.CompletedAt, &value.UpdatedAt}
}

func scanRefund(row accountScanner) (paymentRefundView, error) {
	var value paymentRefundView
	err := row.Scan(refundScanTargets(&value)...)
	return value, err
}

func loadRefundByPayment(r *http.Request, tx pgx.Tx, paymentID, userID string) (paymentRefundView, error) {
	return scanRefund(tx.QueryRow(r.Context(), refundSelect+`WHERE refund.payment_id=$1 AND refund.user_id=$2
		ORDER BY refund.requested_at DESC,refund.id DESC LIMIT 1`, paymentID, userID))
}

func (s *Server) listRefundQueue(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "requested"
	}
	if _, valid := refundStatuses[status]; !valid {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose a valid refund status.")
		return
	}
	limit, cursor, ok := s.staffQueuePageInput(w, r, "refund-queue", status)
	if !ok {
		return
	}
	var afterTime *time.Time
	var afterID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		afterTime, afterID = &value, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), refundSelect+`WHERE refund.status=$1
		AND ($2::timestamptz IS NULL OR (refund.requested_at,refund.id)>($2,$3::uuid))
		ORDER BY refund.requested_at,refund.id LIMIT $4`, status, afterTime, afterID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund queue.")
		return
	}
	defer rows.Close()
	data := []paymentRefundView{}
	for rows.Next() {
		value, scanErr := scanRefund(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund queue.")
			return
		}
		data = append(data, value)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund queue.")
		return
	}
	page := staffQueuePage{}
	if len(data) > limit {
		data = data[:limit]
		page.HasMore = true
		last := data[len(data)-1]
		next, encodeErr := s.encodeStaffQueueCursor("refund-queue", status,
			identityFromContext(r.Context()).UserID, last.RequestedAt, last.ID)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate the refund queue.")
			return
		}
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": page})
}

func (s *Server) staffQueuePageInput(w http.ResponseWriter, r *http.Request, kind, status string) (int, *publicCursor, bool) {
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	limit := 50
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 100.")
			return 0, nil, false
		}
		limit = parsed
	}
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor == "" {
		return limit, nil, true
	}
	actorID := identityFromContext(r.Context()).UserID
	decoded, err := decodePublicCursor(rawCursor, kind, s.config.AccessTokenSecret, time.Now())
	if err != nil || !uuidPattern.MatchString(decoded.ID) || decoded.SortTime <= 0 ||
		decoded.Query != actorID || decoded.Scope != status {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "The queue cursor is invalid or expired.")
		return 0, nil, false
	}
	return limit, &decoded, true
}

func (s *Server) encodeStaffQueueCursor(kind, status, actorID string, sortTime time.Time, id string) (string, error) {
	return encodePublicCursor(publicCursor{
		Kind: kind, ExpiresAt: time.Now().Add(paymentCursorTTL).Unix(), Query: actorID, Scope: status,
		SortTime: sortTime.UnixNano(), ID: id,
	}, s.config.AccessTokenSecret)
}

func (s *Server) decideRefund(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	refundID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(refundID) {
		writeError(w, http.StatusNotFound, "refund_not_found", "Refund request not found.")
		return
	}
	var input refundDecisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Decision = strings.TrimSpace(input.Decision)
	input.Note = strings.TrimSpace(input.Note)
	input.ProviderReceipt = strings.TrimSpace(input.ProviderReceipt)
	if !validRefundDecisionInput(input) {
		writeError(w, http.StatusBadRequest, "invalid_refund_decision", "Choose approve, reject, mark_succeeded or mark_failed. Rejections and failures require a note; only a successful refund accepts a provider receipt.")
		return
	}
	requestHash, _ := hashRequest(input)
	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the refund.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	scope := refundDecisionScope(actorID, refundID)
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another refund decision.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the refund.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	// A refund decision rewrites the entry, so the competition gate comes
	// before the refund and entry locks. The unlocked read only names the gate.
	var gateCompetitionID string
	err = tx.QueryRow(r.Context(), `SELECT payment.competition_id::text FROM payment_refunds refund
		JOIN payment_intents payment ON payment.id=refund.payment_id WHERE refund.id=$1`, refundID).Scan(&gateCompetitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "refund_not_found", "Refund request not found.")
		return
	}
	if err == nil {
		err = lockCompetitionProgressionGate(r.Context(), tx, gateCompetitionID)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund request.")
		return
	}
	var refund paymentRefundView
	var organizationID, competitionID, competitionStatus string
	refund, err = scanRefund(tx.QueryRow(r.Context(), refundSelect+`WHERE refund.id=$1 FOR UPDATE`, refundID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "refund_not_found", "Refund request not found.")
		return
	}
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT competition.organization_id,competition.id,competition.status
			FROM payment_intents payment JOIN competitions competition ON competition.id=payment.competition_id
			WHERE payment.id=$1`, refund.PaymentID).Scan(&organizationID, &competitionID, &competitionStatus)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the refund request.")
		return
	}
	if competitionID != gateCompetitionID {
		writeError(w, http.StatusConflict, "refund_changed", "The refund request changed while it was being decided. Try again.")
		return
	}
	previous := refund.Status
	next, terminal, transitionOK := refundDecisionTransition(previous, input.Decision, refund.Mandatory)
	if !transitionOK {
		writeError(w, http.StatusConflict, "invalid_refund_transition", "That decision is not valid for the current refund status.")
		return
	}
	completedAtSQL := "NULL"
	if terminal {
		completedAtSQL = "now()"
	}
	query := `UPDATE payment_refunds SET status=$2,provider_receipt=NULLIF($3,''),
		provider_result_description=NULLIF($4,''),reviewed_by=$5,reviewed_at=COALESCE(reviewed_at,now()),
		completed_at=` + completedAtSQL + `,updated_at=now() WHERE id=$1
		RETURNING id,payment_id,entry_id,amount_minor,currency,reason_code,mandatory,player_note,status,
		provider_receipt,provider_result_description,requested_at,reviewed_at,completed_at,updated_at`
	if err = tx.QueryRow(r.Context(), query, refundID, next, input.ProviderReceipt, input.Note, actorID).
		Scan(refundScanTargets(&refund)...); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "provider_receipt_exists", "That provider refund receipt is already recorded.")
		} else {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the refund decision.")
		}
		return
	}
	entryChanged := false
	if next == "succeeded" {
		// A removed entry stays disqualified; the refund still completes.
		_, err = tx.Exec(r.Context(), `UPDATE competition_entries SET status='withdrawn',updated_at=now()
			WHERE id=$1 AND status<>'disqualified'`, refund.EntryID)
		entryChanged = true
	} else if next == "rejected" && competitionStatus != "cancelled" {
		_, err = tx.Exec(r.Context(), `UPDATE competition_entries SET status='registered',updated_at=now()
			WHERE id=$1 AND status='withdrawal_pending'`, refund.EntryID)
		entryChanged = true
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the registration.")
		return
	}
	if err = appendAudit(r, tx, organizationID, actorID, "payment.refund_"+next, "payment_refund", refund.ID,
		map[string]any{"status": previous}, map[string]any{"status": next, "providerReceipt": refund.ProviderReceipt}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the refund decision.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('payment_refund',$1,$2,jsonb_build_object('refundId',$1::text,'paymentId',$3::text,'status',$4::text))`,
		refund.ID, "payment.refund_"+next, refund.PaymentID, next); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the refund decision.")
		return
	}
	body, _ := json.Marshal(map[string]any{"data": refund})
	if finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, http.StatusOK, body) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the refund decision.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the refund decision.")
		return
	}
	if entryChanged {
		s.invalidateCompetitionCachesContext(context.WithoutCancel(r.Context()), competitionID)
	}
	writeResultRawJSON(w, http.StatusOK, body)
}

func validRefundDecision(value string) bool {
	return value == "approve" || value == "reject" || value == "mark_succeeded" || value == "mark_failed"
}

func validRefundDecisionInput(input refundDecisionInput) bool {
	if !validRefundDecision(input.Decision) || len(input.Note) > 1000 || len(input.ProviderReceipt) > 128 {
		return false
	}
	if input.Decision == "mark_succeeded" {
		return input.ProviderReceipt != ""
	}
	if input.ProviderReceipt != "" {
		return false
	}
	return (input.Decision != "reject" && input.Decision != "mark_failed") || input.Note != ""
}

func refundTransition(current, decision string) (string, bool, bool) {
	switch decision {
	case "approve":
		return "approved", false, current == "requested" || current == "failed"
	case "reject":
		return "rejected", true, current == "requested" || current == "approved" || current == "manual_review"
	case "mark_succeeded":
		return "succeeded", true, current == "approved" || current == "processing" || current == "manual_review" || current == "failed"
	case "mark_failed":
		return "failed", true, current == "approved" || current == "processing" || current == "manual_review"
	default:
		return current, false, false
	}
}

func refundDecisionTransition(current, decision string, mandatory bool) (string, bool, bool) {
	if mandatory && decision == "reject" {
		return current, false, false
	}
	return refundTransition(current, decision)
}

func createCompetitionCancellationRefunds(r *http.Request, tx pgx.Tx, competitionID string) error {
	_, err := tx.Exec(r.Context(), `INSERT INTO payment_refunds
		(payment_id,user_id,entry_id,amount_minor,currency,reason_code,mandatory,player_note)
		SELECT payment.id,payment.user_id,payment.entry_id,payment.amount_minor,payment.currency,
		'competition_cancelled',true,'Competition cancelled by organizer'
		FROM payment_intents payment
		WHERE payment.competition_id=$1 AND payment.status='succeeded' AND payment.entry_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM payment_refunds refund WHERE refund.payment_id=payment.id
			AND refund.status IN ('requested','approved','processing','manual_review','succeeded'))`, competitionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE competition_entries entry SET status='withdrawal_pending',updated_at=now()
		WHERE entry.competition_id=$1 AND entry.status NOT IN ('withdrawn','disqualified')
		AND EXISTS (SELECT 1 FROM payment_intents payment WHERE payment.entry_id=entry.id AND payment.status='succeeded')`, competitionID)
	return err
}
