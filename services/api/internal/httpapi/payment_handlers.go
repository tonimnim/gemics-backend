package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/mpesa"
	"github.com/jackc/pgx/v5"
)

var (
	kenyaMobile           = regexp.MustCompile(`^254[17][0-9]{8}$`)
	uuidPattern           = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	errPaymentNeedsReview = errors.New("payment requires manual review")
)

type mpesaInitiateInput struct {
	CompetitionID string `json:"competitionId"`
	GameAccountID string `json:"gameAccountId"`
	PhoneNumber   string `json:"phoneNumber"`
}

type paymentIntent struct {
	ID                        string     `json:"id"`
	CompetitionID             string     `json:"competitionId"`
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
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
	CompletedAt               *time.Time `json:"completedAt"`
	// RegistrationStatus and Refund tell the payer whether a succeeded
	// payment holds a place or is being returned (paymentRegistrationStatus).
	RegistrationStatus string             `json:"registrationStatus"`
	Refund             *paymentRefundView `json:"refund"`
	QueryAttempts      int                `json:"-"`
	NextQueryAt        *time.Time         `json:"-"`
}

type callbackEnvelope struct {
	Body struct {
		STKCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  struct {
				Items []struct {
					Name  string `json:"Name"`
					Value any    `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

func (s *Server) initiateMPesa(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.mpesa == nil {
		writeError(w, http.StatusServiceUnavailable, "mpesa_unavailable", "M-Pesa is not configured.")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Provide an Idempotency-Key between 8 and 128 characters.")
		return
	}
	var input mpesaInitiateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.CompetitionID = strings.TrimSpace(input.CompetitionID)
	input.GameAccountID = strings.TrimSpace(input.GameAccountID)
	if !uuidPattern.MatchString(input.CompetitionID) || !uuidPattern.MatchString(input.GameAccountID) {
		writeError(w, http.StatusBadRequest, "invalid_payment_request", "Competition and game account IDs must be valid UUIDs.")
		return
	}
	phone, ok := normalizeKenyanPhone(input.PhoneNumber)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_phone", "Enter a valid Kenyan Safaricom phone number.")
		return
	}

	userID := identityFromContext(r.Context()).UserID
	requestIP := s.clientIP(r)
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the payment request.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,7))", userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the payment request.")
		return
	}

	var amountMinor int64
	var currency, feePurpose, competitionGameID string
	var maxEntries int
	err = tx.QueryRow(r.Context(), `SELECT entry_fee_minor,currency,fee_purpose,game_id,max_entries
		FROM competitions WHERE id=$1 FOR UPDATE`, input.CompetitionID).
		Scan(&amountMinor, &currency, &feePurpose, &competitionGameID, &maxEntries)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the competition fee.")
		return
	}

	// A retry with the same key replays the stored payment before anything
	// that may have changed since, as free registration does.
	requestHashBytes := sha256.Sum256([]byte(strings.Join([]string{
		userID, input.CompetitionID, input.GameAccountID, phone, strconv.FormatInt(amountMinor, 10),
	}, "|")))
	requestHash := hex.EncodeToString(requestHashBytes[:])
	var existingID, existingHash string
	err = tx.QueryRow(r.Context(), `SELECT id,request_hash FROM payment_intents
		WHERE user_id=$1 AND idempotency_key=$2`, userID, idempotencyKey).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another payment request.")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment request.")
			return
		}
		intent, loadErr := s.loadPayment(r.Context(), existingID, userID)
		if loadErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment request.")
			return
		}
		writeJSON(w, http.StatusOK, intent)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the payment request.")
		return
	}

	// Visibility, cancellation, registration status and deadlines are part of
	// the eligibility decision, so a hidden competition is not found and a
	// blocked entry is reported exactly as free registration reports it.
	now := time.Now()
	eligibility, eligibilityErr := loadCompetitionEligibility(r.Context(), tx, input.CompetitionID, userID,
		input.GameAccountID, now.UTC(), s.config.StrikeBanThreshold)
	if errors.Is(eligibilityErr, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if errors.Is(eligibilityErr, errInvalidEligibilityPolicy) {
		s.logger.Error("invalid competition eligibility policy", "competition_id", input.CompetitionID, "error", eligibilityErr)
		writeError(w, http.StatusServiceUnavailable, "eligibility_policy_invalid", "This competition's eligibility policy is unavailable.")
		return
	}
	if eligibilityErr != nil {
		writeError(w, http.StatusServiceUnavailable, "eligibility_unavailable", "Eligibility cannot be evaluated right now.")
		return
	}
	if issue := eligibility.firstBlockingIssue(); issue != nil {
		writeCompetitionIneligible(w, eligibility, *issue)
		return
	}
	// Only a competition without an administration fee, or one M-Pesa cannot
	// collect, is refused here.
	if amountMinor <= 0 || feePurpose != "administration" {
		writeError(w, http.StatusConflict, "payment_not_available", "This competition is not accepting paid registrations.")
		return
	}
	if currency != "KES" || amountMinor%100 != 0 || amountMinor > s.config.MPesaMaxAmountMinor {
		writeError(w, http.StatusConflict, "unsupported_fee", "This competition fee cannot be collected through M-Pesa.")
		return
	}

	var accountGameID, entryDisplayName string
	var onboardingComplete bool
	err = tx.QueryRow(r.Context(), `SELECT account.game_id,COALESCE(profile.handle,account.in_game_name),
		(player.birth_date IS NOT NULL AND player.terms_accepted_at IS NOT NULL
		 AND player.privacy_accepted_at IS NOT NULL AND profile.user_id IS NOT NULL AND player.display_name_set_at IS NOT NULL)
		FROM users player
		JOIN game_accounts account ON account.id=$2 AND account.user_id=player.id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1 AND player.status='active'`, userID, input.GameAccountID).
		Scan(&accountGameID, &entryDisplayName, &onboardingComplete)
	if errors.Is(err, pgx.ErrNoRows) {
		writeBlockedEntry(w, eligibility, entryIssueGameAccountRequired)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the player account.")
		return
	}
	if !onboardingComplete {
		writeBlockedEntry(w, eligibility, entryIssueProfileIncomplete)
		return
	}
	if accountGameID != competitionGameID {
		writeBlockedEntry(w, eligibility, entryIssueGameAccountMismatch)
		return
	}

	var existingEntryStatus string
	err = tx.QueryRow(r.Context(), `SELECT status FROM competition_entries
		WHERE competition_id=$1 AND captain_user_id=$2`, input.CompetitionID, userID).Scan(&existingEntryStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify registration capacity.")
		return
	}
	if err == nil {
		if existingEntryStatus == "withdrawn" || existingEntryStatus == "disqualified" {
			writeBlockedEntry(w, eligibility, entryIssueRegistrationNotReusable)
		} else {
			writeError(w, http.StatusConflict, "already_registered", "You are already registered for this competition.")
		}
		return
	}
	activePaymentID, activePaymentStatus, err := loadActiveCompetitionPayment(r.Context(), tx, userID, input.CompetitionID)
	if err == nil {
		if commitErr := tx.Commit(r.Context()); commitErr != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment request.")
			return
		}
		writePaymentInProgress(w, activePaymentID, activePaymentStatus)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the payment request.")
		return
	}
	occupied, err := countPaymentCapacityOccupied(r.Context(), tx, input.CompetitionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify registration capacity.")
		return
	}
	if occupied >= maxEntries {
		// Unfinished payments hold places the preflight does not count.
		writeBlockedEntry(w, eligibility, entryIssueCompetitionFull)
		return
	}

	var intentID string
	err = tx.QueryRow(r.Context(), `INSERT INTO payment_intents
		(user_id,competition_id,game_account_id,entry_display_name,amount_minor,currency,phone_e164,request_ip,idempotency_key,request_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT DO NOTHING RETURNING id`, userID, input.CompetitionID, input.GameAccountID, entryDisplayName,
		amountMinor, currency, phone, requestIP, idempotencyKey, requestHash).Scan(&intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The payer's advisory lock makes a conflict unreachable; one is still
		// reported with the payment that holds the place.
		writePaymentConflict(r.Context(), w, tx, userID, input.CompetitionID)
		return
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the payment request.")
		return
	}

	if !s.paymentVelocityAllowed(r.Context(), userID, phone, requestIP) {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE payment_intents SET status='failed',provider_result_code='rate_limited',
			provider_result_description='Payment velocity limit exceeded',completed_at=now(),updated_at=now() WHERE id=$1`, intentID)
		w.Header().Set("Retry-After", retryAfterSeconds(s.config.MPesaUserWindow))
		writeError(w, http.StatusTooManyRequests, "payment_rate_limited", "Too many payment prompts. Try again later.")
		return
	}

	_, _ = s.db.Writer.Exec(r.Context(), "UPDATE payment_intents SET provider_request_started_at=now(),updated_at=now() WHERE id=$1", intentID)
	reference := "GM" + strings.ReplaceAll(intentID, "-", "")[:10]
	providerResponse, err := s.mpesa.Initiate(r.Context(), mpesa.InitiateRequest{
		PhoneNumber: phone, AmountKES: amountMinor / 100, AccountReference: reference, Description: "TournamentFee",
	})
	if err != nil {
		s.logger.Error("initiate M-Pesa STK", "payment_id", intentID, "error", err)
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE payment_intents SET status='review',
			provider_result_description=$2,next_query_at=now(),updated_at=now() WHERE id=$1`, intentID, "STK initiation outcome unknown")
		writeError(w, http.StatusBadGateway, "mpesa_request_failed", "M-Pesa could not start the payment. Do not retry with a new key until its status is checked.")
		return
	}
	command, err := s.db.Writer.Exec(r.Context(), `UPDATE payment_intents SET status='pending',merchant_request_id=$2,
		checkout_request_id=$3,next_query_at=now()+interval '10 seconds',updated_at=now()
		WHERE id=$1 AND status='initiating'`, intentID, providerResponse.MerchantRequestID, providerResponse.CheckoutRequestID)
	if err != nil || command.RowsAffected() != 1 {
		s.logger.Error("save M-Pesa checkout", "payment_id", intentID, "checkout_request_id", providerResponse.CheckoutRequestID, "error", err)
		_, _ = s.db.Writer.Exec(r.Context(), "UPDATE payment_intents SET status='review',updated_at=now() WHERE id=$1", intentID)
		writeError(w, http.StatusServiceUnavailable, "payment_record_failed", "The M-Pesa request started but its status could not be saved. Contact support.")
		return
	}
	s.replayPendingCallbacks(r.Context(), providerResponse.CheckoutRequestID)
	intent, err := s.loadPayment(r.Context(), intentID, userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment request.")
		return
	}
	writeJSON(w, http.StatusAccepted, intent)
}

// loadActiveCompetitionPayment returns the payer's unfinished or successful
// payment for the competition; a partial unique index allows only one.
func loadActiveCompetitionPayment(ctx context.Context, tx pgx.Tx, userID, competitionID string) (string, string, error) {
	var paymentID, status string
	err := tx.QueryRow(ctx, `SELECT id,status FROM payment_intents WHERE user_id=$1 AND competition_id=$2
		AND status IN ('initiating','pending','callback_received','review','succeeded') LIMIT 1`, userID, competitionID).
		Scan(&paymentID, &status)
	return paymentID, status, err
}

func writePaymentConflict(ctx context.Context, w http.ResponseWriter, tx pgx.Tx, userID, competitionID string) {
	paymentID, status, err := loadActiveCompetitionPayment(ctx, tx, userID, competitionID)
	switch {
	case err == nil:
		writePaymentInProgress(w, paymentID, status)
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another payment request.")
	default:
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify the payment request.")
	}
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	paymentID := r.PathValue("id")
	if !uuidPattern.MatchString(paymentID) {
		writeError(w, http.StatusBadRequest, "invalid_payment_id", "Payment ID must be a valid UUID.")
		return
	}
	intent, err := s.loadPayment(r.Context(), paymentID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "payment_not_found", "Payment not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the payment.")
		return
	}
	if intent.Status == "initiating" && time.Since(intent.UpdatedAt) > 30*time.Second {
		_, _ = s.db.Writer.Exec(r.Context(), `UPDATE payment_intents SET status='review',updated_at=now(),
			provider_result_description='STK initiation interrupted before checkout ID was stored' WHERE id=$1 AND status='initiating'`, intent.ID)
		if refreshed, loadErr := s.loadPayment(r.Context(), intent.ID, userID); loadErr == nil {
			intent = refreshed
		}
	}
	if s.mpesa != nil && intent.CheckoutRequestID != nil && (intent.Status == "pending" || intent.Status == "callback_received") &&
		(intent.NextQueryAt == nil || !time.Now().Before(*intent.NextQueryAt)) {
		delay := paymentQueryBackoff(intent.QueryAttempts)
		var checkoutID string
		err = s.db.Writer.QueryRow(r.Context(), `UPDATE payment_intents SET query_attempts=query_attempts+1,last_query_at=now(),
			next_query_at=now()+$2::interval,updated_at=now() WHERE id=$1 AND status IN ('pending','callback_received')
			AND (next_query_at IS NULL OR next_query_at<=now()) RETURNING checkout_request_id`, intent.ID, delay.String()).Scan(&checkoutID)
		if err == nil {
			if response, queryErr := s.mpesa.Query(r.Context(), checkoutID); queryErr == nil {
				s.applyQueryResult(r.Context(), intent.ID, response)
			}
			if refreshed, loadErr := s.loadPayment(r.Context(), intent.ID, userID); loadErr == nil {
				intent = refreshed
			}
		}
	}
	writeJSON(w, http.StatusOK, intent)
}

func (s *Server) mpesaCallback(w http.ResponseWriter, r *http.Request) {
	expected := []byte(s.config.MPesaCallbackToken)
	actual := []byte(r.PathValue("token"))
	if len(expected) < 32 || len(expected) != len(actual) || subtle.ConstantTimeCompare(expected, actual) != 1 {
		http.NotFound(w, r)
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	raw, callback, err := readMPesaCallback(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_callback", "The callback payload is invalid.")
		return
	}
	data := callback.Body.STKCallback
	if data.CheckoutRequestID == "" || data.MerchantRequestID == "" {
		writeError(w, http.StatusBadRequest, "invalid_callback", "The callback identifiers are missing.")
		return
	}
	digest := sha256.Sum256(raw)
	var eventID int64
	var processedAt *time.Time
	err = s.db.Writer.QueryRow(r.Context(), `INSERT INTO payment_callback_events
		(checkout_request_id,merchant_request_id,request_id,payload_sha256,payload)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (checkout_request_id,payload_sha256) DO UPDATE SET
		request_id=COALESCE(payment_callback_events.request_id,EXCLUDED.request_id)
		RETURNING id,processed_at`, data.CheckoutRequestID, data.MerchantRequestID, r.Header.Get("X-Request-ID"),
		hex.EncodeToString(digest[:]), raw).Scan(&eventID, &processedAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "callback_persistence_failed", "Unable to persist the callback.")
		return
	}
	if processedAt == nil {
		if s.mpesa == nil {
			_, _ = s.db.Writer.Exec(r.Context(), "UPDATE payment_callback_events SET processing_error='M-Pesa query client unavailable' WHERE id=$1", eventID)
			writeError(w, http.StatusServiceUnavailable, "callback_processing_unavailable", "The callback is safely stored and will be retried.")
			return
		}
		if err := s.processCallbackEvent(r.Context(), eventID, callback, raw); err != nil {
			_, _ = s.db.Writer.Exec(r.Context(), "UPDATE payment_callback_events SET processing_error=$2 WHERE id=$1", eventID, truncate(err.Error(), 500))
			s.logger.Error("process M-Pesa callback", "event_id", eventID, "checkout_request_id", data.CheckoutRequestID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "callback_processing_failed", "The callback is safely stored and will be retried.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
}

func (s *Server) processCallbackEvent(ctx context.Context, eventID int64, callback callbackEnvelope, raw []byte) error {
	data := callback.Body.STKCallback
	var paymentID, merchantID, phone, status string
	var amountMinor int64
	err := s.db.Writer.QueryRow(ctx, `SELECT id,COALESCE(merchant_request_id,''),phone_e164,amount_minor,status
		FROM payment_intents WHERE checkout_request_id=$1`, data.CheckoutRequestID).
		Scan(&paymentID, &merchantID, &phone, &amountMinor, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// The callback can beat the STK response update. It is durable and will be
		// replayed immediately after the checkout ID is stored.
		return nil
	}
	if err != nil {
		return err
	}
	if merchantID != data.MerchantRequestID {
		return s.finishCallbackEvent(ctx, eventID, "merchant request ID mismatch")
	}
	if (status == "succeeded" && data.ResultCode == 0) || (status == "failed" && data.ResultCode != 0) {
		return s.finishCallbackEvent(ctx, eventID, "")
	}
	if status == "succeeded" && data.ResultCode != 0 {
		// A verified successful collection is immutable accounting history. The
		// conflicting callback remains durably visible through its event record,
		// without rewriting any successful payment fields.
		return s.finishCallbackEvent(ctx, eventID, "conflicting callback received after successful payment")
	}
	command, err := s.db.Writer.Exec(ctx, `UPDATE payment_intents SET callback_payload=$2,
		provider_result_code=$3,provider_result_description=$4,updated_at=now(),
		status=CASE WHEN status='pending' THEN 'callback_received' ELSE status END WHERE id=$1`,
		paymentID, raw, strconv.Itoa(data.ResultCode), data.ResultDesc)
	if err != nil || command.RowsAffected() != 1 {
		if err == nil {
			err = errors.New("payment callback update affected no rows")
		}
		return err
	}
	query, err := s.mpesa.Query(ctx, data.CheckoutRequestID)
	if err != nil {
		return err
	}
	if err := validateQueryResponse(query, merchantID, data.CheckoutRequestID); err != nil {
		return err
	}
	queryCode := string(query.ResultCode)
	if data.ResultCode != 0 {
		if queryCode != strconv.Itoa(data.ResultCode) {
			return fmt.Errorf("callback/query result mismatch: callback=%d query=%s", data.ResultCode, queryCode)
		}
		_, err = s.db.Writer.Exec(ctx, `UPDATE payment_intents SET status='failed',completed_at=now(),updated_at=now()
			WHERE id=$1 AND status IN ('initiating','pending','callback_received','review')`, paymentID)
		if err != nil {
			return err
		}
		return s.finishCallbackEvent(ctx, eventID, "")
	}
	if queryCode != "0" {
		return fmt.Errorf("Daraja query did not confirm successful payment: %s", queryCode)
	}
	metadata := callbackMetadata(data.CallbackMetadata.Items)
	callbackPhone, phoneOK := normalizeKenyanPhone(metadata["PhoneNumber"])
	callbackAmountMinor, amountErr := parseKESMinor(metadata["Amount"])
	receipt := strings.TrimSpace(metadata["MpesaReceiptNumber"])
	if !phoneOK || callbackPhone != phone || amountErr != nil || callbackAmountMinor != amountMinor || receipt == "" {
		_, _ = s.db.Writer.Exec(ctx, `UPDATE payment_intents SET status='review',
			provider_result_description='Callback amount, phone or receipt mismatch',updated_at=now() WHERE id=$1`, paymentID)
		return s.finishCallbackEvent(ctx, eventID, "callback amount, phone or receipt mismatch")
	}
	if err := s.completePayment(ctx, paymentID, receipt, metadata["TransactionDate"], raw); err != nil {
		if errors.Is(err, errPaymentNeedsReview) {
			return s.finishCallbackEvent(ctx, eventID, err.Error())
		}
		return err
	}
	return s.finishCallbackEvent(ctx, eventID, "")
}

type verifiedPaymentRegistrationPlan struct {
	EntryStatus  string
	RefundReason string
	RefundNote   string
}

func planVerifiedPaymentRegistration(competitionStatus string, drawExists, capacityFull bool,
	existingEntryStatus *string, existingEntryInDraw bool) verifiedPaymentRegistrationPlan {
	refund := func(reason, note string) verifiedPaymentRegistrationPlan {
		return verifiedPaymentRegistrationPlan{EntryStatus: "withdrawal_pending", RefundReason: reason, RefundNote: note}
	}
	if competitionStatus == "cancelled" {
		return refund("competition_cancelled", "Verified M-Pesa payment arrived after the competition was cancelled; automatic full refund required.")
	}
	if existingEntryStatus != nil {
		if *existingEntryStatus == "withdrawn" || *existingEntryStatus == "disqualified" || *existingEntryStatus == "withdrawal_pending" {
			return refund("operations_adjustment", "Verified M-Pesa payment cannot reactivate an inactive entry; automatic full refund required.")
		}
		// A player already frozen into the draw is not a late entrant. Preserve
		// that participant and attach the independently verified collection.
		if existingEntryInDraw {
			return verifiedPaymentRegistrationPlan{EntryStatus: *existingEntryStatus}
		}
		if drawExists {
			return refund("operations_adjustment", "Verified M-Pesa payment arrived after the bracket draw was frozen; automatic full refund required.")
		}
		if competitionStatus == "registration_open" || competitionStatus == "check_in" {
			return verifiedPaymentRegistrationPlan{EntryStatus: *existingEntryStatus}
		}
		return refund("operations_adjustment", "Verified M-Pesa payment arrived after registration could join the competition; automatic full refund required.")
	}
	if drawExists {
		return refund("operations_adjustment", "Verified M-Pesa payment arrived after the bracket draw was frozen; automatic full refund required.")
	}
	if capacityFull {
		return refund("operations_adjustment", "Verified M-Pesa payment arrived after competition capacity was filled; automatic full refund required.")
	}
	if competitionStatus != "registration_open" && competitionStatus != "check_in" {
		return refund("operations_adjustment", "Verified M-Pesa payment arrived after registration could join the competition; automatic full refund required.")
	}
	return verifiedPaymentRegistrationPlan{EntryStatus: "registered"}
}

// planConductSuspendedPayment applies the strike ban to a verified payment
// (D14): a payer at the conduct strike limit gets no new entry, only an
// automatic refund. Existing entries are unaffected, and a refund already
// planned for another reason keeps that reason.
func planConductSuspendedPayment(plan verifiedPaymentRegistrationPlan, existingEntryStatus *string,
	conductSuspended bool) verifiedPaymentRegistrationPlan {
	if !conductSuspended || existingEntryStatus != nil || plan.RefundReason != "" {
		return plan
	}
	return verifiedPaymentRegistrationPlan{EntryStatus: "withdrawal_pending", RefundReason: "operations_adjustment",
		RefundNote: "Verified M-Pesa payment arrived after the account reached the conduct strike limit; automatic full refund required."}
}

const invalidTransactionTimestampReviewSQL = `UPDATE payment_intents SET status='review',
	provider_result_description='Invalid provider transaction timestamp',completed_at=NULL,updated_at=now()
	WHERE id=$1 AND status IN ('pending','callback_received','review')`

const competitionCapacityEntriesSQL = `SELECT count(*) FROM competition_entries WHERE competition_id=$1
	AND status NOT IN ('withdrawn','disqualified')`

// paymentCapacityOccupiedSQL counts the places a new payment competes for:
// active entries plus unfinished payments that have not created their entry.
const paymentCapacityOccupiedSQL = `SELECT (` + competitionCapacityEntriesSQL + `)
	+ (SELECT count(*) FROM payment_intents WHERE competition_id=$1 AND entry_id IS NULL
	   AND status IN ('initiating','pending','callback_received','review'))`

// countPaymentCapacityOccupied binds the competition to every placeholder of
// paymentCapacityOccupiedSQL.
func countPaymentCapacityOccupied(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, competitionID string) (int, error) {
	var occupied int
	err := queryer.QueryRow(ctx, paymentCapacityOccupiedSQL, competitionID).Scan(&occupied)
	return occupied, err
}

func (s *Server) completePayment(ctx context.Context, paymentID, receipt, transactionDate string, payload []byte) error {
	transactionAt := parseMPesaTime(transactionDate)
	if transactionAt == nil {
		_, updateErr := s.db.Writer.Exec(ctx, invalidTransactionTimestampReviewSQL, paymentID)
		if updateErr != nil {
			return updateErr
		}
		return fmt.Errorf("%w: invalid provider transaction timestamp", errPaymentNeedsReview)
	}
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// The competition row and the payer's entry are locked below, so the
	// competition gate comes first, as for every writer a result finalizer can
	// race. The unlocked read only names the gate.
	var gateCompetitionID string
	if err = tx.QueryRow(ctx, `SELECT competition_id FROM payment_intents WHERE id=$1`, paymentID).
		Scan(&gateCompetitionID); err != nil {
		return err
	}
	if err = lockCompetitionProgressionGate(ctx, tx, gateCompetitionID); err != nil {
		return err
	}
	var userID, competitionID, gameAccountID, displayName, paymentStatus string
	err = tx.QueryRow(ctx, `SELECT user_id,competition_id,game_account_id,entry_display_name,status
		FROM payment_intents WHERE id=$1 FOR UPDATE`, paymentID).
		Scan(&userID, &competitionID, &gameAccountID, &displayName, &paymentStatus)
	if err != nil {
		return err
	}
	if competitionID != gateCompetitionID {
		return errors.New("payment competition changed before its row was locked")
	}
	if paymentStatus == "succeeded" {
		return tx.Commit(ctx)
	}
	var receiptOwner string
	err = tx.QueryRow(ctx, "SELECT id FROM payment_intents WHERE provider_receipt=$1 AND id<>$2", receipt, paymentID).Scan(&receiptOwner)
	if err == nil {
		return s.commitPaymentReview(ctx, tx, paymentID, "M-Pesa receipt is already attached to another payment")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var competitionStatus, organizationID string
	var maxEntries int
	err = tx.QueryRow(ctx, "SELECT status,max_entries,organization_id FROM competitions WHERE id=$1 FOR UPDATE", competitionID).
		Scan(&competitionStatus, &maxEntries, &organizationID)
	if err != nil {
		return err
	}
	var drawExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_draws WHERE competition_id=$1)`, competitionID).Scan(&drawExists); err != nil {
		return err
	}
	var entryID string
	var existingEntryStatus *string
	var entryStatus string
	err = tx.QueryRow(ctx, `SELECT id,status FROM competition_entries
		WHERE competition_id=$1 AND captain_user_id=$2 FOR UPDATE`, competitionID, userID).Scan(&entryID, &entryStatus)
	entryExists := err == nil
	if entryExists {
		existingEntryStatus = &entryStatus
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var entryInDraw bool
	if entryExists {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_draw_entries
			WHERE competition_id=$1 AND entry_id=$2)`, competitionID, entryID).Scan(&entryInDraw); err != nil {
			return err
		}
	}
	var entries int
	if err = tx.QueryRow(ctx, competitionCapacityEntriesSQL, competitionID).Scan(&entries); err != nil {
		return err
	}
	var activeStrikes int
	if err = tx.QueryRow(ctx, `SELECT `+activePlayerStrikesSQL+` FROM users player WHERE player.id=$1`, userID).
		Scan(&activeStrikes); err != nil {
		return err
	}
	plan := planVerifiedPaymentRegistration(competitionStatus, drawExists, !entryExists && entries >= maxEntries,
		existingEntryStatus, entryInDraw)
	plan = planConductSuspendedPayment(plan, existingEntryStatus, strikeBanApplies(activeStrikes, s.config.StrikeBanThreshold))
	if !entryExists {
		err = tx.QueryRow(ctx, `INSERT INTO competition_entries(competition_id,display_name,captain_user_id,status)
			VALUES ($1,$2,$3,$4) RETURNING id`, competitionID, displayName, userID, plan.EntryStatus).Scan(&entryID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
			VALUES ($1,$2,$3,$4)`, entryID, competitionID, userID, gameAccountID)
		if err != nil {
			return err
		}
	} else if plan.RefundReason != "" && entryStatus != "withdrawal_pending" {
		// A withdrawn or removed entry keeps its status, so a late payment can
		// never revive it; the refund below still returns the money.
		if _, err = tx.Exec(ctx, `UPDATE competition_entries SET status='withdrawal_pending',updated_at=now()
			WHERE id=$1 AND status NOT IN ('withdrawn','disqualified')`, entryID); err != nil {
			return err
		}
	}
	command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='succeeded',entry_id=$2,provider_receipt=$3,
		provider_transaction_at=$4,callback_payload=$5,completed_at=now(),updated_at=now()
		WHERE id=$1 AND status<>'succeeded'`, paymentID, entryID, receipt, transactionAt, payload)
	if err != nil || command.RowsAffected() != 1 {
		if err == nil {
			err = errors.New("payment completion affected no rows")
		}
		return err
	}
	var refundID *string
	if plan.RefundReason != "" {
		var value string
		err = tx.QueryRow(ctx, `INSERT INTO payment_refunds
			(payment_id,user_id,entry_id,amount_minor,currency,reason_code,mandatory,player_note)
			SELECT id,user_id,entry_id,amount_minor,currency,$2,true,$3 FROM payment_intents WHERE id=$1
			RETURNING id`, paymentID, plan.RefundReason, plan.RefundNote).Scan(&value)
		if err != nil {
			return err
		}
		refundID = &value
	}
	var refundIDValue any
	registrationDisposition := "registered"
	if refundID != nil {
		refundIDValue = *refundID
		registrationDisposition = "refund_requested"
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('payment_intent',$1,'payment.succeeded',jsonb_build_object('paymentId',$1::text,
		'entryId',$2::text,'competitionId',$3::text,'refundId',$4::text,
		'registrationDisposition',$5::text))`, paymentID, entryID, competitionID, refundIDValue,
		registrationDisposition)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,subject_id,after_state)
		VALUES ($1,$2,'payment.succeeded','payment_intent',$3,jsonb_build_object('entryId',$4::text,
		'competitionId',$5::text,'providerReceipt',$6::text,'refundId',$7::text))`,
		organizationID, userID, paymentID, entryID, competitionID, receipt, refundIDValue)
	if err != nil {
		return err
	}
	if refundID != nil {
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
			VALUES ('payment_refund',$1,'payment.refund_requested',jsonb_build_object('refundId',$1::text,
			'paymentId',$2::text,'entryId',$3::text,'userId',$4::text,'reasonCode',$5::text,'mandatory',true))`,
			*refundID, paymentID, entryID, userID, plan.RefundReason)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,
			subject_id,after_state) VALUES ($1,$2,'payment.refund_requested','payment_refund',$3,
			jsonb_build_object('paymentId',$4::text,'entryId',$5::text,'status','requested',
			'reasonCode',$6::text,'mandatory',true,
			'amountMinor',(SELECT amount_minor FROM payment_intents WHERE id=$4)))`,
			organizationID, userID, *refundID, paymentID, entryID, plan.RefundReason)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Registration capacity and the player's registration state changed in the
	// committed transaction. Retire addressable detail/bracket responses; cached
	// list counters intentionally converge through their short freshness window.
	s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), competitionID)
	return nil
}

func (s *Server) commitPaymentReview(ctx context.Context, tx pgx.Tx, paymentID, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE payment_intents SET status='review',provider_result_description=$2,
		completed_at=NULL,updated_at=now() WHERE id=$1`, paymentID, reason); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", errPaymentNeedsReview, reason)
}

