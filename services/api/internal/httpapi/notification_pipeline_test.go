package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testNotificationEventID       = "11111111-1111-4111-8111-111111111111"
	testNotificationMatchID       = "22222222-2222-4222-8222-222222222222"
	testNotificationUserID        = "33333333-3333-4333-8333-333333333333"
	testNotificationEntryID       = "44444444-4444-4444-8444-444444444444"
	testNotificationCompetitionID = "55555555-5555-4555-8555-555555555555"
	testNotificationStrikeID      = "66666666-6666-4666-8666-666666666666"
)

func notificationTestEvent(t *testing.T, eventType, aggregateID string, payload any) notificationOutboxEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return notificationOutboxEvent{ID: testNotificationEventID, AggregateID: aggregateID, EventType: eventType, Payload: raw}
}

func notificationTestRemovalPayload(reasonCode any) map[string]any {
	return map[string]any{"entryId": testNotificationEntryID, "competitionId": testNotificationCompetitionID,
		"matchId": testNotificationMatchID, "reasonCode": reasonCode}
}

func TestNotificationDefinitionWhitelistsResultData(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"submissionId":  "44444444-4444-4444-8444-444444444444",
		"matchId":       testNotificationMatchID,
		"ratingChanges": []map[string]any{{"userId": testNotificationUserID, "before": 1200, "after": 1250}},
	})
	definition := notificationDefinitionForEvent(notificationOutboxEvent{
		ID: testNotificationEventID, AggregateID: testNotificationMatchID,
		EventType: "match.result_confirmed", Payload: payload,
	})
	if definition.Disposition != notificationDispositionProjected || definition.PreferenceKey != "result_push" {
		t.Fatalf("unexpected result mapping: %+v", definition)
	}
	want := map[string]any{"matchId": testNotificationMatchID, "kind": "match.result_confirmed"}
	if !maps.Equal(definition.Data, want) {
		t.Fatalf("outbox payload leaked into public notification data: %#v", definition.Data)
	}
}

