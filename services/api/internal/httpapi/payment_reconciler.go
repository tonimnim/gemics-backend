package httpapi

import (
	"context"
	"time"
)

const (
	paymentReconcileInterval = 15 * time.Second
	paymentReconcileBatch    = 25
	paymentReconcileMaxTries = 12
)

type reconciliationClaim struct {
	ID                string
	CheckoutRequestID string
	Attempts          int
}

// runPaymentReconciler closes the callback-only reliability gap. Every API
// replica may run it because claims use SKIP LOCKED and advance next_query_at
// atomically before making the provider call.
func (s *Server) runPaymentReconciler(ctx context.Context) {
	s.reconcileDuePayments(ctx)
	ticker := time.NewTicker(paymentReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileDuePayments(ctx)
		}
	}
}

func (s *Server) reconcileDuePayments(ctx context.Context) {
	if s.db == nil || s.mpesa == nil {
		return
	}
	// Recover claims whose worker died after incrementing the last permitted
	// attempt. Without this sweep they would never make another provider call
	// and could remain pending indefinitely.
	s.escalateExhaustedPaymentReconciliations(ctx)
	rows, err := s.db.Writer.Query(ctx, `WITH due AS (
		SELECT id FROM payment_intents
		WHERE status IN ('pending','callback_received')
		  AND checkout_request_id IS NOT NULL
		  AND query_attempts<$2
		  AND (next_query_at IS NULL OR next_query_at<=now())
		ORDER BY COALESCE(next_query_at,created_at),id
		LIMIT $1 FOR UPDATE SKIP LOCKED
	), claimed AS (
		UPDATE payment_intents payment SET query_attempts=payment.query_attempts+1,last_query_at=now(),
			-- The minimum lease exceeds the default 15-second provider timeout,
			-- preventing another replica from claiming the same query in flight.
			next_query_at=now()+LEAST(interval '5 minutes',interval '30 seconds' *
				power(2::double precision,LEAST(payment.query_attempts,5)::double precision)),updated_at=now()
		FROM due WHERE payment.id=due.id
		RETURNING payment.id,payment.checkout_request_id,payment.query_attempts
	) SELECT id,checkout_request_id,query_attempts FROM claimed`, paymentReconcileBatch, paymentReconcileMaxTries)
	if err != nil {
		s.logger.Warn("claim M-Pesa reconciliation batch", "error", err)
		return
	}
	claims := []reconciliationClaim{}
	for rows.Next() {
		var claim reconciliationClaim
		if err := rows.Scan(&claim.ID, &claim.CheckoutRequestID, &claim.Attempts); err != nil {
			rows.Close()
			s.logger.Warn("scan M-Pesa reconciliation claim", "error", err)
			return
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Warn("read M-Pesa reconciliation batch", "error", err)
		return
	}
	rows.Close()
	for _, claim := range claims {
		if ctx.Err() != nil {
			return
		}
		queryCtx, cancel := context.WithTimeout(ctx, s.config.MPesaTimeout)
		response, queryErr := s.mpesa.Query(queryCtx, claim.CheckoutRequestID)
		cancel()
		if queryErr != nil {
			s.logger.Warn("query M-Pesa payment", "payment_id", claim.ID, "attempt", claim.Attempts, "error", queryErr)
			s.escalatePaymentReconciliation(ctx, claim.ID, "Repeated Daraja query requests failed")
			continue
		}
		s.applyQueryResult(ctx, claim.ID, response)
	}
}

func (s *Server) escalateExhaustedPaymentReconciliations(ctx context.Context) {
	_, err := s.db.Writer.Exec(ctx, `WITH escalated AS (
		UPDATE payment_intents SET status='review',
			provider_result_description='M-Pesa reconciliation attempt budget exhausted',
			completed_at=NULL,updated_at=now()
		WHERE status IN ('pending','callback_received') AND query_attempts>=$1
		RETURNING id,user_id
	)
	INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
	SELECT 'payment_intent',id,'payment.reconciliation_review_required',
		jsonb_build_object('paymentId',id::text,'userId',user_id::text,
		'reason','M-Pesa reconciliation attempt budget exhausted')
	FROM escalated`, paymentReconcileMaxTries)
	if err != nil {
		s.logger.Warn("escalate exhausted M-Pesa reconciliations", "error", err)
	}
}