func (s *Server) applyQueryResult(ctx context.Context, paymentID string, response mpesa.QueryResponse) {
	var merchantID, checkoutID string
	if err := s.db.Writer.QueryRow(ctx, `SELECT COALESCE(merchant_request_id,''),COALESCE(checkout_request_id,'')
		FROM payment_intents WHERE id=$1`, paymentID).Scan(&merchantID, &checkoutID); err != nil {
		return
	}
	if err := validateQueryResponse(response, merchantID, checkoutID); err != nil {
		s.logger.Warn("invalid M-Pesa query response", "payment_id", paymentID, "error", err)
		s.escalatePaymentReconciliation(ctx, paymentID, "Repeated Daraja queries returned invalid or mismatched responses")
		return
	}
	resultCode := string(response.ResultCode)
	if resultCode == "0" {
		s.replayPendingCallbacks(ctx, checkoutID)
		// STK query does not return the receipt/amount/phone metadata needed to
		// safely complete a collection. If the success callback is truly lost,
		// escalate exactly once rather than leaving the intent pending forever.
		s.escalatePaymentReconciliation(ctx, paymentID, "Daraja reports success but the receipt callback is missing")
		return
	}
	if resultCode != "" {
		// A non-zero query code is not sufficient evidence of a terminal failure.
		// Safaricom can return an intermediate code while the callback is still in
		// flight. Preserve the payment until a matching callback arrives; after a
		// bounded number of attempts, route it to manual reconciliation.
		_, _ = s.db.Writer.Exec(ctx, `UPDATE payment_intents SET provider_result_code=$2,
			provider_result_description=$3,updated_at=now()
			WHERE id=$1 AND status IN ('pending','callback_received')`, paymentID, resultCode, response.ResultDesc)
		s.escalatePaymentReconciliation(ctx, paymentID, "Repeated Daraja queries did not produce a matching terminal callback")
	}
}