func TestNotificationDefinitionMapsResultVerificationEvents(t *testing.T) {
	matchURL := "/matches/" + testNotificationMatchID
	matchData := map[string]any{"matchId": testNotificationMatchID}
	reportPayload := map[string]any{"matchId": testNotificationMatchID, "entryId": testNotificationEntryID}
	matchPayload := map[string]any{"matchId": testNotificationMatchID}
	strikePayload := map[string]any{"strikeId": testNotificationStrikeID, "userId": testNotificationUserID,
		"matchId": testNotificationMatchID}
	cases := []struct {
		eventType   string
		aggregateID string
		payload     map[string]any
		want        notificationDefinition
	}{
		{"result.report_received", testNotificationMatchID, reportPayload, notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Confirm the match result",
			Body: "Your opponent submitted the result. Confirm or reject it before the deadline.", ActionURL: matchURL,
			RecipientKind: notificationRecipientMatchEntry, RecipientID: testNotificationEntryID, Data: matchData,
			Disposition: notificationDispositionProjected, SkipUnlessAwaitingEntryReport: true,
		}},
		{"result.report_reminder", testNotificationMatchID, reportPayload, notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Confirm the result now",
			Body:      "Time is almost up. If you don't confirm or reject it, the submitted result stands.",
			ActionURL: matchURL, RecipientKind: notificationRecipientMatchEntry, RecipientID: testNotificationEntryID,
			Data: matchData, Disposition: notificationDispositionProjected, SkipUnlessAwaitingEntryReport: true,
		}},
		{"result.mismatch", testNotificationMatchID, matchPayload, notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Result rejected",
			Body:      "The submitted result was rejected. Send a screenshot of the Full Time screen before the deadline.",
			ActionURL: matchURL, RecipientKind: notificationRecipientMatch, Data: matchData,
			Disposition: notificationDispositionProjected,
		}},
		{"result.under_review", testNotificationMatchID, matchPayload, notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Result under review",
			Body:      "Gamics is reviewing this match. You'll be notified when a decision is made.",
			ActionURL: matchURL, RecipientKind: notificationRecipientMatch, Data: matchData,
			Disposition: notificationDispositionProjected,
		}},
		{"result.review_decided", testNotificationMatchID, matchPayload, notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Review complete",
			Body:      "Gamics has reviewed your match. Open it to see the final result.",
			ActionURL: matchURL, RecipientKind: notificationRecipientMatch, Data: matchData,
			Disposition: notificationDispositionProjected,
		}},
		{"match.forfeited", testNotificationMatchID, matchPayload, notificationDefinition{
			Category: "match", PreferenceKey: "match_push", Title: "Match decided by forfeit",
			Body: "Your bracket has been updated after a forfeit.", ActionURL: matchURL,
			RecipientKind: notificationRecipientMatch, Data: matchData, Disposition: notificationDispositionProjected,
		}},
		{"match.cancelled", testNotificationMatchID, matchPayload, notificationDefinition{
			Category: "match", PreferenceKey: "match_push", Title: "Match cancelled",
			Body:      "This match will not be played. Open the bracket for the latest state.",
			ActionURL: matchURL, RecipientKind: notificationRecipientMatch, Data: matchData,
			Disposition: notificationDispositionProjected,
		}},
		{"competition.entry_removed", testNotificationEntryID, notificationTestRemovalPayload("response_timeout"), notificationDefinition{
			Category: "result", PreferenceKey: "result_push", Title: "Removed from tournament",
			Body: "You didn't send your screenshot in time.", ActionURL: matchURL, RecipientKind: notificationRecipientEntry,
			Data:        map[string]any{"competitionId": testNotificationCompetitionID, "matchId": testNotificationMatchID},
			Disposition: notificationDispositionProjected,
		}},
		{"player.strike_recorded", testNotificationUserID, strikePayload, notificationDefinition{
			Category: "account", PreferenceKey: "result_push", Title: "Conduct strike recorded",
			Body:          "Gamics confirmed a false result report on your account. Repeated strikes block new registrations.",
			RecipientKind: notificationRecipientPayloadUser, RecipientID: testNotificationUserID,
			Data: map[string]any{"strikeId": testNotificationStrikeID}, Disposition: notificationDispositionProjected,
		}},
		{"player.strike_revoked", testNotificationUserID, strikePayload, notificationDefinition{
			Category: "account", Title: "Conduct strike removed", Body: "A conduct strike was removed from your account.",
			RecipientKind: notificationRecipientPayloadUser, RecipientID: testNotificationUserID,
			Data: map[string]any{"strikeId": testNotificationStrikeID}, Disposition: notificationDispositionProjected,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			want := tc.want
			want.Data = maps.Clone(tc.want.Data)
			want.Data["kind"] = tc.eventType
			got := notificationDefinitionForEvent(notificationTestEvent(t, tc.eventType, tc.aggregateID, tc.payload))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("definition = %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestEntryRemovedNotificationExplainsEachReason(t *testing.T) {
	cases := []struct {
		reasonCode string
		body       string
	}{
		{"response_timeout", "You didn't send your screenshot in time."},
		{"no_result_reported", "No score was reported before the deadline."},
		{"platform_review", "Gamics reviewed your match and removed your entry."},
	}
	for _, tc := range cases {
		t.Run(tc.reasonCode, func(t *testing.T) {
			definition := notificationDefinitionForEvent(notificationTestEvent(t, "competition.entry_removed",
				testNotificationEntryID, notificationTestRemovalPayload(tc.reasonCode)))
			if definition.Disposition != notificationDispositionProjected || definition.Body != tc.body {
				t.Fatalf("removal for %s = %q / %q", tc.reasonCode, definition.Disposition, definition.Body)
			}
		})
	}
}

func TestNotificationDefinitionRejectsMalformedResultPayloads(t *testing.T) {
	without := func(key string) map[string]any {
		payload := notificationTestRemovalPayload("response_timeout")
		delete(payload, key)
		return payload
	}
	cases := []struct {
		name        string
		eventType   string
		aggregateID string
		payload     any
	}{
		{"report without entry", "result.report_received", testNotificationMatchID, map[string]any{"matchId": testNotificationMatchID}},
		{"reminder with invalid entry", "result.report_reminder", testNotificationMatchID, map[string]any{"entryId": "entry-1"}},
		{"removal without entry", "competition.entry_removed", testNotificationEntryID, without("entryId")},
		{"removal without match", "competition.entry_removed", testNotificationEntryID, without("matchId")},
		{"removal without competition", "competition.entry_removed", testNotificationEntryID, without("competitionId")},
		{"removal without reason", "competition.entry_removed", testNotificationEntryID, without("reasonCode")},
		{"removal with null reason", "competition.entry_removed", testNotificationEntryID, notificationTestRemovalPayload(nil)},
		{"removal with numeric reason", "competition.entry_removed", testNotificationEntryID, notificationTestRemovalPayload(7)},
		{"removal with unknown reason", "competition.entry_removed", testNotificationEntryID, notificationTestRemovalPayload("referee")},
		{"strike without strike", "player.strike_recorded", testNotificationUserID, map[string]any{"userId": testNotificationUserID}},
		{"strike with invalid user", "player.strike_recorded", testNotificationUserID,
			map[string]any{"strikeId": testNotificationStrikeID, "userId": "user-1"}},
		{"revocation without user", "player.strike_revoked", testNotificationUserID, map[string]any{"strikeId": testNotificationStrikeID}},
		{"mismatch with array payload", "result.mismatch", testNotificationMatchID, []any{}},
		{"review with invalid aggregate", "result.under_review", "match-1", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition := notificationDefinitionForEvent(notificationTestEvent(t, tc.eventType, tc.aggregateID, tc.payload))
			if definition.Disposition != notificationDispositionMalformed {
				t.Fatalf("disposition = %q, want malformed", definition.Disposition)
			}
		})
	}
}

func TestRetiredRefereeAndSubmissionEventsAreIgnored(t *testing.T) {
	// Rows queued before the rollout are consumed as ignored rather than
	// sending players to retired referee routes.
	payload := map[string]any{"submissionId": "77777777-7777-4777-8777-777777777777",
		"caseId": "88888888-8888-4888-8888-888888888888", "requestedFrom": testNotificationUserID}
	for _, eventType := range []string{
		"result.submitted", "result.disputed", "dispute.evidence_requested", "dispute.evidence_submitted",
		"dispute.decision_recorded", "dispute.appeal_submitted", "dispute.appeal_decided",
		"dispute.decision_finalized_after_appeal_window",
	} {
		definition := notificationDefinitionForEvent(notificationTestEvent(t, eventType, testNotificationMatchID, payload))
		if definition.Disposition != notificationDispositionIgnored || definition.RecipientKind != "" || definition.ActionURL != "" {
			t.Errorf("retired event %q = %+v", eventType, definition)
		}
	}
	assertFileOmits(t, "notification_pipeline.go", "/referee-cases/", "result_submissions", "SubmissionID", `"dispute.`)
}

func TestResultNotificationsCarryIdentifiersOnly(t *testing.T) {
	// Producers keep claims out of outbox payloads; the projector must still
	// never forward one into an inbox row or a push.
	payload := map[string]any{
		"matchId": testNotificationMatchID, "entryId": testNotificationEntryID,
		"competitionId": testNotificationCompetitionID, "strikeId": testNotificationStrikeID,
		"userId": testNotificationUserID, "reasonCode": "platform_review",
		"homeScore": 7, "awayScore": 3, "reportedScore": "7-3",
		"tiebreak": map[string]any{"type": "penalties", "homeScore": 5, "awayScore": 4},
		"games":    []map[string]any{{"homeScore": 7, "awayScore": 3}},
	}
	events := []struct {
		eventType   string
		aggregateID string
	}{
		{"result.report_received", testNotificationMatchID}, {"result.report_reminder", testNotificationMatchID},
		{"result.mismatch", testNotificationMatchID}, {"result.under_review", testNotificationMatchID},
		{"result.review_decided", testNotificationMatchID}, {"match.result_confirmed", testNotificationMatchID},
		{"match.forfeited", testNotificationMatchID}, {"match.cancelled", testNotificationMatchID},
		{"competition.entry_removed", testNotificationEntryID},
		{"player.strike_recorded", testNotificationUserID}, {"player.strike_revoked", testNotificationUserID},
	}
	allowed := map[string]bool{"matchId": true, "competitionId": true, "strikeId": true}
	for _, event := range events {
		definition := notificationDefinitionForEvent(notificationTestEvent(t, event.eventType, event.aggregateID, payload))
		if definition.Disposition != notificationDispositionProjected {
			t.Fatalf("%s disposition = %q", event.eventType, definition.Disposition)
		}
		if definition.Data["kind"] != event.eventType {
			t.Errorf("%s kind = %v", event.eventType, definition.Data["kind"])
		}
		for key, value := range definition.Data {
			if key == "kind" {
				continue
			}
			id, isString := value.(string)
			if !allowed[key] || strings.Contains(strings.ToLower(key), "score") || !isString || !uuidPattern.MatchString(id) {
				t.Errorf("%s leaks %q=%v into notification data", event.eventType, key, value)
			}
		}
		if strings.ContainsAny(definition.Title+definition.Body, "0123456789") {
			t.Errorf("%s copy carries a number: %q / %q", event.eventType, definition.Title, definition.Body)
		}
	}
}

func TestNotificationStaleReportCheckRequiresOpenWindowAndOwedReport(t *testing.T) {
	for _, required := range []string{
		"verification.match_id=$1", "verification.phase='awaiting_confirmation'", "report_deadline_at>now()",
		"entry.id=$2 AND entry.status NOT IN ('withdrawn','disqualified')",
		"NOT EXISTS (SELECT 1 FROM match_result_reports report", "report.match_id=$1 AND report.entry_id=$2",
	} {
		if !strings.Contains(notificationAwaitingEntryReportSQL, required) {
			t.Errorf("stale report check is missing %q", required)
		}
	}
	// The check must settle the disposition before any recipient is resolved.
	assertFileOrder(t, "notification_pipeline.go", "func (s *Server) projectNotificationBatch",
		"tx.QueryRow(ctx, notificationAwaitingEntryReportSQL, event.AggregateID,",
		"definition.Disposition = notificationDispositionIgnored",
		"resolveNotificationRecipients(ctx, tx, event, definition)")
}

// notificationCaptureTx records the recipient query instead of running it.
type notificationCaptureTx struct {
	pgx.Tx
	query string
	args  []any
}

func (tx *notificationCaptureTx) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.query, tx.args = query, args
	return nil, errors.ErrUnsupported
}

