package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const paymentReviewCursorKind = "admin-payment-reviews"

var paymentReviewPhonePattern = regexp.MustCompile(`(?:\+?254|0)[17][0-9]{8}`)

type paymentReviewItem struct {
	ID                        string     `json:"id"`
	UserID                    string     `json:"userId"`
	PlayerDisplayName         string     `json:"playerDisplayName"`
	CompetitionID             string     `json:"competitionId"`
	CompetitionName           string     `json:"competitionName"`
	EntryID                   *string    `json:"entryId"`
	AmountMinor               int64      `json:"amountMinor"`
	Currency                  string     `json:"currency"`
	MaskedPhone               string     `json:"phoneNumber"`
	Status                    string     `json:"status"`
	MerchantRequestID         *string    `json:"merchantRequestId"`
	CheckoutRequestID         *string    `json:"checkoutRequestId"`
	ProviderReceipt           *string    `json:"providerReceipt"`
	ProviderResultCode        *string    `json:"providerResultCode"`
	ProviderResultDescription *string    `json:"providerResultDescription"`
	QueryAttempts             int        `json:"queryAttempts"`
	LastQueryAt               *time.Time `json:"lastQueryAt"`
	NextQueryAt               *time.Time `json:"nextQueryAt"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
	CompletedAt               *time.Time `json:"completedAt"`
	CallbackCount             int        `json:"callbackCount"`
	PendingCallbackCount      int        `json:"pendingCallbackCount"`
}

type paymentReviewCallbackEvent struct {
	ID                int64      `json:"id"`
	MerchantRequestID *string    `json:"merchantRequestId"`
	RequestID         *string    `json:"requestId"`
	PayloadSHA256     string     `json:"payloadSha256"`
	ReceivedAt        time.Time  `json:"receivedAt"`
	ProcessedAt       *time.Time `json:"processedAt"`
	ProcessingError   *string    `json:"processingError"`
}

type paymentReviewAuditEvent struct {
	ID          int64     `json:"id"`
	ActorUserID *string   `json:"actorUserId"`
	Action      string    `json:"action"`
	RequestID   *string   `json:"requestId"`
	BeforeState any       `json:"beforeState"`
	AfterState  any       `json:"afterState"`
	OccurredAt  time.Time `json:"occurredAt"`
}

type paymentReviewDetail struct {
	Payment            paymentReviewItem            `json:"payment"`
	CallbackEvents     []paymentReviewCallbackEvent `json:"callbackEvents"`
	Actions            []paymentReviewAuditEvent    `json:"actions"`
	CallbacksTruncated bool                         `json:"callbacksTruncated"`
	ActionsTruncated   bool                         `json:"actionsTruncated"`
}

func sanitizePaymentReviewAuditValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			switch {
			case strings.Contains(normalizedKey, "phone"):
				result[key] = sanitizePaymentReviewAuditValue(child)
			case strings.Contains(normalizedKey, "payload"), strings.Contains(normalizedKey, "requestip"),
				strings.Contains(normalizedKey, "requesthash"), strings.Contains(normalizedKey, "token"),
				strings.Contains(normalizedKey, "secret"), strings.Contains(normalizedKey, "password"),
				strings.Contains(normalizedKey, "otp"):
				result[key] = "[redacted]"
			default:
				result[key] = sanitizePaymentReviewAuditValue(child)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = sanitizePaymentReviewAuditValue(typed[index])
		}
		return result
	case []byte:
		return "[redacted binary]"
	case string:
		return paymentReviewPhonePattern.ReplaceAllStringFunc(typed, func(candidate string) string {
			normalized, ok := normalizeKenyanPhone(candidate)
			if !ok {
				return "[redacted phone]"
			}
			return maskPhone(normalized)
		})
	default:
		return value
	}
}

type paymentReviewFilters struct {
	Status        string
	CompetitionID string
	Checkout      string
	MinAttempts   int
}

func (filters paymentReviewFilters) scope() string {
	return strings.Join([]string{filters.Status, filters.CompetitionID, filters.Checkout, strconv.Itoa(filters.MinAttempts)}, "|")
}

type paymentReviewDecisionInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

type paymentReviewDecisionPlan struct {
	NextStatus         string
	ResetQueryAttempts bool
	ReplayCallbacks    bool
	Terminal           bool
	EventType          string
}

func paymentReviewDecisionScope(actorID, paymentID string) string {
	return "payment-review-decision:" + actorID + ":" + paymentID
}

const paymentReviewSelect = `SELECT payment.id::text,payment.user_id::text,player.display_name,
	payment.competition_id::text,competition.name,payment.entry_id::text,payment.amount_minor,
	payment.currency,payment.phone_e164,payment.status,payment.merchant_request_id,
	payment.checkout_request_id,payment.provider_receipt,payment.provider_result_code,
	payment.provider_result_description,payment.query_attempts,payment.last_query_at,payment.next_query_at,
	payment.created_at,payment.updated_at,payment.completed_at,
	(SELECT count(*) FROM payment_callback_events callback
		WHERE callback.checkout_request_id=payment.checkout_request_id),
	(SELECT count(*) FROM payment_callback_events callback
		WHERE callback.checkout_request_id=payment.checkout_request_id AND callback.processed_at IS NULL)
	FROM payment_intents payment
	JOIN users player ON player.id=payment.user_id
	JOIN competitions competition ON competition.id=payment.competition_id `

func scanPaymentReview(row accountScanner) (paymentReviewItem, error) {
	var value paymentReviewItem
	var phone string
	err := row.Scan(&value.ID, &value.UserID, &value.PlayerDisplayName, &value.CompetitionID,
		&value.CompetitionName, &value.EntryID, &value.AmountMinor, &value.Currency, &phone,
		&value.Status, &value.MerchantRequestID, &value.CheckoutRequestID, &value.ProviderReceipt,
		&value.ProviderResultCode, &value.ProviderResultDescription, &value.QueryAttempts,
		&value.LastQueryAt, &value.NextQueryAt, &value.CreatedAt, &value.UpdatedAt, &value.CompletedAt,
		&value.CallbackCount, &value.PendingCallbackCount)
	value.MaskedPhone = maskPhone(phone)
	return value, err
}

func (s *Server) listPaymentReviews(w http.ResponseWriter, r *http.Request) {
	filters, limit, cursor, ok := s.paymentReviewPageInput(w, r)
	if !ok {
		return
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		beforeTime, beforeID = &value, &cursor.ID
	}
	var competitionID any
	if filters.CompetitionID != "" {
		competitionID = filters.CompetitionID
	}
	rows, err := s.db.Writer.Query(r.Context(), paymentReviewSelect+`WHERE payment.status=$1
		AND ($2::uuid IS NULL OR payment.competition_id=$2::uuid)
		AND ($3='any' OR ($3='present' AND payment.checkout_request_id IS NOT NULL)
			OR ($3='missing' AND payment.checkout_request_id IS NULL))
		AND payment.query_attempts>=$4
		AND ($5::timestamptz IS NULL OR (payment.updated_at,payment.id)<($5,$6::uuid))
		ORDER BY payment.updated_at DESC,payment.id DESC LIMIT $7`, filters.Status, competitionID,
		filters.Checkout, filters.MinAttempts, beforeTime, beforeID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment review queue.")
		return
	}
	defer rows.Close()
	items := make([]paymentReviewItem, 0, limit)
	for rows.Next() {
		item, scanErr := scanPaymentReview(rows)
		if scanErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment review queue.")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment review queue.")
		return
	}
	page := staffQueuePage{}
	if len(items) > limit {
		items = items[:limit]
		page.HasMore = true
		last := items[len(items)-1]
		token, encodeErr := encodePublicCursor(publicCursor{
			Kind: paymentReviewCursorKind, ExpiresAt: time.Now().Add(paymentCursorTTL).Unix(),
			Query: identityFromContext(r.Context()).UserID, Scope: filters.scope(),
			SortTime: last.UpdatedAt.UnixNano(), ID: last.ID,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate the payment review queue.")
			return
		}
		page.NextCursor = &token
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": page})
}

func (s *Server) paymentReviewPageInput(w http.ResponseWriter, r *http.Request) (paymentReviewFilters, int, *publicCursor, bool) {
	filters := paymentReviewFilters{
		Status:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))),
		CompetitionID: strings.TrimSpace(r.URL.Query().Get("competitionId")),
		Checkout:      strings.ToLower(strings.TrimSpace(r.URL.Query().Get("checkout"))),
	}
	if filters.Status == "" {
		filters.Status = "review"
	}
	if _, valid := paymentHistoryStatuses[filters.Status]; !valid {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose a valid payment status.")
		return paymentReviewFilters{}, 0, nil, false
	}
	if filters.CompetitionID != "" && !uuidPattern.MatchString(filters.CompetitionID) {
		writeError(w, http.StatusBadRequest, "invalid_competition", "Choose a valid competition.")
		return paymentReviewFilters{}, 0, nil, false
	}
	if filters.Checkout == "" {
		filters.Checkout = "any"
	}
	if filters.Checkout != "any" && filters.Checkout != "present" && filters.Checkout != "missing" {
		writeError(w, http.StatusBadRequest, "invalid_checkout_filter", "Choose any, present or missing.")
		return paymentReviewFilters{}, 0, nil, false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("minAttempts")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > paymentReconcileMaxTries {
			writeError(w, http.StatusBadRequest, "invalid_min_attempts", "minAttempts must be between 0 and 12.")
			return paymentReviewFilters{}, 0, nil, false
		}
		filters.MinAttempts = parsed
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 100.")
			return paymentReviewFilters{}, 0, nil, false
		}
		limit = parsed
	}
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor == "" {
		return filters, limit, nil, true
	}
	cursor, err := decodePublicCursor(rawCursor, paymentReviewCursorKind, s.config.AccessTokenSecret, time.Now())
	actorID := identityFromContext(r.Context()).UserID
	if err != nil || cursor.Query != actorID || cursor.Scope != filters.scope() || cursor.SortTime <= 0 || !uuidPattern.MatchString(cursor.ID) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "The payment review cursor is invalid or expired.")
		return paymentReviewFilters{}, 0, nil, false
	}
	return filters, limit, &cursor, true
}

func (s *Server) getPaymentReview(w http.ResponseWriter, r *http.Request) {
	paymentID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(paymentID) {
		writeError(w, http.StatusNotFound, "payment_review_not_found", "Payment review not found.")
		return
	}
	payment, err := scanPaymentReview(s.db.Writer.QueryRow(r.Context(), paymentReviewSelect+`WHERE payment.id=$1`, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "payment_review_not_found", "Payment review not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment review.")
		return
	}
	detail := paymentReviewDetail{Payment: payment, CallbackEvents: []paymentReviewCallbackEvent{}, Actions: []paymentReviewAuditEvent{}}
	if payment.CheckoutRequestID != nil {
		rows, queryErr := s.db.Writer.Query(r.Context(), `SELECT id,merchant_request_id,request_id,payload_sha256,
			received_at,processed_at,processing_error FROM payment_callback_events
			WHERE checkout_request_id=$1 ORDER BY received_at DESC,id DESC LIMIT 101`, *payment.CheckoutRequestID)
		if queryErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load callback history.")
			return
		}
		for rows.Next() {
			var item paymentReviewCallbackEvent
			if queryErr = rows.Scan(&item.ID, &item.MerchantRequestID, &item.RequestID, &item.PayloadSHA256,
				&item.ReceivedAt, &item.ProcessedAt, &item.ProcessingError); queryErr != nil {
				rows.Close()
				writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load callback history.")
				return
			}
			detail.CallbackEvents = append(detail.CallbackEvents, item)
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load callback history.")
			return
		}
		if len(detail.CallbackEvents) > 100 {
			detail.CallbackEvents = detail.CallbackEvents[:100]
			detail.CallbacksTruncated = true
		}
	}
	rows, err := s.db.Writer.Query(r.Context(), `SELECT id,actor_user_id::text,action,request_id,
		before_state,after_state,occurred_at FROM audit_events
		WHERE subject_type='payment_intent' AND subject_id=$1
		ORDER BY occurred_at DESC,id DESC LIMIT 101`, paymentID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment review history.")
		return
	}
	for rows.Next() {
		var item paymentReviewAuditEvent
		if err = rows.Scan(&item.ID, &item.ActorUserID, &item.Action, &item.RequestID,
			&item.BeforeState, &item.AfterState, &item.OccurredAt); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment review history.")
			return
		}
		item.BeforeState = sanitizePaymentReviewAuditValue(item.BeforeState)
		item.AfterState = sanitizePaymentReviewAuditValue(item.AfterState)
		detail.Actions = append(detail.Actions, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load payment review history.")
		return
	}
	if len(detail.Actions) > 100 {
		detail.Actions = detail.Actions[:100]
		detail.ActionsTruncated = true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
}

func planPaymentReviewDecision(currentStatus, decision string, hasCheckout bool, pendingCallbacks int, knownSuccess bool, note string) (paymentReviewDecisionPlan, error) {
	if currentStatus == "succeeded" {
		return paymentReviewDecisionPlan{}, errors.New("a succeeded payment is immutable")
	}
	switch decision {
	case "retry_query":
		if currentStatus != "review" || !hasCheckout {
			return paymentReviewDecisionPlan{}, errors.New("only a review payment with a checkout ID can restart querying")
		}
		return paymentReviewDecisionPlan{
			NextStatus: "pending", ResetQueryAttempts: true,
			EventType: "payment.review_query_retried",
		}, nil
	case "replay_callbacks":
		if !hasCheckout || pendingCallbacks < 1 || currentStatus == "initiating" {
			return paymentReviewDecisionPlan{}, errors.New("this payment has no replayable callback")
		}
		return paymentReviewDecisionPlan{
			NextStatus: currentStatus, ReplayCallbacks: true,
			EventType: "payment.review_callback_replay_requested",
		}, nil
	case "mark_failed":
		if currentStatus != "review" || strings.TrimSpace(note) == "" || pendingCallbacks != 0 || knownSuccess {
			return paymentReviewDecisionPlan{}, errors.New("the payment cannot be safely marked failed")
		}
		return paymentReviewDecisionPlan{
			NextStatus: "failed", Terminal: true,
			EventType: "payment.review_marked_failed",
		}, nil
	default:
		return paymentReviewDecisionPlan{}, errors.New("unsupported payment review decision")
	}
}

func inspectStoredPaymentCallbacks(ctx context.Context, tx pgx.Tx, checkoutID string) (pending int, knownSuccess, complete bool, err error) {
	rows, err := tx.Query(ctx, `SELECT payload,processed_at FROM payment_callback_events
		WHERE checkout_request_id=$1 ORDER BY received_at DESC,id DESC LIMIT 101`, checkoutID)
	if err != nil {
		return 0, false, false, err
	}
	defer rows.Close()
	complete = true
	count := 0
	for rows.Next() {
		count++
		var raw []byte
		var processedAt *time.Time
		if err = rows.Scan(&raw, &processedAt); err != nil {
			return 0, false, false, err
		}
		if count > 100 {
			complete = false
			continue
		}
		if processedAt == nil {
			pending++
		}
		var callback callbackEnvelope
		if decodeMPesaJSON(raw, &callback) == nil && callback.Body.STKCallback.ResultCode == 0 {
			knownSuccess = true
		}
	}
	if err = rows.Err(); err != nil {
		return 0, false, false, err
	}
	return pending, knownSuccess, complete, nil
}

func (s *Server) decidePaymentReview(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	paymentID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(paymentID) {
		writeError(w, http.StatusNotFound, "payment_review_not_found", "Payment review not found.")
		return
	}
	var input paymentReviewDecisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Note = strings.TrimSpace(input.Note)
	if input.Decision != "retry_query" && input.Decision != "replay_callbacks" && input.Decision != "mark_failed" ||
		utf8.RuneCountInString(input.Note) > 1000 || input.Decision == "mark_failed" && input.Note == "" {
		writeError(w, http.StatusBadRequest, "invalid_payment_review_decision", "Choose retry_query, replay_callbacks or mark_failed. mark_failed requires a note up to 1000 characters.")
		return
	}
	if (input.Decision == "retry_query" || input.Decision == "replay_callbacks") && s.mpesa == nil {
		writeError(w, http.StatusServiceUnavailable, "mpesa_unavailable", "M-Pesa is not configured for provider verification.")
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to decide the payment review.")
		return
	}
	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the payment review.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	scope := paymentReviewDecisionScope(actorID, paymentID)
	replay, err := beginIdempotentRequest(r.Context(), tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another payment review decision.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the payment review.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	current, err := scanPaymentReview(tx.QueryRow(r.Context(), paymentReviewSelect+`WHERE payment.id=$1 FOR UPDATE OF payment`, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "payment_review_not_found", "Payment review not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment review.")
		return
	}
	pendingCallbacks := current.PendingCallbackCount
	knownSuccess := current.ProviderResultCode != nil && *current.ProviderResultCode == "0"
	if current.ProviderResultDescription != nil && strings.Contains(strings.ToLower(*current.ProviderResultDescription), "daraja reports success") {
		knownSuccess = true
	}
	callbackInspectionComplete := true
	if current.CheckoutRequestID != nil {
		var callbackSuccess bool
		pendingCallbacks, callbackSuccess, callbackInspectionComplete, err = inspectStoredPaymentCallbacks(r.Context(), tx, *current.CheckoutRequestID)
		knownSuccess = knownSuccess || callbackSuccess
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to inspect stored callbacks.")
			return
		}
	}
	if !callbackInspectionComplete && input.Decision == "mark_failed" {
		writeError(w, http.StatusConflict, "callback_history_too_large", "Callback history requires offline investigation before failure can be recorded.")
		return
	}
	plan, err := planPaymentReviewDecision(current.Status, input.Decision, current.CheckoutRequestID != nil,
		pendingCallbacks, knownSuccess, input.Note)
	if err != nil {
		code := "invalid_payment_review_transition"
		if current.Status == "succeeded" {
			code = "succeeded_payment_immutable"
		}
		writeError(w, http.StatusConflict, code, err.Error())
		return
	}
	command := pgconn.CommandTag{}
	switch input.Decision {
	case "retry_query":
		command, err = tx.Exec(r.Context(), `UPDATE payment_intents SET status='pending',query_attempts=0,
			last_query_at=NULL,next_query_at=now(),completed_at=NULL,updated_at=now()
			WHERE id=$1 AND status='review' AND checkout_request_id IS NOT NULL`, paymentID)
	case "replay_callbacks":
		command, err = tx.Exec(r.Context(), `UPDATE payment_intents SET updated_at=now()
			WHERE id=$1 AND status=$2 AND status<>'succeeded'`, paymentID, current.Status)
	case "mark_failed":
		command, err = tx.Exec(r.Context(), `UPDATE payment_intents SET status='failed',completed_at=now(),
			next_query_at=NULL,updated_at=now() WHERE id=$1 AND status='review'`, paymentID)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the payment review decision.")
		return
	}
	if command.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "payment_review_changed", "The payment changed before the decision was saved.")
		return
	}
	updated, err := scanPaymentReview(tx.QueryRow(r.Context(), paymentReviewSelect+`WHERE payment.id=$1`, paymentID))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the updated payment review.")
		return
	}
	before := map[string]any{"status": current.Status, "queryAttempts": current.QueryAttempts, "nextQueryAt": current.NextQueryAt}
	after := map[string]any{"status": updated.Status, "queryAttempts": updated.QueryAttempts,
		"nextQueryAt": updated.NextQueryAt, "decision": input.Decision, "note": input.Note}
	if err = appendPlatformAudit(r, tx, actorID, plan.EventType, "payment_intent", paymentID, before, after); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the payment review decision.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('payment_intent',$1,$2,jsonb_build_object('paymentId',$1::text,'decision',$3::text,
		'actorUserId',$4::text,'status',$5::text,'note',$6::text))`, paymentID, plan.EventType,
		input.Decision, actorID, updated.Status, input.Note); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the payment review decision.")
		return
	}
	statusCode := http.StatusOK
	if plan.ResetQueryAttempts || plan.ReplayCallbacks {
		statusCode = http.StatusAccepted
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{
		"payment": updated, "decision": input.Decision, "callbackReplayRequested": plan.ReplayCallbacks,
		"queryAttemptBudget": paymentReconcileMaxTries,
	}})
	if err = finishIdempotentRequest(r.Context(), tx, scope, idempotencyKey, statusCode, body); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the payment review decision.")
		return
	}
	if plan.ReplayCallbacks && current.CheckoutRequestID != nil {
		s.replayPendingCallbacks(r.Context(), *current.CheckoutRequestID)
	}
	writeResultRawJSON(w, statusCode, body)
}
