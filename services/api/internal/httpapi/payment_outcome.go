package httpapi

import "time"

// Registration outcomes of a payment. A verified collection always reads
// status=succeeded; registrationStatus says whether it holds a place, because
// a payment that completes too late or over capacity is refunded instead.
const (
	paymentRegistrationPending       = "pending"
	paymentRegistrationRegistered    = "registered"
	paymentRegistrationRefundPending = "refund_pending"
	paymentRegistrationRefunded      = "refunded"
	paymentRegistrationRemoved       = "removed"
	paymentRegistrationNotRegistered = "not_registered"
)

// paymentIntentSelect reads a payment with the status of the entry it created
// and its latest refund, the facts registrationStatus is derived from. Callers
// append a WHERE clause that binds the payer's user ID, so a payment and its
// refund are only ever shown to the payer. A refund's user is always its
// payment's (composite foreign key); repeating that predicate lets the refund
// lookup use payment_refunds_user_requested_idx on every status poll.
const paymentIntentSelect = `SELECT payment.id,payment.competition_id,payment.entry_id,
	payment.amount_minor,payment.currency,payment.phone_e164,payment.status,payment.merchant_request_id,
	payment.checkout_request_id,payment.provider_receipt,payment.provider_result_code,
	payment.provider_result_description,payment.created_at,payment.updated_at,payment.completed_at,
	payment.query_attempts,payment.next_query_at,entry.status,
	refund.id,refund.payment_id,refund.entry_id,refund.amount_minor,refund.currency,refund.reason_code,
	refund.mandatory,refund.player_note,refund.status,refund.provider_receipt,refund.provider_result_description,
	refund.requested_at,refund.reviewed_at,refund.completed_at,refund.updated_at
	FROM payment_intents payment
	LEFT JOIN competition_entries entry ON entry.id=payment.entry_id
	LEFT JOIN LATERAL (
		SELECT * FROM payment_refunds candidate
		WHERE candidate.payment_id=payment.id AND candidate.user_id=payment.user_id
		ORDER BY candidate.requested_at DESC,candidate.id DESC LIMIT 1
	) refund ON true
	`

func scanPaymentIntent(row accountScanner) (paymentIntent, error) {
	var result paymentIntent
	var phone string
	var entryStatus *string
	var refundID, refundPaymentID, refundEntryID, refundCurrency, refundReason, refundNote, refundStatus *string
	var refundMandatory *bool
	var refundReceipt, refundDescription *string
	var refundAmount *int64
	var refundRequested, refundReviewed, refundCompleted, refundUpdated *time.Time
	err := row.Scan(&result.ID, &result.CompetitionID, &result.EntryID, &result.AmountMinor, &result.Currency, &phone,
		&result.Status, &result.MerchantRequestID, &result.CheckoutRequestID, &result.ProviderReceipt,
		&result.ProviderResultCode, &result.ProviderResultDescription, &result.CreatedAt, &result.UpdatedAt,
		&result.CompletedAt, &result.QueryAttempts, &result.NextQueryAt, &entryStatus,
		&refundID, &refundPaymentID, &refundEntryID, &refundAmount, &refundCurrency, &refundReason, &refundMandatory, &refundNote,
		&refundStatus, &refundReceipt, &refundDescription, &refundRequested, &refundReviewed, &refundCompleted, &refundUpdated)
	if err != nil {
		return paymentIntent{}, err
	}
	result.MaskedPhone = maskPhone(phone)
	if refundID != nil {
		result.Refund = &paymentRefundView{ID: *refundID, PaymentID: *refundPaymentID, EntryID: *refundEntryID,
			AmountMinor: *refundAmount, Currency: *refundCurrency, ReasonCode: *refundReason, Mandatory: *refundMandatory, PlayerNote: *refundNote,
			Status: *refundStatus, ProviderReceipt: refundReceipt, ProviderResultDescription: refundDescription,
			RequestedAt: *refundRequested, ReviewedAt: refundReviewed, CompletedAt: refundCompleted, UpdatedAt: *refundUpdated}
	}
	result.RegistrationStatus = paymentRegistrationStatus(result.Status, entryStatus, result.Refund)
	return result, nil
}

// paymentRegistrationStatus derives what a payment did for the payer's place
// from the payment, the entry it created and its latest refund. A refund that
// is owed or paid outranks the entry: completePayment answers a verified
// payment that arrives after cancellation, the draw, the deadline, the strike
// limit or full capacity with a withdrawal_pending entry that never plays and a
// mandatory refund.
func paymentRegistrationStatus(paymentStatus string, entryStatus *string, refund *paymentRefundView) string {
	if paymentStatus == "failed" {
		return paymentRegistrationNotRegistered
	}
	if paymentStatus != "succeeded" {
		return paymentRegistrationPending
	}
	if refund != nil {
		switch refund.Status {
		case "succeeded":
			return paymentRegistrationRefunded
		case "requested", "approved", "processing", "manual_review", "failed":
			// A failed refund is retried by staff; the money is still owed.
			return paymentRegistrationRefundPending
		}
		// A rejected voluntary refund leaves the entry as it was.
	}
	if entryStatus == nil {
		// The entry commits with the successful payment, so only a read racing
		// that commit can get here.
		return paymentRegistrationPending
	}
	switch *entryStatus {
	case "registered", "checked_in", "accepted":
		return paymentRegistrationRegistered
	case "withdrawal_pending":
		return paymentRegistrationRefundPending
	case "disqualified":
		return paymentRegistrationRemoved
	default:
		return paymentRegistrationNotRegistered
	}
}