func TestNotificationRecipientQueriesScopeEntries(t *testing.T) {
	cases := []struct {
		name        string
		aggregateID string
		definition  notificationDefinition
		wantArgs    []any
		required    []string
		forbidden   []string
	}{
		{
			name: "match skips withdrawn and removed entries", aggregateID: testNotificationMatchID,
			definition: notificationDefinition{RecipientKind: notificationRecipientMatch},
			wantArgs:   []any{testNotificationMatchID},
			required: []string{"entry.id IN (match.home_entry_id,match.away_entry_id)",
				"entry.status NOT IN ('withdrawn','disqualified')", "roster_role IN ('starter','substitute')",
				"player.status='active'", "WHERE match.id=$1"},
		},
		{
			name: "match entry belongs to the aggregate match", aggregateID: testNotificationMatchID,
			definition: notificationDefinition{RecipientKind: notificationRecipientMatchEntry, RecipientID: testNotificationEntryID},
			wantArgs:   []any{testNotificationMatchID, testNotificationEntryID},
			required: []string{"entry.id=$2 AND entry.id IN (match.home_entry_id,match.away_entry_id)",
				"roster_role IN ('starter','substitute')", "player.status='active'", "WHERE match.id=$1"},
		},
		{
			// A removal notice must still reach the entry it removed.
			name: "entry reaches its roster whatever its status", aggregateID: testNotificationEntryID,
			definition: notificationDefinition{RecipientKind: notificationRecipientEntry},
			wantArgs:   []any{testNotificationEntryID},
			required: []string{"WHERE entry.id=$1", "SELECT entry.captain_user_id",
				"roster_role IN ('starter','substitute')", "player.status='active'"},
			forbidden: []string{"entry.status"},
		},
		{
			name: "payload user ignores the aggregate", aggregateID: testNotificationMatchID,
			definition: notificationDefinition{RecipientKind: notificationRecipientPayloadUser, RecipientID: testNotificationUserID},
			wantArgs:   []any{testNotificationUserID},
			required:   []string{"WHERE id=$1 AND status='active'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &notificationCaptureTx{}
			event := notificationOutboxEvent{ID: testNotificationEventID, AggregateID: tc.aggregateID}
			if _, err := resolveNotificationRecipients(context.Background(), tx, event, tc.definition); !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("recipient query was not issued: %v", err)
			}
			if !reflect.DeepEqual(tx.args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", tx.args, tc.wantArgs)
			}
			for _, required := range tc.required {
				if !strings.Contains(tx.query, required) {
					t.Errorf("recipient query is missing %q", required)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(tx.query, forbidden) {
					t.Errorf("recipient query must not contain %q", forbidden)
				}
			}
		})
	}
	tx := &notificationCaptureTx{}
	recipients, err := resolveNotificationRecipients(context.Background(), tx,
		notificationOutboxEvent{ID: testNotificationEventID, AggregateID: testNotificationMatchID}, notificationDefinition{})
	if err != nil || recipients != nil || tx.query != "" {
		t.Fatalf("unknown recipient kind = %v, %v after query %q", recipients, err, tx.query)
	}
}

