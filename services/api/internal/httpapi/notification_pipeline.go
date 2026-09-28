package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	notificationDispositionProjected = "projected"
	notificationDispositionIgnored   = "ignored"
	notificationDispositionMalformed = "malformed"

	notificationRecipientCompetition        = "competition"
	notificationRecipientDraw               = "draw"
	notificationRecipientMatch              = "match"
	notificationRecipientMatchEntry         = "match_entry"
	notificationRecipientEntry              = "entry"
	notificationRecipientPayment            = "payment"
	notificationRecipientRefund             = "refund"
	notificationRecipientVerification       = "verification"
	notificationRecipientPayloadUser        = "payload_user"
	notificationPushLeaseFloor              = 30 * time.Second
	notificationPushRetryMaximum            = time.Hour
	notificationPushResponseLimit     int64 = 1 << 20
)

const notificationProjectClaimSQL = `SELECT queued.source_event_id::text,queued.aggregate_id,
	queued.event_type,queued.payload
	FROM notification_outbox_queue queued
	WHERE queued.available_at<=now()
	ORDER BY queued.available_at,queued.notification_sequence
	LIMIT $1 FOR UPDATE OF queued SKIP LOCKED`

// notificationAwaitingEntryReportSQL re-validates a "report your score" push at
// projection time. An outbox event cannot be retracted, so a lagging projector
// must not tell a removed entry, an entry that already reported, or an entry
// whose report window has closed to report now.
const notificationAwaitingEntryReportSQL = `SELECT EXISTS (
	SELECT 1 FROM match_result_verifications verification
	JOIN competition_entries entry ON entry.id=$2 AND entry.status NOT IN ('withdrawn','disqualified')
	WHERE verification.match_id=$1 AND verification.phase='awaiting_second_report'
	  AND verification.report_deadline_at>now()
	  AND NOT EXISTS (SELECT 1 FROM match_result_reports report
		WHERE report.match_id=$1 AND report.entry_id=$2))`

type notificationOutboxEvent struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     json.RawMessage
}

type notificationDefinition struct {
	Category      string
	PreferenceKey string
	Title         string
	Body          string
	ActionURL     string
	RecipientKind string
	// RecipientID is the user for payload_user recipients and the entry for
	// match_entry recipients.
	RecipientID   string
	ExcludeUserID string
	Data          map[string]any
	Disposition   string
	// SkipUnlessAwaitingEntryReport projects the event only while the
	// RecipientID entry still owes its initial score report.
	SkipUnlessAwaitingEntryReport bool
}

type notificationPushClaim struct {
	DeliveryID     string
	NotificationID string
	PushTokenID    string
	PushToken      string
	Attempts       int
	Title          string
	Body           string
	Data           json.RawMessage
	ActionURL      *string
}

type notificationReceiptClaim struct {
	DeliveryID    string
	PushTokenID   string
	PushToken     string
	SentTokenHash []byte
	TicketID      string
	Attempts      int
	PushAttempts  int
}