func (s *Server) escalatePaymentReconciliation(ctx context.Context, paymentID, reason string) {
	_, err := s.db.Writer.Exec(ctx, `WITH escalated AS (
		UPDATE payment_intents SET status='review',provider_result_description=$2,completed_at=NULL,updated_at=now()
		WHERE id=$1 AND status IN ('pending','callback_received') AND query_attempts>=$3
		RETURNING id,user_id
	)
	INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
	SELECT 'payment_intent',id,'payment.reconciliation_review_required',
		jsonb_build_object('paymentId',id::text,'userId',user_id::text,'reason',$2::text)
	FROM escalated`, paymentID, truncate(reason, 500), paymentReconcileMaxTries)
	if err != nil {
		s.logger.Warn("escalate payment reconciliation", "payment_id", paymentID, "error", err)
	}
}

func validateQueryResponse(response mpesa.QueryResponse, merchantID, checkoutID string) error {
	if string(response.ResponseCode) != "0" || string(response.ResultCode) == "" {
		return errors.New("Daraja query response is not terminal")
	}
	if response.CheckoutRequestID == "" || response.CheckoutRequestID != checkoutID {
		return errors.New("Daraja query checkout ID mismatch")
	}
	if response.MerchantRequestID != "" && response.MerchantRequestID != merchantID {
		return errors.New("Daraja query merchant ID mismatch")
	}
	return nil
}