func TestNotificationDefinitionRejectsMalformedAndIgnoresUnmapped(t *testing.T) {
	malformed := notificationDefinitionForEvent(notificationOutboxEvent{
		ID: testNotificationEventID, AggregateID: testNotificationMatchID,
		EventType: "match.participant_checked_in", Payload: json.RawMessage(`{"userId":"not-a-uuid"}`),
	})
	if malformed.Disposition != notificationDispositionMalformed {
		t.Fatalf("malformed disposition = %q", malformed.Disposition)
	}
	ignored := notificationDefinitionForEvent(notificationOutboxEvent{
		ID: testNotificationEventID, AggregateID: testNotificationMatchID,
		EventType: "match.progression_applied", Payload: json.RawMessage(`{}`),
	})
	if ignored.Disposition != notificationDispositionIgnored {
		t.Fatalf("ignored disposition = %q", ignored.Disposition)
	}
}

func TestAccountDecisionIsInboxOnlyWithoutPreferenceContract(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"gameAccountId": testNotificationMatchID})
	definition := notificationDefinitionForEvent(notificationOutboxEvent{
		ID: testNotificationEventID, AggregateID: "66666666-6666-4666-8666-666666666666",
		EventType: "game_account.verification_approved", Payload: payload,
	})
	if definition.Disposition != notificationDispositionProjected || definition.PreferenceKey != "" {
		t.Fatalf("account decision must project without bypassing preferences: %+v", definition)
	}
}

