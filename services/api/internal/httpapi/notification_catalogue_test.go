package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The notification catalogue in docs/mobile-api-requirements.md is what the
// mobile app routes pushes by. These tests derive the mapped events from the
// pipeline source, so a new event cannot ship without a documented row.

const (
	testCataloguePaymentID      = "77777777-7777-4777-8777-777777777777"
	testCatalogueRefundID       = "88888888-8888-4888-8888-888888888888"
	testCatalogueVerificationID = "99999999-9999-4999-8999-999999999999"
	testCatalogueGameAccountID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

// notificationCatalogueDoc is the catalogue's path from this package.
var notificationCatalogueDoc = filepath.Join("..", "..", "..", "..", "docs", "mobile-api-requirements.md")

// mappedNotificationEventTypes returns every event type the pipeline's
// mapping switch handles, read from the source rather than a copied list.
func mappedNotificationEventTypes(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "notification_pipeline.go", nil, 0)
	if err != nil {
		t.Fatalf("parse notification_pipeline.go: %v", err)
	}
	var eventTypes []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "mapNotificationEvent" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switchStmt, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if tag, ok := switchStmt.Tag.(*ast.SelectorExpr); !ok || tag.Sel.Name != "EventType" {
				return true
			}
			for _, clause := range switchStmt.Body.List {
				for _, expression := range clause.(*ast.CaseClause).List {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("mapNotificationEvent has a non-literal case %T", expression)
					}
					eventType, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						t.Fatal(unquoteErr)
					}
					eventTypes = append(eventTypes, eventType)
				}
			}
			return false
		})
	}
	if len(eventTypes) == 0 {
		t.Fatal("no mapped event types found in mapNotificationEvent")
	}
	slices.Sort(eventTypes)
	return eventTypes
}

// catalogueTestEvent builds a valid event of any mapped type. The aggregate
// follows the producer of each event family, and the payload carries every
// key any mapping reads plus score fields that must never be forwarded.
func catalogueTestEvent(t *testing.T, eventType string) notificationOutboxEvent {
	t.Helper()
	var aggregateID string
	switch {
	case eventType == "competition.entry_removed":
		aggregateID = testNotificationEntryID
	case strings.HasPrefix(eventType, "competition."):
		aggregateID = testNotificationCompetitionID
	case strings.HasPrefix(eventType, "match."), strings.HasPrefix(eventType, "result."):
		aggregateID = testNotificationMatchID
	case strings.HasPrefix(eventType, "player."):
		aggregateID = testNotificationUserID
	case strings.HasPrefix(eventType, "payment.refund_"):
		aggregateID = testCatalogueRefundID
	case strings.HasPrefix(eventType, "payment."):
		aggregateID = testCataloguePaymentID
	case strings.HasPrefix(eventType, "game_account."):
		aggregateID = testCatalogueVerificationID
	default:
		t.Fatalf("no test aggregate for %s; extend catalogueTestEvent", eventType)
	}
	return notificationTestEvent(t, eventType, aggregateID, map[string]any{
		"userId": testNotificationUserID, "entryId": testNotificationEntryID, "matchId": testNotificationMatchID,
		"competitionId": testNotificationCompetitionID, "strikeId": testNotificationStrikeID,
		"gameAccountId": testCatalogueGameAccountID, "reasonCode": "response_timeout",
		"homeScore": 7, "awayScore": 3, "reportedScore": "7-3",
		"tiebreak": map[string]any{"type": "penalties", "homeScore": 5, "awayScore": 4},
	})
}

func TestEveryProjectedNotificationCarriesKindAndNoScore(t *testing.T) {
	statuses := []string{"requested", "approved", "rejected", "succeeded", "failed", "manual_review", "processing"}
	for _, eventType := range mappedNotificationEventTypes(t) {
		definition := notificationDefinitionForEvent(catalogueTestEvent(t, eventType))
		if definition.Disposition != notificationDispositionProjected {
			t.Errorf("%s disposition = %q", eventType, definition.Disposition)
			continue
		}
		if definition.Data["kind"] != eventType {
			t.Errorf("%s data.kind = %v, want the event type", eventType, definition.Data["kind"])
		}
		for key, value := range definition.Data {
			text, isString := value.(string)
			switch {
			case strings.Contains(strings.ToLower(key), "score"):
				t.Errorf("%s forwards score key %q", eventType, key)
			case !isString:
				t.Errorf("%s data %q is %T, want an identifier string", eventType, key, value)
			case key == "kind":
			case key == "status":
				if !slices.Contains(statuses, text) {
					t.Errorf("%s data.status = %q", eventType, text)
				}
			case !uuidPattern.MatchString(text):
				t.Errorf("%s data %q = %q, want a UUID", eventType, key, text)
			}
		}
	}
}

// notificationCatalogueRow is one row of the documented catalogue table.
type notificationCatalogueRow struct {
	Category, Delivery, Preference, Title, ActionURL string
	DataKeys                                         []string
}