func (s *Server) replayPendingCallbacks(ctx context.Context, checkoutID string) {
	rows, err := s.db.Writer.Query(ctx, `SELECT id,payload FROM payment_callback_events
		WHERE checkout_request_id=$1 AND processed_at IS NULL ORDER BY received_at,id LIMIT 10`, checkoutID)
	if err != nil {
		return
	}
	type pending struct {
		id      int64
		payload []byte
	}
	events := make([]pending, 0)
	for rows.Next() {
		var event pending
		if rows.Scan(&event.id, &event.payload) == nil {
			events = append(events, event)
		}
	}
	rows.Close()
	for _, event := range events {
		var callback callbackEnvelope
		if decodeMPesaJSON(event.payload, &callback) != nil {
			_ = s.finishCallbackEvent(ctx, event.id, "stored callback is invalid")
			continue
		}
		if err := s.processCallbackEvent(ctx, event.id, callback, event.payload); err != nil {
			_, _ = s.db.Writer.Exec(ctx, "UPDATE payment_callback_events SET processing_error=$2 WHERE id=$1", event.id, truncate(err.Error(), 500))
		}
	}
}

func (s *Server) finishCallbackEvent(ctx context.Context, eventID int64, processingError string) error {
	_, err := s.db.Writer.Exec(ctx, `UPDATE payment_callback_events SET processed_at=now(),processing_error=NULLIF($2,'')
		WHERE id=$1`, eventID, truncate(processingError, 500))
	return err
}