func TestActualPlayerFacingProducerEventNamesAreMapped(t *testing.T) {
	defaultPayload := json.RawMessage(`{}`)
	reportPayload := json.RawMessage(`{"entryId":"44444444-4444-4444-8444-444444444444"}`)
	strikePayload := json.RawMessage(`{"strikeId":"66666666-6666-4666-8666-666666666666","userId":"33333333-3333-4333-8333-333333333333"}`)
	payloads := map[string]json.RawMessage{
		"match.participant_checked_in": json.RawMessage(`{"userId":"33333333-3333-4333-8333-333333333333"}`),
		"result.report_received":       reportPayload,
		"result.report_reminder":       reportPayload,
		"competition.entry_removed": json.RawMessage(`{"entryId":"44444444-4444-4444-8444-444444444444",` +
			`"competitionId":"55555555-5555-4555-8555-555555555555","matchId":"22222222-2222-4222-8222-222222222222",` +
			`"reasonCode":"response_timeout"}`),
		"player.strike_recorded":             strikePayload,
		"player.strike_revoked":              strikePayload,
		"game_account.verification_approved": json.RawMessage(`{"gameAccountId":"44444444-4444-4444-8444-444444444444"}`),
		"game_account.verification_rejected": json.RawMessage(`{"gameAccountId":"44444444-4444-4444-8444-444444444444"}`),
	}
	eventTypes := []string{
		"competition.check_in", "competition.running", "competition.cancelled", "competition.completed", "competition.draw_generated",
		"competition.entry_removed",
		"match.ready", "match.forfeited", "match.cancelled", "match.participant_checked_in", "match.result_confirmed",
		"result.report_received", "result.report_reminder", "result.mismatch", "result.under_review", "result.review_decided",
		"player.strike_recorded", "player.strike_revoked",
		"payment.succeeded", "payment.reconciliation_review_required", "payment.review_marked_failed",
		"payment.refund_requested", "payment.refund_approved", "payment.refund_rejected",
		"payment.refund_processing", "payment.refund_manual_review", "payment.refund_succeeded", "payment.refund_failed",
		"game_account.verification_approved", "game_account.verification_rejected",
	}
	for _, eventType := range eventTypes {
		payload := payloads[eventType]
		if payload == nil {
			payload = defaultPayload
		}
		definition := notificationDefinitionForEvent(notificationOutboxEvent{
			ID: testNotificationEventID, AggregateID: testNotificationMatchID,
			EventType: eventType, Payload: payload,
		})
		if definition.Disposition != notificationDispositionProjected {
			t.Errorf("producer event %q disposition = %q", eventType, definition.Disposition)
		}
	}
}

// TestIntegrationNotificationProjectorScopesResultEvents runs the production
// projector: a "report now" push reaches only the entry that still owes its
// report and goes stale with it, and a removed entry leaves generic match
// pushes but still receives its removal notice.
func TestIntegrationNotificationProjectorScopesResultEvents(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	ready := readyIntegrationMatches(t, pool, seeded.ID)
	if len(ready) != 2 {
		t.Fatalf("seeded %d ready matches, want 2", len(ready))
	}
	matchID, otherMatchID := ready[0], ready[1]
	home, away := notificationIntegrationSides(t, pool, matchID)
	otherHome, otherAway := notificationIntegrationSides(t, pool, otherMatchID)
	substituteID := notificationIntegrationMember(t, pool, seeded, away.ID, "substitute")
	notificationIntegrationMember(t, pool, seeded, away.ID, "coach")
	notificationIntegrationFirstReport(t, pool, seeded.ID, matchID, home)
	notificationIntegrationFirstReport(t, pool, seeded.ID, otherMatchID, otherHome)
	server := &Server{db: &database.Cluster{Writer: pool, Reader: pool}, config: config.Config{NotificationProject: 100}}
	matchPayload := map[string]any{"matchId": matchID}

	owed := notificationIntegrationEmit(t, pool, "match", matchID, "result.report_received",
		map[string]any{"matchId": matchID, "entryId": away.ID})
	reported := notificationIntegrationEmit(t, pool, "match", matchID, "result.report_received",
		map[string]any{"matchId": matchID, "entryId": home.ID})
	foreign := notificationIntegrationEmit(t, pool, "match", matchID, "result.report_reminder",
		map[string]any{"matchId": matchID, "entryId": otherAway.ID})
	mismatch := notificationIntegrationEmit(t, pool, "match", matchID, "result.mismatch", matchPayload)
	strike := notificationIntegrationEmit(t, pool, "user", home.UserID, "player.strike_recorded",
		map[string]any{"strikeId": testNotificationStrikeID, "userId": home.UserID, "matchId": matchID})
	retired := notificationIntegrationEmit(t, pool, "match", matchID, "result.submitted",
		map[string]any{"submissionId": testNotificationEventID})
	notificationIntegrationProject(t, server)
	notificationIntegrationExpect(t, pool, owed, notificationDispositionProjected, away.UserID, substituteID)
	notificationIntegrationExpect(t, pool, reported, notificationDispositionIgnored)
	notificationIntegrationExpect(t, pool, foreign, notificationDispositionProjected)
	notificationIntegrationExpect(t, pool, mismatch, notificationDispositionProjected, home.UserID, away.UserID, substituteID)
	notificationIntegrationExpect(t, pool, strike, notificationDispositionProjected, home.UserID)
	notificationIntegrationExpect(t, pool, retired, notificationDispositionIgnored)

	// The other match's window closes while its away entry is still silent,
	// and this match's away entry is removed while its window is still open.
	shiftVerificationClock(t, pool, otherMatchID, 11*time.Minute)
	if _, err := pool.Exec(t.Context(), `UPDATE competition_entries SET status='disqualified',updated_at=now()
		WHERE id=$1`, away.ID); err != nil {
		t.Fatal(err)
	}
	late := notificationIntegrationEmit(t, pool, "match", otherMatchID, "result.report_reminder",
		map[string]any{"matchId": otherMatchID, "entryId": otherAway.ID})
	removedReminder := notificationIntegrationEmit(t, pool, "match", matchID, "result.report_reminder",
		map[string]any{"matchId": matchID, "entryId": away.ID})
	decided := notificationIntegrationEmit(t, pool, "match", matchID, "result.review_decided", matchPayload)
	forfeited := notificationIntegrationEmit(t, pool, "match", matchID, "match.forfeited", matchPayload)
	removal := notificationIntegrationEmit(t, pool, "competition_entry", away.ID, "competition.entry_removed",
		map[string]any{"entryId": away.ID, "competitionId": seeded.ID, "matchId": matchID, "reasonCode": "response_timeout"})
	notificationIntegrationProject(t, server)
	notificationIntegrationExpect(t, pool, late, notificationDispositionIgnored)
	notificationIntegrationExpect(t, pool, removedReminder, notificationDispositionIgnored)
	notificationIntegrationExpect(t, pool, decided, notificationDispositionProjected, home.UserID)
	notificationIntegrationExpect(t, pool, forfeited, notificationDispositionProjected, home.UserID)
	notificationIntegrationExpect(t, pool, removal, notificationDispositionProjected, away.UserID, substituteID)

	var title, body, actionURL string
	var data map[string]string
	if err := pool.QueryRow(t.Context(), `SELECT title,body,action_url,data FROM notifications
		WHERE source_event_id=$1 AND user_id=$2`, removal, away.UserID).Scan(&title, &body, &actionURL, &data); err != nil {
		t.Fatal(err)
	}
	wantData := map[string]string{"competitionId": seeded.ID, "matchId": matchID, "kind": "competition.entry_removed"}
	if title != "Removed from tournament" || body != "You didn't send your screenshot in time." ||
		actionURL != "/matches/"+matchID || !maps.Equal(data, wantData) {
		t.Fatalf("removal notice = %q / %q / %q / %v", title, body, actionURL, data)
	}
}