type expoPushMessage struct {
	To       string         `json:"to"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Data     map[string]any `json:"data"`
	Sound    string         `json:"sound,omitempty"`
	Priority string         `json:"priority,omitempty"`
}

type expoPushTicket struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

type expoPushResponse struct {
	Data []expoPushTicket `json:"data"`
}

type expoReceiptResponse struct {
	Data map[string]expoPushTicket `json:"data"`
}

type expoPushBatchError struct {
	Code      string
	Permanent bool
}

type notificationPushResolution struct {
	State                 string
	Code                  string
	TicketID              string
	RetryAfter            time.Duration
	RevokeRegisteredToken bool
}

type notificationReceiptResolution struct {
	State                 string
	Code                  string
	RetryAfter            time.Duration
	RevokeRegisteredToken bool
}

func (e *expoPushBatchError) Error() string { return e.Code }

type expoPushClient struct {
	endpoint         string
	receiptsEndpoint string
	accessToken      string
	httpClient       *http.Client
}

// runNotificationPipeline is the single lifecycle hook for both workers. Inbox
// projection never depends on Expo configuration; disabling outbound delivery
// therefore cannot lose player-visible notifications.
func (s *Server) runNotificationPipeline(ctx context.Context) {
	if s.db == nil || s.db.Writer == nil {
		return
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		s.runNotificationProjector(ctx)
	}()
	if s.config.ExpoPushEnabled() {
		client := &expoPushClient{
			endpoint: s.config.ExpoPushURL, receiptsEndpoint: s.config.ExpoReceiptsURL,
			accessToken: s.config.ExpoAccessToken,
			httpClient:  &http.Client{Timeout: s.config.ExpoPushTimeout},
		}
		workers.Add(2)
		go func() {
			defer workers.Done()
			s.runNotificationPushDelivery(ctx, client)
		}()
		go func() {
			defer workers.Done()
			s.runNotificationPushReceipts(ctx, client)
		}()
	} else {
		s.logger.Info("Expo push delivery disabled; notification inbox projection remains enabled")
	}
	workers.Wait()
}

func (s *Server) runNotificationPushReceipts(ctx context.Context, client *expoPushClient) {
	for {
		count, err := s.reconcileNotificationReceiptBatch(ctx, client)
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("reconcile notification push receipt batch", "error", err)
		}
		if !waitForNotificationWork(ctx, s.config.NotificationPoll, count == s.config.ExpoReceiptBatch) {
			return
		}
	}
}

func (s *Server) runNotificationProjector(ctx context.Context) {
	for {
		count, err := s.projectNotificationBatch(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("project notification outbox batch", "error", err)
		}
		if !waitForNotificationWork(ctx, s.config.NotificationPoll, count == s.config.NotificationProject) {
			return
		}
	}
}

func (s *Server) runNotificationPushDelivery(ctx context.Context, client *expoPushClient) {
	for {
		count, err := s.deliverNotificationPushBatch(ctx, client)
		if err != nil && ctx.Err() == nil {
			// Errors returned here are database/worker failures. Provider failures
			// are normalized and persisted per delivery without response bodies.
			s.logger.Warn("deliver notification push batch", "error", err)
		}
		if !waitForNotificationWork(ctx, s.config.NotificationPoll, count == s.config.NotificationPushBatch) {
			return
		}
	}
}

func waitForNotificationWork(ctx context.Context, interval time.Duration, immediately bool) bool {
	if immediately {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) projectNotificationBatch(ctx context.Context) (int, error) {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, notificationProjectClaimSQL, s.config.NotificationProject)
	if err != nil {
		return 0, err
	}
	events := make([]notificationOutboxEvent, 0, s.config.NotificationProject)
	for rows.Next() {
		var event notificationOutboxEvent
		if err = rows.Scan(&event.ID, &event.AggregateID, &event.EventType, &event.Payload); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, event := range events {
		definition := notificationDefinitionForEvent(event)
		if definition.Disposition == notificationDispositionProjected && definition.SkipUnlessAwaitingEntryReport {
			var awaiting bool
			if err = tx.QueryRow(ctx, notificationAwaitingEntryReportSQL, event.AggregateID,
				definition.RecipientID).Scan(&awaiting); err != nil {
				return 0, err
			}
			if !awaiting {
				definition.Disposition = notificationDispositionIgnored
			}
		}
		recipientCount := 0
		if definition.Disposition == notificationDispositionProjected {
			recipients, recipientErr := resolveNotificationRecipients(ctx, tx, event, definition)
			if recipientErr != nil {
				return 0, recipientErr
			}
			for _, userID := range recipients {
				if userID == definition.ExcludeUserID {
					continue
				}
				notificationID, insertErr := insertInboxNotification(ctx, tx, event, definition, userID)
				if insertErr != nil {
					return 0, insertErr
				}
				if definition.PreferenceKey != "" {
					if _, insertErr = tx.Exec(ctx, `INSERT INTO notification_push_deliveries
						(notification_id,push_token_id,preference_key)
						SELECT $1,token.id,$3 FROM push_tokens token
						LEFT JOIN notification_preferences preference ON preference.user_id=token.user_id
						WHERE token.user_id=$2 AND token.revoked_at IS NULL AND CASE $3
							WHEN 'competition_push' THEN COALESCE(preference.competition_push,true)
							WHEN 'match_push' THEN COALESCE(preference.match_push,true)
							WHEN 'result_push' THEN COALESCE(preference.result_push,true)
							ELSE false END
						ON CONFLICT (notification_id,push_token_id) DO NOTHING`,
						notificationID, userID, definition.PreferenceKey); insertErr != nil {
						return 0, insertErr
					}
				}
				recipientCount++
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notification_outbox_consumptions
			(source_event_id,event_type,disposition,recipient_count) VALUES ($1,$2,$3,$4)
			ON CONFLICT (source_event_id) DO NOTHING`, event.ID, event.EventType,
			definition.Disposition, recipientCount); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM notification_outbox_queue WHERE source_event_id=$1`, event.ID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(events), nil
}

func insertInboxNotification(ctx context.Context, tx pgx.Tx, event notificationOutboxEvent,
	definition notificationDefinition, userID string) (string, error) {
	data, err := json.Marshal(definition.Data)
	if err != nil {
		return "", err
	}
	var notificationID string
	err = tx.QueryRow(ctx, `WITH inserted AS (
		INSERT INTO notifications(user_id,source_event_id,category,title,body,data,action_url)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))
		ON CONFLICT (user_id,source_event_id) WHERE source_event_id IS NOT NULL DO NOTHING
		RETURNING id
	) SELECT id::text FROM inserted
	UNION ALL
	SELECT id::text FROM notifications WHERE user_id=$1 AND source_event_id=$2
	LIMIT 1`, userID, event.ID, definition.Category, definition.Title, definition.Body,
		json.RawMessage(data), definition.ActionURL).Scan(&notificationID)
	return notificationID, err
}

func notificationDefinitionForEvent(event notificationOutboxEvent) notificationDefinition {
	ignored := notificationDefinition{Disposition: notificationDispositionIgnored}
	if !uuidPattern.MatchString(event.AggregateID) {
		ignored.Disposition = notificationDispositionMalformed
		return ignored
	}
	payload := map[string]json.RawMessage{}
	if len(event.Payload) == 0 || json.Unmarshal(event.Payload, &payload) != nil {
		ignored.Disposition = notificationDispositionMalformed
		return ignored
	}
	base := notificationDefinition{Disposition: notificationDispositionProjected, Data: map[string]any{}}
	switch event.EventType {
	case "competition.check_in":
		return competitionNotification(base, event.AggregateID, "Competition check-in is open", "Check in before the deadline to keep your place in the draw.")
	case "competition.running":
		return competitionNotification(base, event.AggregateID, "Competition started", "The competition is now running. Open it to see your next match.")
	case "competition.cancelled":
		return competitionNotification(base, event.AggregateID, "Competition cancelled", "This competition has been cancelled. Open it for the latest details.")
	case "competition.completed":
		return competitionNotification(base, event.AggregateID, "Competition complete", "The competition has finished. Final standings are now available.")
	case "competition.draw_generated":
		base.Category, base.PreferenceKey = "competition", "competition_push"
		base.Title, base.Body = "Tournament draw is ready", "The bracket has been generated. Open it to see your path."
		base.ActionURL, base.RecipientKind = "/competitions/"+event.AggregateID+"/bracket", notificationRecipientDraw
		base.Data = map[string]any{"competitionId": event.AggregateID, "kind": "draw_generated"}
		return base
	case "match.ready":
		return matchNotification(base, event.AggregateID, "match", "match_push", "Your match is ready", "Open the match room to check in and play.")
	case "match.forfeited":
		return matchNotification(base, event.AggregateID, "match", "match_push", "Match decided by forfeit", "Your bracket has been updated after a forfeit.")
	case "match.cancelled":
		return matchNotification(base, event.AggregateID, "match", "match_push", "Match cancelled", "This match will not be played. Open the bracket for the latest state.")
	case "match.participant_checked_in":
		actor, ok := payloadUUID(payload, "userId")
		if !ok {
			base.Disposition = notificationDispositionMalformed
			return base
		}
		base = matchNotification(base, event.AggregateID, "match", "match_push", "Opponent checked in", "Another participant has checked in to your match.")
		base.ExcludeUserID = actor
		return base
	case "match.result_confirmed":
		return matchNotification(base, event.AggregateID, "result", "result_push", "Result confirmed", "The match result is final and the bracket has been updated.")
	case "result.report_received", "result.report_reminder":
		// Only the entry that still owes its report is told; the projector drops
		// the push once that entry reported, was removed or its window closed.
		entryID, ok := payloadUUID(payload, "entryId")
		if !ok {
			base.Disposition = notificationDispositionMalformed
			return base
		}
		title, body := "Your opponent reported the score", "Report your score for this match before the deadline."
		if event.EventType == "result.report_reminder" {
			title, body = "Report your score now", "Your report window is about to close. If you don't report, you will be removed from the tournament."
		}
		base = matchNotification(base, event.AggregateID, "result", "result_push", title, body)
		base.RecipientKind, base.RecipientID = notificationRecipientMatchEntry, entryID
		base.SkipUnlessAwaitingEntryReport = true
		return base
	case "result.mismatch":
		// Identical for both entries, so the push reveals neither claim.
		return matchNotification(base, event.AggregateID, "result", "result_push", "Scores don't match", "The reported scores don't match. Check the result and submit your final score with a screenshot before the deadline.")
	case "result.under_review":
		return matchNotification(base, event.AggregateID, "result", "result_push", "Result under review", "Gamics is reviewing this match. You'll be notified when a decision is made.")
	case "result.review_decided":
		// Entries the decision removed are no longer match recipients; they are
		// told through competition.entry_removed instead.
		return matchNotification(base, event.AggregateID, "result", "result_push", "Review complete", "Gamics has reviewed your match. Open it to see the final result.")
	case "competition.entry_removed":
		_, entryOK := payloadUUID(payload, "entryId")
		matchID, matchOK := payloadUUID(payload, "matchId")
		competitionID, competitionOK := payloadUUID(payload, "competitionId")
		body, reasonOK := entryRemovedNotificationBody(payload["reasonCode"])
		if !entryOK || !matchOK || !competitionOK || !reasonOK {
			base.Disposition = notificationDispositionMalformed
			return base
		}
		base.Category, base.PreferenceKey = "result", "result_push"
		base.Title, base.Body = "Removed from tournament", body
		base.ActionURL, base.RecipientKind = "/matches/"+matchID, notificationRecipientEntry
		base.Data = map[string]any{"competitionId": competitionID, "matchId": matchID}
		return base
	case "player.strike_recorded", "player.strike_revoked":
		strikeID, strikeOK := payloadUUID(payload, "strikeId")
		userID, userOK := payloadUUID(payload, "userId")
		if !strikeOK || !userOK {
			base.Disposition = notificationDispositionMalformed
			return base
		}
		base.Category, base.PreferenceKey = "account", "result_push"
		base.Title = "Conduct strike recorded"
		base.Body = "Gamics confirmed a false result report on your account. Repeated strikes block new registrations."
		if event.EventType == "player.strike_revoked" {
			// A revocation needs no action, and there is no account_push
			// preference to honour, so it stays in the inbox.
			base.PreferenceKey = ""
			base.Title, base.Body = "Conduct strike removed", "A conduct strike was removed from your account."
		}
		base.RecipientKind, base.RecipientID = notificationRecipientPayloadUser, userID
		base.Data = map[string]any{"strikeId": strikeID}
		return base
	case "payment.succeeded":
		base.Category, base.Title, base.Body = "payment", "Payment received", "Your M-Pesa competition payment was confirmed."
		base.RecipientKind, base.ActionURL = notificationRecipientPayment, "/payments/"+event.AggregateID
		base.Data = map[string]any{"paymentId": event.AggregateID, "kind": "payment_succeeded"}
		return base
	case "payment.reconciliation_review_required":
		base.Category, base.Title, base.Body = "payment", "Payment needs review", "Your M-Pesa payment is being reviewed. You do not need to pay again."
		base.RecipientKind, base.ActionURL = notificationRecipientPayment, "/payments/"+event.AggregateID
		base.Data = map[string]any{"paymentId": event.AggregateID, "kind": "payment_review"}
		return base
	case "payment.review_marked_failed":
		base.Category, base.Title, base.Body = "payment", "Payment not completed", "The reviewed M-Pesa payment was not completed. Open it for details."
		base.RecipientKind, base.ActionURL = notificationRecipientPayment, "/payments/"+event.AggregateID
		base.Data = map[string]any{"paymentId": event.AggregateID, "kind": "payment_failed"}
		return base
	case "payment.refund_requested", "payment.refund_approved", "payment.refund_rejected", "payment.refund_succeeded", "payment.refund_failed", "payment.refund_manual_review", "payment.refund_processing":
		status := strings.TrimPrefix(event.EventType, "payment.refund_")
		base.Category, base.Title = "payment", refundNotificationTitle(status)
		base.Body = "Open your payment history for the latest refund status."
		base.RecipientKind, base.ActionURL = notificationRecipientRefund, "/refunds/"+event.AggregateID
		base.Data = map[string]any{"refundId": event.AggregateID, "status": status}
		return base
	case "game_account.verification_approved", "game_account.verification_rejected":
		status := strings.TrimPrefix(event.EventType, "game_account.verification_")
		base.Category, base.Title = "account", "Game account verification "+status
		base.Body = "Open your game account to review the verification decision."
		base.RecipientKind = notificationRecipientVerification
		base.Data = map[string]any{"verificationRequestId": event.AggregateID, "status": status}
		if accountID, valid := payloadUUID(payload, "gameAccountId"); valid {
			base.ActionURL = "/game-accounts/" + accountID
			base.Data["gameAccountId"] = accountID
		}
		// There is no account_push preference in the public contract, so these
		// remain inbox-only rather than bypassing the player's choices.
		return base
	default:
		return ignored
	}
}

func competitionNotification(base notificationDefinition, competitionID, title, body string) notificationDefinition {
	base.Category, base.PreferenceKey = "competition", "competition_push"
	base.Title, base.Body = title, body
	base.ActionURL, base.RecipientKind = "/competitions/"+competitionID, notificationRecipientCompetition
	base.Data = map[string]any{"competitionId": competitionID}
	return base
}

func matchNotification(base notificationDefinition, matchID, category, preference, title, body string) notificationDefinition {
	base.Category, base.PreferenceKey = category, preference
	base.Title, base.Body = title, body
	base.ActionURL, base.RecipientKind = "/matches/"+matchID, notificationRecipientMatch
	base.Data = map[string]any{"matchId": matchID}
	return base
}

// entryRemovedNotificationBody explains a removal by its reason code. An
// unknown or missing code has no player-facing wording, so the event is
// treated as malformed rather than sent with a vague message.
func entryRemovedNotificationBody(reasonCode json.RawMessage) (string, bool) {
	var reason string
	if json.Unmarshal(reasonCode, &reason) != nil {
		return "", false
	}
	switch reason {
	case "report_timeout":
		return "You didn't report your score in time.", true
	case "response_timeout":
		return "You didn't submit your final score in time.", true
	case "no_result_reported":
		return "No score was reported before the deadline.", true
	case "platform_review":
		return "Gamics reviewed your match and removed your entry.", true
	default:
		return "", false
	}
}

func refundNotificationTitle(status string) string {
	switch status {
	case "requested":
		return "Refund requested"
	case "approved":
		return "Refund approved"
	case "rejected":
		return "Refund rejected"
	case "succeeded":
		return "Refund completed"
	case "failed":
		return "Refund needs attention"
	case "manual_review":
		return "Refund under review"
	default:
		return "Refund processing"
	}
}

func payloadUUID(payload map[string]json.RawMessage, key string) (string, bool) {
	var result string
	if json.Unmarshal(payload[key], &result) != nil || !uuidPattern.MatchString(result) {
		return "", false
	}
	return result, true
}

// resolveNotificationRecipients returns the active users an event reaches.
// Entry-scoped kinds read the roster at projection time; match recipients
// skip withdrawn and removed entries, which are told through their own events.
func resolveNotificationRecipients(ctx context.Context, tx pgx.Tx, event notificationOutboxEvent,
	definition notificationDefinition) ([]string, error) {
	var query string
	args := []any{event.AggregateID}
	switch definition.RecipientKind {
	case notificationRecipientCompetition:
		query = `SELECT DISTINCT recipient.user_id::text FROM (
			SELECT member.user_id FROM competition_entries entry
			JOIN entry_members member ON member.entry_id=entry.id
			WHERE entry.competition_id=$1 AND entry.status IN ('registered','checked_in','accepted','withdrawal_pending')
			  AND member.roster_role IN ('starter','substitute')
			UNION
			SELECT entry.captain_user_id FROM competition_entries entry
			WHERE entry.competition_id=$1 AND entry.status IN ('registered','checked_in','accepted','withdrawal_pending')
		) recipient JOIN users player ON player.id=recipient.user_id AND player.status='active'
		ORDER BY recipient.user_id::text`
	case notificationRecipientDraw:
		query = `SELECT DISTINCT recipient.user_id::text FROM (
			SELECT member.user_id FROM competition_draw_entries drawn
			JOIN entry_members member ON member.entry_id=drawn.entry_id
			WHERE drawn.competition_id=$1 AND member.roster_role IN ('starter','substitute')
			UNION
			SELECT entry.captain_user_id FROM competition_draw_entries drawn
			JOIN competition_entries entry ON entry.id=drawn.entry_id
			WHERE drawn.competition_id=$1
		) recipient JOIN users player ON player.id=recipient.user_id AND player.status='active'
		ORDER BY recipient.user_id::text`
	case notificationRecipientMatch:
		query = `SELECT DISTINCT recipient.user_id::text FROM matches match
		JOIN competition_entries entry ON entry.id IN (match.home_entry_id,match.away_entry_id)
		  AND entry.status NOT IN ('withdrawn','disqualified')
		CROSS JOIN LATERAL (
			SELECT member.user_id FROM entry_members member
			WHERE member.entry_id=entry.id AND member.roster_role IN ('starter','substitute')
			UNION
			SELECT entry.captain_user_id
		) recipient JOIN users player ON player.id=recipient.user_id AND player.status='active'
		WHERE match.id=$1 ORDER BY recipient.user_id::text`
	case notificationRecipientMatchEntry:
		// The payload entry must be one of the aggregate match's entries, so a
		// malformed event cannot address another match's players.
		query = `SELECT DISTINCT recipient.user_id::text FROM matches match
		JOIN competition_entries entry ON entry.id=$2 AND entry.id IN (match.home_entry_id,match.away_entry_id)
		CROSS JOIN LATERAL (
			SELECT member.user_id FROM entry_members member
			WHERE member.entry_id=entry.id AND member.roster_role IN ('starter','substitute')
			UNION
			SELECT entry.captain_user_id
		) recipient JOIN users player ON player.id=recipient.user_id AND player.status='active'
		WHERE match.id=$1 ORDER BY recipient.user_id::text`
		args = append(args, definition.RecipientID)
	case notificationRecipientEntry:
		query = `SELECT DISTINCT recipient.user_id::text FROM competition_entries entry
		CROSS JOIN LATERAL (
			SELECT member.user_id FROM entry_members member
			WHERE member.entry_id=entry.id AND member.roster_role IN ('starter','substitute')
			UNION
			SELECT entry.captain_user_id
		) recipient JOIN users player ON player.id=recipient.user_id AND player.status='active'
		WHERE entry.id=$1 ORDER BY recipient.user_id::text`
	case notificationRecipientPayment:
		query = `SELECT payment.user_id::text FROM payment_intents payment
		JOIN users player ON player.id=payment.user_id AND player.status='active'
		WHERE payment.id=$1`
	case notificationRecipientRefund:
		query = `SELECT refund.user_id::text FROM payment_refunds refund
		JOIN users player ON player.id=refund.user_id AND player.status='active'
		WHERE refund.id=$1`
	case notificationRecipientVerification:
		query = `SELECT request.user_id::text FROM game_account_verification_requests request
		JOIN users player ON player.id=request.user_id AND player.status='active'
		WHERE request.id=$1`
	case notificationRecipientPayloadUser:
		query, args = `SELECT id::text FROM users WHERE id=$1 AND status='active'`, []any{definition.RecipientID}
	default:
		return nil, nil
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, 4)
	for rows.Next() {
		var userID string
		if err = rows.Scan(&userID); err != nil {
			return nil, err
		}
		result = append(result, userID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func (s *Server) deliverNotificationPushBatch(ctx context.Context, client *expoPushClient) (int, error) {
	claims, err := s.claimNotificationPushBatch(ctx)
	if err != nil || len(claims) == 0 {
		return len(claims), err
	}
	messages := make([]expoPushMessage, 0, len(claims))
	for _, claim := range claims {
		data := map[string]any{}
		if json.Unmarshal(claim.Data, &data) != nil {
			data = map[string]any{}
		}
		data["notificationId"] = claim.NotificationID
		if claim.ActionURL != nil {
			data["actionUrl"] = *claim.ActionURL
		}
		messages = append(messages, expoPushMessage{To: claim.PushToken, Title: claim.Title,
			Body: claim.Body, Data: data, Sound: "default", Priority: "high"})
	}
	sendCtx, cancel := context.WithTimeout(ctx, s.config.ExpoPushTimeout)
	tickets, sendErr := client.Send(sendCtx, messages)
	cancel()
	if err = s.persistNotificationPushResults(ctx, claims, tickets, sendErr); err != nil {
		return len(claims), err
	}
	return len(claims), nil
}

func (s *Server) reconcileNotificationReceiptBatch(ctx context.Context, client *expoPushClient) (int, error) {
	claims, err := s.claimNotificationReceiptBatch(ctx)
	if err != nil || len(claims) == 0 {
		return len(claims), err
	}
	ticketIDs := make([]string, 0, len(claims))
	for _, claim := range claims {
		ticketIDs = append(ticketIDs, claim.TicketID)
	}
	receiptCtx, cancel := context.WithTimeout(ctx, s.config.ExpoPushTimeout)
	receipts, receiptErr := client.Receipts(receiptCtx, ticketIDs)
	cancel()
	if err = s.persistNotificationReceiptResults(ctx, claims, receipts, receiptErr); err != nil {
		return len(claims), err
	}
	return len(claims), nil
}

func (s *Server) claimNotificationReceiptBatch(ctx context.Context) ([]notificationReceiptClaim, error) {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	lease := max(notificationPushLeaseFloor, s.config.ExpoPushTimeout+15*time.Second)
	rows, err := tx.Query(ctx, `WITH due AS (
		SELECT delivery.id FROM notification_push_deliveries delivery
		JOIN push_tokens token ON token.id=delivery.push_token_id
		WHERE delivery.provider_ticket_id IS NOT NULL AND (
			(delivery.state='accepted' AND delivery.receipt_next_attempt_at<=now()) OR
			(delivery.state='checking_receipt' AND delivery.receipt_lease_until<=now()))
		ORDER BY delivery.receipt_next_attempt_at,delivery.id
		LIMIT $1 FOR UPDATE OF delivery,token SKIP LOCKED
	), claimed AS (
		UPDATE notification_push_deliveries delivery SET state='checking_receipt',
			receipt_attempts=delivery.receipt_attempts+1,
			receipt_lease_until=now()+($2::double precision * interval '1 second'),updated_at=now()
		FROM due WHERE delivery.id=due.id
		RETURNING delivery.id,delivery.push_token_id,delivery.provider_ticket_id,delivery.sent_token_hash,
			delivery.receipt_attempts,delivery.attempts
	)
	SELECT claimed.id::text,claimed.push_token_id::text,token.expo_push_token,
		claimed.sent_token_hash,claimed.provider_ticket_id,claimed.receipt_attempts,claimed.attempts
	FROM claimed JOIN push_tokens token ON token.id=claimed.push_token_id
	ORDER BY claimed.id`, s.config.ExpoReceiptBatch, lease.Seconds())
	if err != nil {
		return nil, err
	}
	claims := make([]notificationReceiptClaim, 0, s.config.ExpoReceiptBatch)
	for rows.Next() {
		var claim notificationReceiptClaim
		if err = rows.Scan(&claim.DeliveryID, &claim.PushTokenID, &claim.PushToken,
			&claim.SentTokenHash, &claim.TicketID, &claim.Attempts, &claim.PushAttempts); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Server) claimNotificationPushBatch(ctx context.Context) ([]notificationPushClaim, error) {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Re-evaluate preferences at claim time so a queued delivery cannot race a
	// later opt-out indefinitely. Revoked installations and inactive accounts
	// are terminally suppressed as well.
	if _, err = tx.Exec(ctx, `UPDATE notification_push_deliveries delivery SET
		state='suppressed',lease_until=NULL,last_error_code='delivery_disabled',updated_at=now()
		WHERE ((delivery.state IN ('pending','retry') AND delivery.next_attempt_at<=now()) OR
			(delivery.state='submitting' AND delivery.lease_until<=now()))
		AND (EXISTS (SELECT 1 FROM push_tokens token WHERE token.id=delivery.push_token_id
			AND token.revoked_at IS NOT NULL)
		OR EXISTS (SELECT 1 FROM notifications notification JOIN users player ON player.id=notification.user_id
			WHERE notification.id=delivery.notification_id AND player.status<>'active')
		OR NOT COALESCE((SELECT CASE delivery.preference_key
			WHEN 'competition_push' THEN preference.competition_push
			WHEN 'match_push' THEN preference.match_push
			WHEN 'result_push' THEN preference.result_push ELSE false END
			FROM notifications notification
			JOIN notification_preferences preference ON preference.user_id=notification.user_id
			WHERE notification.id=delivery.notification_id),true))`); err != nil {
		return nil, err
	}
	lease := max(notificationPushLeaseFloor, s.config.ExpoPushTimeout+15*time.Second)
	rows, err := tx.Query(ctx, `WITH due AS (
		SELECT delivery.id FROM notification_push_deliveries delivery
		JOIN notifications notification ON notification.id=delivery.notification_id
		JOIN users player ON player.id=notification.user_id AND player.status='active'
		JOIN push_tokens token ON token.id=delivery.push_token_id AND token.revoked_at IS NULL
		LEFT JOIN notification_preferences preference ON preference.user_id=notification.user_id
		WHERE ((delivery.state IN ('pending','retry') AND delivery.next_attempt_at<=now())
			OR (delivery.state='submitting' AND delivery.lease_until<=now()))
		  AND CASE delivery.preference_key
			WHEN 'competition_push' THEN COALESCE(preference.competition_push,true)
			WHEN 'match_push' THEN COALESCE(preference.match_push,true)
			WHEN 'result_push' THEN COALESCE(preference.result_push,true)
			ELSE false END
		ORDER BY delivery.next_attempt_at,delivery.id
		LIMIT $1 FOR UPDATE OF delivery,token SKIP LOCKED
	), claimed AS (
		UPDATE notification_push_deliveries delivery SET state='submitting',attempts=delivery.attempts+1,
			lease_until=now()+($2::double precision * interval '1 second'),updated_at=now()
		FROM due WHERE delivery.id=due.id
		RETURNING delivery.id,delivery.notification_id,delivery.push_token_id,delivery.attempts
	)
	SELECT claimed.id::text,claimed.notification_id::text,claimed.push_token_id::text,
		claimed.attempts,token.expo_push_token,notification.title,notification.body,
		notification.data,notification.action_url
	FROM claimed
	JOIN push_tokens token ON token.id=claimed.push_token_id
	JOIN notifications notification ON notification.id=claimed.notification_id
	ORDER BY claimed.id`, s.config.NotificationPushBatch, lease.Seconds())
	if err != nil {
		return nil, err
	}
	claims := make([]notificationPushClaim, 0, s.config.NotificationPushBatch)
	for rows.Next() {
		var claim notificationPushClaim
		if err = rows.Scan(&claim.DeliveryID, &claim.NotificationID, &claim.PushTokenID,
			&claim.Attempts, &claim.PushToken, &claim.Title, &claim.Body, &claim.Data,
			&claim.ActionURL); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func (client *expoPushClient) Send(ctx context.Context, messages []expoPushMessage) ([]expoPushTicket, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if len(messages) > 100 {
		return nil, &expoPushBatchError{Code: "batch_too_large", Permanent: true}
	}
	body, err := json.Marshal(messages)
	if err != nil {
		return nil, &expoPushBatchError{Code: "encode_request", Permanent: true}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &expoPushBatchError{Code: "invalid_endpoint", Permanent: true}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.accessToken)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &expoPushBatchError{Code: "transport_error"}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, notificationPushResponseLimit))
		permanent := response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusRequestTimeout
		return nil, &expoPushBatchError{Code: fmt.Sprintf("expo_http_%d", response.StatusCode), Permanent: permanent}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, notificationPushResponseLimit))
	var result expoPushResponse
	if err = decoder.Decode(&result); err != nil {
		return nil, &expoPushBatchError{Code: "invalid_response"}
	}
	return result.Data, nil
}

func (client *expoPushClient) Receipts(ctx context.Context, ticketIDs []string) (map[string]expoPushTicket, error) {
	if len(ticketIDs) == 0 {
		return map[string]expoPushTicket{}, nil
	}
	if len(ticketIDs) > 1000 {
		return nil, &expoPushBatchError{Code: "receipt_batch_too_large", Permanent: true}
	}
	body, err := json.Marshal(map[string]any{"ids": ticketIDs})
	if err != nil {
		return nil, &expoPushBatchError{Code: "encode_receipt_request", Permanent: true}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.receiptsEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &expoPushBatchError{Code: "invalid_receipts_endpoint", Permanent: true}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.accessToken)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &expoPushBatchError{Code: "receipt_transport_error"}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, notificationPushResponseLimit))
		permanent := response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusRequestTimeout
		return nil, &expoPushBatchError{Code: fmt.Sprintf("expo_receipts_http_%d", response.StatusCode), Permanent: permanent}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, notificationPushResponseLimit))
	var result expoReceiptResponse
	if err = decoder.Decode(&result); err != nil || result.Data == nil {
		return nil, &expoPushBatchError{Code: "invalid_receipts_response"}
	}
	return result.Data, nil
}

func (s *Server) persistNotificationPushResults(ctx context.Context, claims []notificationPushClaim,
	tickets []expoPushTicket, sendErr error) error {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for index, claim := range claims {
		resolution := resolveNotificationPush(claim, index, tickets, sendErr, s.config.NotificationPushTries)
		if resolution.State == "accepted" {
			sentTokenHash := sha256.Sum256([]byte(claim.PushToken))
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='accepted',lease_until=NULL,
				provider_ticket_id=$3,sent_token_hash=$4,last_error_code=NULL,accepted_at=now(),receipt_attempts=0,
				receipt_next_attempt_at=now()+($5::double precision * interval '1 second'),
				receipt_lease_until=NULL,delivered_at=NULL,updated_at=now()
				WHERE id=$1 AND state='submitting' AND attempts=$2`, claim.DeliveryID, claim.Attempts,
				resolution.TicketID, sentTokenHash[:], s.config.ExpoReceiptDelay.Seconds())
		} else if resolution.State == "permanent_failure" {
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='permanent_failure',
				lease_until=NULL,last_error_code=$3,updated_at=now()
				WHERE id=$1 AND state='submitting' AND attempts=$2`, claim.DeliveryID, claim.Attempts, resolution.Code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='retry',lease_until=NULL,
				last_error_code=$3,next_attempt_at=now()+($4::double precision * interval '1 second'),updated_at=now()
				WHERE id=$1 AND state='submitting' AND attempts=$2`, claim.DeliveryID, claim.Attempts,
				resolution.Code, resolution.RetryAfter.Seconds())
		}
		if err != nil {
			return err
		}
		if resolution.RevokeRegisteredToken {
			if _, err = tx.Exec(ctx, `UPDATE push_tokens SET revoked_at=COALESCE(revoked_at,now()),updated_at=now()
				WHERE id=$1 AND expo_push_token=$2`, claim.PushTokenID, claim.PushToken); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func resolveNotificationPush(claim notificationPushClaim, index int, tickets []expoPushTicket,
	sendErr error, maxAttempts int) notificationPushResolution {
	result := notificationPushResolution{State: "retry", Code: "missing_ticket"}
	permanent := false
	if sendErr != nil {
		var batchErr *expoPushBatchError
		if errors.As(sendErr, &batchErr) {
			result.Code, permanent = batchErr.Code, batchErr.Permanent
		} else {
			result.Code = "provider_error"
		}
	} else if index < len(tickets) {
		ticket := tickets[index]
		if ticket.Status == "ok" && strings.TrimSpace(ticket.ID) != "" {
			if len(strings.TrimSpace(ticket.ID)) > 256 {
				result.State, result.Code = "permanent_failure", "invalid_ticket_id"
				return result
			}
			result.State, result.Code = "accepted", ""
			result.TicketID = strings.TrimSpace(ticket.ID)
			return result
		}
		result.Code = normalizeExpoTicketError(ticket.Details.Error)
		result.RevokeRegisteredToken = result.Code == "DeviceNotRegistered"
		permanent = expoTicketErrorIsPermanent(result.Code)
	}
	if permanent || claim.Attempts >= maxAttempts {
		result.State = "permanent_failure"
		return result
	}
	result.RetryAfter = notificationPushRetryDelay(claim.Attempts, claim.DeliveryID)
	return result
}

func (s *Server) persistNotificationReceiptResults(ctx context.Context, claims []notificationReceiptClaim,
	receipts map[string]expoPushTicket, receiptErr error) error {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, claim := range claims {
		resolution := resolveNotificationReceipt(claim, receipts, receiptErr,
			s.config.ExpoReceiptTries, s.config.NotificationPushTries, s.config.ExpoReceiptRetry)
		switch resolution.State {
		case "delivered":
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='delivered',
				receipt_lease_until=NULL,receipt_next_attempt_at=NULL,last_error_code=NULL,
				delivered_at=now(),updated_at=now()
				WHERE id=$1 AND state='checking_receipt' AND receipt_attempts=$2`,
				claim.DeliveryID, claim.Attempts)
		case "retry":
			// The receipt explicitly states that Expo did not deliver this push.
			// Only rate limiting is safe to resend; the old ticket is cleared so
			// the send worker creates a fresh provider request.
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='retry',
				lease_until=NULL,next_attempt_at=now()+($3::double precision * interval '1 second'),
				provider_ticket_id=NULL,sent_token_hash=NULL,accepted_at=NULL,receipt_attempts=0,
				receipt_next_attempt_at=NULL,receipt_lease_until=NULL,delivered_at=NULL,
				last_error_code=$4,updated_at=now()
				WHERE id=$1 AND state='checking_receipt' AND receipt_attempts=$2`,
				claim.DeliveryID, claim.Attempts, resolution.RetryAfter.Seconds(), resolution.Code)
		case "accepted":
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='accepted',
				receipt_lease_until=NULL,
				receipt_next_attempt_at=now()+($3::double precision * interval '1 second'),
				last_error_code=$4,updated_at=now()
				WHERE id=$1 AND state='checking_receipt' AND receipt_attempts=$2`,
				claim.DeliveryID, claim.Attempts, resolution.RetryAfter.Seconds(), resolution.Code)
		default:
			_, err = tx.Exec(ctx, `UPDATE notification_push_deliveries SET state='permanent_failure',
				receipt_lease_until=NULL,receipt_next_attempt_at=NULL,last_error_code=$3,updated_at=now()
				WHERE id=$1 AND state='checking_receipt' AND receipt_attempts=$2`,
				claim.DeliveryID, claim.Attempts, resolution.Code)
		}
		if err != nil {
			return err
		}
		if resolution.RevokeRegisteredToken {
			if _, err = tx.Exec(ctx, `UPDATE push_tokens SET revoked_at=COALESCE(revoked_at,now()),updated_at=now()
				WHERE id=$1 AND expo_push_token=$2`, claim.PushTokenID, claim.PushToken); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func resolveNotificationReceipt(claim notificationReceiptClaim, receipts map[string]expoPushTicket,
	receiptErr error, maxReceiptAttempts, maxPushAttempts int, retryBase time.Duration) notificationReceiptResolution {
	result := notificationReceiptResolution{State: "accepted", Code: "receipt_not_ready"}
	permanent := false
	if receiptErr != nil {
		var batchErr *expoPushBatchError
		if errors.As(receiptErr, &batchErr) {
			result.Code, permanent = batchErr.Code, batchErr.Permanent
		} else {
			result.Code = "receipt_provider_error"
		}
	} else if receipt, exists := receipts[claim.TicketID]; exists {
		if receipt.Status == "ok" {
			result.State, result.Code = "delivered", ""
			return result
		}
		if receipt.Status != "error" {
			result.Code = "unknown_receipt_status"
		} else {
			result.Code = normalizeExpoTicketError(receipt.Details.Error)
			currentTokenHash := sha256.Sum256([]byte(claim.PushToken))
			result.RevokeRegisteredToken = result.Code == "DeviceNotRegistered" &&
				bytes.Equal(currentTokenHash[:], claim.SentTokenHash)
			if result.Code == "MessageRateExceeded" && claim.PushAttempts < maxPushAttempts {
				result.State = "retry"
				result.RetryAfter = notificationPushRetryDelay(claim.PushAttempts, claim.DeliveryID)
				return result
			}
			permanent = true
		}
	}
	if permanent || claim.Attempts >= maxReceiptAttempts {
		result.State = "permanent_failure"
		return result
	}
	result.RetryAfter = notificationReceiptRetryDelay(claim.Attempts, claim.DeliveryID, retryBase)
	return result
}

func normalizeExpoTicketError(value string) string {
	switch strings.TrimSpace(value) {
	case "DeviceNotRegistered", "MessageTooBig", "MessageRateExceeded", "MismatchSenderId", "InvalidCredentials":
		return strings.TrimSpace(value)
	default:
		return "unknown_ticket_error"
	}
}

func expoTicketErrorIsPermanent(code string) bool {
	return code == "DeviceNotRegistered" || code == "MessageTooBig" ||
		code == "MismatchSenderId" || code == "InvalidCredentials"
}

func notificationPushRetryDelay(attempt int, deliveryID string) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := min(attempt-1, 10)
	base := 5 * time.Second * time.Duration(1<<exponent)
	if base > notificationPushRetryMaximum {
		base = notificationPushRetryMaximum
	}
	digest := sha256.Sum256([]byte(deliveryID))
	// Stable 75%-125% jitter avoids synchronized replicas while keeping retry
	// scheduling deterministic and straightforward to test.
	fraction := float64(binary.BigEndian.Uint16(digest[:2])) / 65535
	delay := time.Duration(float64(base) * (0.75 + fraction*0.5))
	return min(delay, notificationPushRetryMaximum)
}

func notificationReceiptRetryDelay(attempt int, deliveryID string, base time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if base <= 0 {
		base = 30 * time.Second
	}
	exponent := min(attempt-1, 10)
	multiplier := time.Duration(1 << exponent)
	delayBase := notificationPushRetryMaximum
	if base < notificationPushRetryMaximum && base <= notificationPushRetryMaximum/multiplier {
		delayBase = base * multiplier
	}
	if delayBase > notificationPushRetryMaximum {
		delayBase = notificationPushRetryMaximum
	}
	digest := sha256.Sum256([]byte("receipt:" + deliveryID))
	fraction := float64(binary.BigEndian.Uint16(digest[:2])) / 65535
	delay := time.Duration(float64(delayBase) * (0.75 + fraction*0.5))
	return min(delay, notificationPushRetryMaximum)
}