func (s *Server) loadPayment(ctx context.Context, paymentID, userID string) (paymentIntent, error) {
	return scanPaymentIntent(s.db.Writer.QueryRow(ctx, paymentIntentSelect+`WHERE payment.id=$1 AND payment.user_id=$2`,
		paymentID, userID))
}

func (s *Server) paymentVelocityAllowed(ctx context.Context, userID, phone, ip string) bool {
	checks := []struct {
		kind, value, column string
		limit               int
		window              time.Duration
	}{
		{kind: "user", value: userID, column: "user_id", limit: s.config.MPesaUserLimit, window: s.config.MPesaUserWindow},
		{kind: "phone", value: phone, column: "phone_e164", limit: s.config.MPesaPhoneLimit, window: s.config.MPesaPhoneWindow},
		{kind: "ip", value: ip, column: "request_ip", limit: s.config.MPesaIPLimit, window: s.config.MPesaIPWindow},
	}
	for _, check := range checks {
		digest := sha256.Sum256([]byte(strings.ToLower(check.value)))
		if s.redis != nil {
			key := s.securityKey("rate:mpesa:" + check.kind + ":" + hex.EncodeToString(digest[:]))
			if allowed, err := redisFixedWindow(ctx, s.redis, key, check.limit, check.window); err == nil {
				if !allowed {
					return false
				}
				continue
			}
		}
		var count int
		query := "SELECT count(*) FROM payment_intents WHERE " + check.column + "=$1 AND created_at>now()-$2::interval"
		if err := s.db.Writer.QueryRow(ctx, query, check.value, check.window.String()).Scan(&count); err != nil || !paymentVelocityCountAllowed(count, check.limit) {
			return false
		}
	}
	return true
}