func notificationIntegrationSides(t *testing.T, pool *pgxpool.Pool, matchID string) (home, away integrationEntry) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT home.id::text,home.captain_user_id::text,
		away.id::text,away.captain_user_id::text
		FROM matches match
		JOIN competition_entries home ON home.id=match.home_entry_id
		JOIN competition_entries away ON away.id=match.away_entry_id
		WHERE match.id=$1`, matchID).Scan(&home.ID, &home.UserID, &away.ID, &away.UserID); err != nil {
		t.Fatalf("load sides of %s: %v", matchID, err)
	}
	return home, away
}

// notificationIntegrationMember adds a non-captain roster member to an entry.
func notificationIntegrationMember(t *testing.T, pool *pgxpool.Pool, seeded integrationCompetition,
	entryID, rosterRole string) string {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	handle := rosterRole + "_" + strings.ToLower(rand.Text())[:12]
	userID, err := insertIntegrationUser(ctx, tx, handle, "Roster "+rosterRole)
	if err != nil {
		t.Fatalf("seed %s: %v", rosterRole, err)
	}
	var gameAccountID string
	if err = tx.QueryRow(ctx, `INSERT INTO game_accounts(user_id,game_id,platform,in_game_name)
		VALUES ($1,$2,'android',$3) RETURNING id`, userID, seeded.GameID, handle).Scan(&gameAccountID); err != nil {
		t.Fatalf("seed %s game account: %v", rosterRole, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id,roster_role)
		VALUES ($1,$2,$3,$4,$5)`, entryID, seeded.ID, userID, gameAccountID, rosterRole); err != nil {
		t.Fatalf("seed %s membership: %v", rosterRole, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return userID
}

// notificationIntegrationFirstReport leaves the match as T2 does: the
// reporter's entry has reported and the other entry has ten minutes left.
func notificationIntegrationFirstReport(t *testing.T, pool *pgxpool.Pool, competitionID, matchID string,
	reporter integrationEntry) {
	t.Helper()
	checkInBoth(t, pool, matchID)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var reportedVersion int
	if err = tx.QueryRow(ctx, `UPDATE matches SET state='awaiting_confirmation',version=version+1,updated_at=now()
		WHERE id=$1 AND state='in_progress' RETURNING version-1`, matchID).Scan(&reportedVersion); err != nil {
		t.Fatalf("start report window of %s: %v", matchID, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO match_result_verifications
		(match_id,competition_id,phase,first_report_entry_id,first_reported_at,report_window_seconds,
		 reminder_lead_seconds,response_window_seconds,report_deadline_at,reminder_at)
		VALUES ($1,$2,'awaiting_confirmation',$3,now(),600,180,600,now()+interval '600 seconds',
		 now()+interval '420 seconds')`, matchID, competitionID, reporter.ID); err != nil {
		t.Fatalf("open verification of %s: %v", matchID, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO match_result_reports
		(match_id,competition_id,entry_id,reported_by,kind,home_score,away_score,game_results,match_version)
		VALUES ($1,$2,$3,$4,'initial',2,1,'[{"homeScore":2,"awayScore":1}]'::jsonb,$5)`,
		matchID, competitionID, reporter.ID, reporter.UserID, reportedVersion); err != nil {
		t.Fatalf("insert first report of %s: %v", matchID, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func notificationIntegrationEmit(t *testing.T, pool *pgxpool.Pool, aggregateType, aggregateID, eventType string,
	payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err = pool.QueryRow(t.Context(), `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ($1,$2,$3,$4) RETURNING id::text`, aggregateType, aggregateID, eventType, json.RawMessage(raw)).
		Scan(&eventID); err != nil {
		t.Fatalf("emit %s: %v", eventType, err)
	}
	return eventID
}

// notificationIntegrationProject drains the projector queue.
func notificationIntegrationProject(t *testing.T, server *Server) {
	t.Helper()
	for {
		count, err := server.projectNotificationBatch(t.Context())
		if err != nil {
			t.Fatalf("project notifications: %v", err)
		}
		if count < server.config.NotificationProject {
			return
		}
	}
}

// notificationIntegrationExpect requires the event's recorded disposition and
// exactly the given inbox recipients.
func notificationIntegrationExpect(t *testing.T, pool *pgxpool.Pool, eventID, disposition string, want ...string) {
	t.Helper()
	ctx := t.Context()
	var gotDisposition string
	var recipientCount int
	if err := pool.QueryRow(ctx, `SELECT disposition,recipient_count FROM notification_outbox_consumptions
		WHERE source_event_id=$1`, eventID).Scan(&gotDisposition, &recipientCount); err != nil {
		t.Fatalf("event %s was not consumed: %v", eventID, err)
	}
	rows, err := pool.Query(ctx, `SELECT user_id::text FROM notifications WHERE source_event_id=$1
		ORDER BY user_id::text`, eventID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want = slices.Clone(want)
	slices.Sort(want)
	if gotDisposition != disposition || recipientCount != len(want) || !slices.Equal(got, want) {
		t.Fatalf("event %s: %s to %d %v, want %s to %v", eventID, gotDisposition, recipientCount, got, disposition, want)
	}
}

func TestExpoPushClientParsesTicketsAndAuthenticates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-expo-access" {
			t.Errorf("authorization header was not set")
		}
		var messages []expoPushMessage
		if err := json.NewDecoder(r.Body).Decode(&messages); err != nil {
			t.Fatal(err)
		}
		if len(messages) != 2 || messages[0].To != "ExpoPushToken[aaaaaaaaaaaaaaaaaaaa]" {
			t.Fatalf("messages = %#v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"status":"ok","id":"ticket-1"},{"status":"error","details":{"error":"DeviceNotRegistered"}}]}`))
	}))
	defer server.Close()
	client := &expoPushClient{endpoint: server.URL, accessToken: "private-expo-access", httpClient: server.Client()}
	tickets, err := client.Send(context.Background(), []expoPushMessage{
		{To: "ExpoPushToken[aaaaaaaaaaaaaaaaaaaa]", Title: "one", Body: "body", Data: map[string]any{}},
		{To: "ExpoPushToken[bbbbbbbbbbbbbbbbbbbb]", Title: "two", Body: "body", Data: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 2 || tickets[0].ID != "ticket-1" || normalizeExpoTicketError(tickets[1].Details.Error) != "DeviceNotRegistered" {
		t.Fatalf("tickets = %#v", tickets)
	}
}

func TestExpoReceiptClientMapsReceiptsByTicketID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-expo-access" {
			t.Errorf("authorization header was not set")
		}
		var request struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.IDs) != 2 || request.IDs[0] != "ticket-1" {
			t.Errorf("receipt IDs = %#v", request.IDs)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ticket-2":{"status":"error","details":{"error":"DeviceNotRegistered"}},"ticket-1":{"status":"ok"}}}`))
	}))
	defer server.Close()
	client := &expoPushClient{receiptsEndpoint: server.URL, accessToken: "private-expo-access", httpClient: server.Client()}
	receipts, err := client.Receipts(context.Background(), []string{"ticket-1", "ticket-2"})
	if err != nil {
		t.Fatal(err)
	}
	if receipts["ticket-1"].Status != "ok" || receipts["ticket-2"].Details.Error != "DeviceNotRegistered" {
		t.Fatalf("receipts = %#v", receipts)
	}
}

func TestExpoPushClientClassifiesProviderResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credentials are invalid and must not be logged", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &expoPushClient{endpoint: server.URL, accessToken: "secret", httpClient: server.Client()}
	_, err := client.Send(context.Background(), []expoPushMessage{{To: "token", Data: map[string]any{}}})
	var batchErr *expoPushBatchError
	if err == nil || !strings.Contains(err.Error(), "401") || !errorsAs(err, &batchErr) || !batchErr.Permanent {
		t.Fatalf("401 classification = %#v", err)
	}
	if strings.Contains(err.Error(), "credentials") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider response or token leaked into error: %v", err)
	}
}