// parseNotificationCatalogue reads the table under "Notification catalogue".
func parseNotificationCatalogue(t *testing.T) map[string]notificationCatalogueRow {
	t.Helper()
	doc := readSourceFile(t, notificationCatalogueDoc)
	_, section, found := strings.Cut(doc, "\n#### Notification catalogue\n")
	if !found {
		t.Fatalf("%s has no Notification catalogue section", notificationCatalogueDoc)
	}
	rows := map[string]notificationCatalogueRow{}
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 8 {
			t.Fatalf("catalogue row has %d cells, want 8: %s", len(cells), line)
		}
		for index := range cells {
			cells[index] = strings.TrimSpace(cells[index])
		}
		kind := strings.Trim(cells[0], "`")
		if _, duplicate := rows[kind]; duplicate {
			t.Errorf("catalogue lists %s twice", kind)
		}
		var keys []string
		for _, key := range strings.Split(cells[6], ",") {
			keys = append(keys, strings.Trim(strings.TrimSpace(key), "`"))
		}
		slices.Sort(keys)
		rows[kind] = notificationCatalogueRow{
			Category: strings.Trim(cells[1], "`"), Delivery: cells[2], Preference: strings.Trim(cells[3], "`"),
			Title: cells[4], ActionURL: strings.Trim(cells[5], "`"), DataKeys: keys,
		}
	}
	return rows
}

// documentedNotificationRow renders a definition the way the catalogue does:
// API preference names and actionUrl patterns with named placeholders.
func documentedNotificationRow(t *testing.T, definition notificationDefinition) notificationCatalogueRow {
	t.Helper()
	preferences := map[string]string{"": "none", "competition_push": "competitionPush",
		"match_push": "matchPush", "result_push": "resultPush"}
	preference, known := preferences[definition.PreferenceKey]
	if !known {
		t.Fatalf("preference %q has no API name", definition.PreferenceKey)
	}
	delivery := "inbox only"
	if definition.PreferenceKey != "" {
		delivery = "push"
	}
	actionURL := "null"
	if definition.ActionURL != "" {
		actionURL = strings.NewReplacer(testNotificationCompetitionID, "{competitionId}",
			testNotificationMatchID, "{matchId}", testCataloguePaymentID, "{paymentId}",
			testCatalogueRefundID, "{refundId}", testCatalogueGameAccountID, "{gameAccountId}").
			Replace(definition.ActionURL)
	}
	keys := slices.Sorted(maps.Keys(definition.Data))
	return notificationCatalogueRow{Category: definition.Category, Delivery: delivery, Preference: preference,
		Title: definition.Title, ActionURL: actionURL, DataKeys: keys}
}

func TestNotificationCatalogueDocumentsEveryMappedEvent(t *testing.T) {
	rows := parseNotificationCatalogue(t)
	mapped := mappedNotificationEventTypes(t)
	for _, eventType := range mapped {
		row, documented := rows[eventType]
		if !documented {
			t.Errorf("%s is mapped in notification_pipeline.go but missing from the catalogue in %s",
				eventType, notificationCatalogueDoc)
			continue
		}
		want := documentedNotificationRow(t, notificationDefinitionForEvent(catalogueTestEvent(t, eventType)))
		if row.Category != want.Category || row.Delivery != want.Delivery || row.Preference != want.Preference ||
			row.Title != want.Title || row.ActionURL != want.ActionURL || !slices.Equal(row.DataKeys, want.DataKeys) {
			t.Errorf("catalogue row for %s = %+v\nwant %+v", eventType, row, want)
		}
	}
	for kind := range rows {
		if !slices.Contains(mapped, kind) {
			t.Errorf("catalogue documents %s, which notification_pipeline.go does not map", kind)
		}
	}
}

func TestOpenAPINotificationSchemaListsEveryKind(t *testing.T) {
	mapped := mappedNotificationEventTypes(t)
	for _, name := range []string{"openapi.yaml", "identity-notifications.paths.yaml"} {
		contract := openAPIFile(t, name)
		notification := openAPIWords(openAPIBlock(t, contract, "Notification", 4))
		for _, kind := range mapped {
			if !strings.Contains(notification, kind) {
				t.Errorf("%s Notification schema does not list kind %s", name, kind)
			}
		}
		for _, fragment := range []string{"in-app route", "not an API path", "PushNotificationData"} {
			if !strings.Contains(notification, fragment) {
				t.Errorf("%s Notification schema does not say %q", name, fragment)
			}
		}
		category := openAPIEnum(t, openAPIBlock(t, openAPIBlock(t, contract, "Notification", 4), "category", 8))
		for _, kind := range mapped {
			definition := notificationDefinitionForEvent(catalogueTestEvent(t, kind))
			if !slices.Contains(category, definition.Category) {
				t.Errorf("%s Notification.category enum %v lacks %q used by %s", name, category, definition.Category, kind)
			}
		}
		push := openAPIWords(openAPIBlock(t, contract, "PushNotificationData", 4))
		for _, field := range []string{"notificationId", "kind", "actionUrl"} {
			if !strings.Contains(push, field+":") {
				t.Errorf("%s PushNotificationData does not declare %s", name, field)
			}
		}
	}
}