// The fallback query runs after the new intent commits, so count already
// includes the request being evaluated. This mirrors Redis INCR: the Nth
// request is allowed and request N+1 is rejected.
func paymentVelocityCountAllowed(count, limit int) bool {
	return limit > 0 && count <= limit
}

func readMPesaCallback(w http.ResponseWriter, r *http.Request) ([]byte, callbackEnvelope, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		return nil, callbackEnvelope{}, err
	}
	var callback callbackEnvelope
	if err := decodeMPesaJSON(raw, &callback); err != nil {
		return nil, callbackEnvelope{}, err
	}
	return raw, callback, nil
}

func decodeMPesaJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("callback contains multiple JSON values")
	}
	return nil
}

func normalizeKenyanPhone(raw string) (string, bool) {
	value := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	value = strings.TrimPrefix(value, "+")
	if strings.HasPrefix(value, "0") {
		value = "254" + value[1:]
	}
	if strings.HasPrefix(value, "7") || strings.HasPrefix(value, "1") {
		value = "254" + value
	}
	return value, kenyaMobile.MatchString(value)
}

func maskPhone(phone string) string {
	if len(phone) < 7 {
		return "***"
	}
	return phone[:4] + "*****" + phone[len(phone)-3:]
}

func callbackMetadata(items []struct {
	Name  string `json:"Name"`
	Value any    `json:"Value"`
}) map[string]string {
	result := map[string]string{}
	for _, item := range items {
		switch value := item.Value.(type) {
		case json.Number:
			result[item.Name] = value.String()
		case string:
			result[item.Name] = value
		case nil:
			result[item.Name] = ""
		default:
			result[item.Name] = fmt.Sprint(value)
		}
	}
	return result
}

func parseMPesaTime(value string) *time.Time {
	location, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		location = time.FixedZone("EAT", 3*60*60)
	}
	parsed, err := time.ParseInLocation("20060102150405", value, location)
	if err != nil {
		return nil
	}
	result := parsed.UTC()
	return &result
}

func parseKESMinor(value string) (int64, error) {
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || amount.Sign() < 0 {
		return 0, errors.New("invalid KES amount")
	}
	amount.Mul(amount, big.NewRat(100, 1))
	if !amount.IsInt() || !amount.Num().IsInt64() {
		return 0, errors.New("KES amount has unsupported precision")
	}
	return amount.Num().Int64(), nil
}

func paymentQueryBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	return minDuration(5*time.Minute, 10*time.Second*time.Duration(1<<attempt))
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