func TestPushResolutionRevokesOnlyDeviceNotRegistered(t *testing.T) {
	claim := notificationPushClaim{DeliveryID: testNotificationEventID, Attempts: 1}
	ticket := expoPushTicket{Status: "error"}
	ticket.Details.Error = "DeviceNotRegistered"
	resolution := resolveNotificationPush(claim, 0, []expoPushTicket{ticket}, nil, 8)
	if resolution.State != "permanent_failure" || !resolution.RevokeRegisteredToken {
		t.Fatalf("DeviceNotRegistered resolution = %+v", resolution)
	}
	ticket.Details.Error = "MessageRateExceeded"
	resolution = resolveNotificationPush(claim, 0, []expoPushTicket{ticket}, nil, 8)
	if resolution.State != "retry" || resolution.RevokeRegisteredToken || resolution.RetryAfter <= 0 {
		t.Fatalf("rate-limit resolution = %+v", resolution)
	}
}

func TestReceiptResolutionRequiresFinalProviderReceipt(t *testing.T) {
	registeredToken := "ExpoPushToken[current-installation]"
	registeredHash := sha256.Sum256([]byte(registeredToken))
	claim := notificationReceiptClaim{
		DeliveryID: testNotificationEventID, TicketID: "ticket-1",
		PushToken: registeredToken, SentTokenHash: registeredHash[:],
		Attempts: 1, PushAttempts: 1,
	}
	ok := expoPushTicket{Status: "ok"}
	resolution := resolveNotificationReceipt(claim, map[string]expoPushTicket{"ticket-1": ok}, nil, 12, 8, 30*time.Second)
	if resolution.State != "delivered" {
		t.Fatalf("successful receipt = %+v", resolution)
	}
	resolution = resolveNotificationReceipt(claim, map[string]expoPushTicket{}, nil, 12, 8, 30*time.Second)
	if resolution.State != "accepted" || resolution.RetryAfter <= 0 {
		t.Fatalf("missing receipt must remain pending: %+v", resolution)
	}
	disabled := expoPushTicket{Status: "error"}
	disabled.Details.Error = "DeviceNotRegistered"
	resolution = resolveNotificationReceipt(claim, map[string]expoPushTicket{"ticket-1": disabled}, nil, 12, 8, 30*time.Second)
	if resolution.State != "permanent_failure" || !resolution.RevokeRegisteredToken {
		t.Fatalf("unregistered receipt = %+v", resolution)
	}
	rotatedHash := sha256.Sum256([]byte("ExpoPushToken[previous-installation]"))
	claim.SentTokenHash = rotatedHash[:]
	resolution = resolveNotificationReceipt(claim, map[string]expoPushTicket{"ticket-1": disabled}, nil, 12, 8, 30*time.Second)
	if resolution.RevokeRegisteredToken {
		t.Fatal("a receipt for a rotated token must not revoke the replacement token")
	}
	claim.SentTokenHash = registeredHash[:]
	rateLimited := expoPushTicket{Status: "error"}
	rateLimited.Details.Error = "MessageRateExceeded"
	resolution = resolveNotificationReceipt(claim, map[string]expoPushTicket{"ticket-1": rateLimited}, nil, 12, 8, 30*time.Second)
	if resolution.State != "retry" || resolution.RetryAfter <= 0 {
		t.Fatalf("rate-limited receipt = %+v", resolution)
	}
}

func TestNotificationRetryBackoffIsBoundedDeterministicAndJittered(t *testing.T) {
	first := notificationPushRetryDelay(1, testNotificationEventID)
	if first != notificationPushRetryDelay(1, testNotificationEventID) {
		t.Fatal("retry jitter must be deterministic for a delivery")
	}
	if first < 3750*time.Millisecond || first > 6250*time.Millisecond {
		t.Fatalf("first retry = %s", first)
	}
	if got := notificationPushRetryDelay(99, testNotificationEventID); got <= 0 || got > time.Hour {
		t.Fatalf("capped retry = %s", got)
	}
}

func TestNotificationProjectorClaimsIndexedConsumerQueue(t *testing.T) {
	for _, required := range []string{"notification_outbox_queue", "available_at<=now()", "notification_sequence", "SKIP LOCKED"} {
		if !strings.Contains(notificationProjectClaimSQL, required) {
			t.Fatalf("project claim is missing %q", required)
		}
	}
	for _, forbidden := range []string{"outbox_events", "notification_outbox_consumptions", "NOT EXISTS"} {
		if strings.Contains(notificationProjectClaimSQL, forbidden) {
			t.Fatalf("project claim still performs historical scan via %q", forbidden)
		}
	}
}

// Keep the test readable without binding it to the concrete error type in the
// condition expression.
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
