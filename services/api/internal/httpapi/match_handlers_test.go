package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

const testMatchID = "018f0d5e-7b7a-4f31-a955-37fc0b6fb111"

func TestMatchRoutesRequireAuthentication(t *testing.T) {
	server := newMatchTestServer()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/me/matches"},
		{http.MethodGet, "/v1/matches/" + testMatchID},
		{http.MethodPost, "/v1/matches/" + testMatchID + "/check-ins"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		server.http.Handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_required") {
			t.Fatalf("%s %s: expected authenticated route, got %d %q", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMatchCursorRoundTripAndTamperResistance(t *testing.T) {
	server := newMatchTestServer()
	record := matchRecord{ID: testMatchID, SortAt: time.Date(2026, 8, 9, 12, 30, 0, 123, time.UTC)}
	encoded := server.encodeMatchCursor("active", record)
	decoded, err := server.decodeMatchCursor(encoded, "active")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != record.ID || decoded.SortAt != record.SortAt.UnixNano() || decoded.State != "active" {
		t.Fatalf("unexpected decoded cursor: %+v", decoded)
	}
	if _, err := server.decodeMatchCursor(encoded, "history"); err == nil {
		t.Fatal("cursor must not be reusable across state buckets")
	}
	replacement := "A"
	if strings.HasSuffix(encoded, replacement) {
		replacement = "B"
	}
	tampered := encoded[:len(encoded)-1] + replacement
	if _, err := server.decodeMatchCursor(tampered, "active"); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestMatchPresentationDerivesAllowedActions(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	opens := now.Add(-5 * time.Minute)
	closes := now.Add(5 * time.Minute)
	due := now.Add(time.Hour)
	homeID, awayID := "home-player", "away-player"
	record := matchRecord{
		State: "ready", CurrentSide: "home", HomePlayerID: &homeID, AwayPlayerID: &awayID,
		CheckInOpensAt: &opens, CheckInClosesAt: &closes, ResultDueAt: &due,
	}
	lifecycle, actions := record.presentation(now, &opens, &closes)
	if lifecycle != "ready_for_check_in" || !slices.Equal(actions, []string{"check_in"}) {
		t.Fatalf("unexpected ready presentation: %q %v", lifecycle, actions)
	}

	checkedIn := now.Add(-time.Minute)
	record.HomeCheckedInAt = &checkedIn
	lifecycle, actions = record.presentation(now, &opens, &closes)
	if lifecycle != "checked_in" || len(actions) != 0 {
		t.Fatalf("unexpected one-sided check-in presentation: %q %v", lifecycle, actions)
	}
}

func TestMatchPresentationFollowsTheBlindReportPhases(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	checkedIn, open := now.Add(-time.Minute), now.Add(time.Minute)
	at := func(value time.Time) *time.Time { return &value }
	phase := func(value string) *string { return &value }
	mine := &scoreReportView{Kind: "initial", HomeScore: 2, AwayScore: 1}
	mineFinal := &scoreReportView{Kind: "final", HomeScore: 2, AwayScore: 1}
	tests := []struct {
		name      string
		record    matchRecord
		lifecycle string
		actions   []string
	}{
		{"both checked in and nobody reported", matchRecord{State: "in_progress", HomeCheckedInAt: &checkedIn,
			AwayCheckedInAt: &checkedIn, ResultDueAt: at(open)}, "report_required", []string{"report_score"}},
		{"one side checked in", matchRecord{State: "in_progress", HomeCheckedInAt: &checkedIn, ResultDueAt: at(open)},
			"checked_in", nil},
		{"result deadline reached", matchRecord{State: "in_progress", HomeCheckedInAt: &checkedIn,
			AwayCheckedInAt: &checkedIn, ResultDueAt: at(now)}, "awaiting_resolution", nil},
		{"my entry reported first", matchRecord{State: "awaiting_confirmation", VerificationPhase: phase("awaiting_second_report"),
			ReportDeadlineAt: at(open), MyInitialReport: mine}, "awaiting_opponent_report", nil},
		{"the other entry reported first", matchRecord{State: "awaiting_confirmation",
			VerificationPhase: phase("awaiting_second_report"), ReportDeadlineAt: at(open)}, "report_required", []string{"report_score"}},
		{"report window reached", matchRecord{State: "awaiting_confirmation",
			VerificationPhase: phase("awaiting_second_report"), ReportDeadlineAt: at(now)}, "awaiting_resolution", nil},
		{"my entry responded", matchRecord{State: "disputed", VerificationPhase: phase("awaiting_responses"),
			ResponseDeadlineAt: at(open), MyInitialReport: mine, MyFinalReport: mineFinal}, "awaiting_opponent_response", nil},
		{"mismatch awaits my response", matchRecord{State: "disputed", VerificationPhase: phase("awaiting_responses"),
			ResponseDeadlineAt: at(open), MyInitialReport: mine}, "mismatch_response_required", []string{"submit_final_score"}},
		{"response window reached", matchRecord{State: "disputed", VerificationPhase: phase("awaiting_responses"),
			ResponseDeadlineAt: at(now), MyInitialReport: mine}, "awaiting_resolution", nil},
		{"Gamics review", matchRecord{State: "disputed", VerificationPhase: phase("in_review"), MyInitialReport: mine},
			"under_review", nil},
		{"forfeit", matchRecord{State: "forfeit"}, "forfeited", nil},
		{"completed", matchRecord{State: "completed"}, "completed", nil},
		{"cancelled", matchRecord{State: "cancelled"}, "completed", nil},
		{"pending", matchRecord{State: "pending"}, "assigned", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle, actions := test.record.presentation(now, nil, nil)
			if lifecycle != test.lifecycle || !slices.Equal(actions, test.actions) {
				t.Fatalf("presentation = %q %v, want %q %v", lifecycle, actions, test.lifecycle, test.actions)
			}
		})
	}
}

func TestMatchRoomNeverRevealsTheOtherEntryClaim(t *testing.T) {
	// The other entry reported 7-3. Side B's record carries only its own 1-0 and
	// the other entry's booleans, and the room must not grow a field for more.
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(8 * time.Minute)
	phase := "awaiting_responses"
	record := matchRecord{
		ID: testMatchID, State: "disputed", CurrentSide: "away", VerificationPhase: &phase, ResponseDeadlineAt: &deadline,
		MyInitialReport: &scoreReportView{Kind: "initial", HomeScore: 1, AwayScore: 0,
			Games: []gameScoreInput{{HomeScore: 1, AwayScore: 0}}, ReportedAt: now.Add(-time.Minute)},
		OpponentReported: true, OpponentResponded: true,
	}
	raw, err := json.Marshal(record.response("away-player", now))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"homeScore":7`, `"awayScore":3`, "pendingResult", "opponentReport\""} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("room leaks %s: %s", forbidden, raw)
		}
	}
	var room struct {
		ResultVerification map[string]json.RawMessage `json:"resultVerification"`
		Result             json.RawMessage            `json:"result"`
	}
	if err = json.Unmarshal(raw, &room); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(room.ResultVerification))
	want := []string{"entryRemoved", "myFinalReport", "myReport", "opponentReported", "opponentResponded",
		"phase", "reportDeadline", "resolution", "responseDeadline"}
	if !slices.Equal(keys, want) {
		t.Fatalf("resultVerification keys = %v, want %v", keys, want)
	}
	if string(room.ResultVerification["opponentReported"]) != "true" || string(room.ResultVerification["opponentResponded"]) != "true" ||
		string(room.ResultVerification["reportDeadline"]) != "null" || string(room.Result) != "null" {
		t.Fatalf("unexpected blind view: %s", raw)
	}
}

func TestMatchRoomShowsTheConfirmedResultOnlyWhenCompleted(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record := matchRecord{State: "completed", ConfirmedResult: &matchConfirmedResultResponse{
		HomeScore: 2, AwayScore: 1, Origin: "agreed_reports", ConfirmedAt: now,
	}}
	if room := record.response("player", now); room.Result == nil || room.Result.HomeScore != 2 {
		t.Fatalf("completed match hides its result: %+v", room.Result)
	}
	for _, state := range []string{"forfeit", "cancelled", "disputed"} {
		record.State = state
		if room := record.response("player", now); room.Result != nil {
			t.Fatalf("%s match shows a result: %+v", state, room.Result)
		}
	}
}

func TestMatchRecordDecodesTheRoomQueryJSON(t *testing.T) {
	var record matchRecord
	reports := []byte(`{"initial":{"id":"4d3e5536-bf3c-4dba-a643-575e43f56970","kind":"initial","homeScore":1,"awayScore":1,
		"tiebreak":{"type":"penalties","homeScore":4,"awayScore":3},"games":[{"homeScore":1,"awayScore":1}],
		"reportedAt":"2026-09-28T14:31:00.123456+03:00","evidenceIds":[]},
		"final":{"id":"5d3e5536-bf3c-4dba-a643-575e43f56970","kind":"final","homeScore":2,"awayScore":1,"tiebreak":null,
		"games":[{"homeScore":2,"awayScore":1}],"reportedAt":"2026-09-28T11:40:00+00:00",
		"evidenceIds":["6d3e5536-bf3c-4dba-a643-575e43f56970"]}}`)
	confirmed := []byte(`{"homeScore":2,"awayScore":1,"tiebreak":null,"origin":"agreed_reports",
		"confirmedAt":"2026-09-28T14:45:00+03:00"}`)
	if err := record.decodeResultViews(reports, confirmed); err != nil {
		t.Fatal(err)
	}
	initial, final := record.MyInitialReport, record.MyFinalReport
	if initial == nil || final == nil || initial.Tiebreak == nil || initial.Tiebreak.HomeScore != 4 ||
		!initial.ReportedAt.Equal(time.Date(2026, 9, 28, 11, 31, 0, 123456000, time.UTC)) || initial.ReportedAt.Location() != time.UTC ||
		len(final.EvidenceIDs) != 1 || final.Tiebreak != nil {
		t.Fatalf("unexpected reports: %+v %+v", initial, final)
	}
	if record.ConfirmedResult == nil || record.ConfirmedResult.ConfirmedAt.Hour() != 11 {
		t.Fatalf("unexpected confirmed result: %+v", record.ConfirmedResult)
	}
	if err := (&matchRecord{}).decodeResultViews(nil, nil); err != nil {
		t.Fatalf("absent views must decode as empty: %v", err)
	}
}

func TestCheckInDeadlinesAreServerEnforced(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	homeID, awayID := "home", "away"
	record := matchRecord{State: "ready", HomePlayerID: &homeID, AwayPlayerID: &awayID}
	opens, closes := now.Add(time.Minute), now.Add(10*time.Minute)
	record.CheckInOpensAt, record.CheckInClosesAt = &opens, &closes
	code, _, retry := record.checkInRejection(now)
	if code != "check_in_not_open" || retry != time.Minute {
		t.Fatalf("unexpected early rejection: %q %s", code, retry)
	}
	pastOpen, pastClose := now.Add(-10*time.Minute), now.Add(-time.Second)
	record.CheckInOpensAt, record.CheckInClosesAt = &pastOpen, &pastClose
	code, _, _ = record.checkInRejection(now)
	if code != "check_in_closed" {
		t.Fatalf("unexpected late rejection: %q", code)
	}
	record.State = "in_progress"
	code, _, _ = record.checkInRejection(now)
	if code != "match_not_ready" {
		t.Fatalf("unexpected state rejection: %q", code)
	}
}

func TestMatchSettingsUseValidatedStageOverrides(t *testing.T) {
	competition := []byte(`{"drawAllowed":false,"friendMatchInstructions":[{"title":"Competition rule","detail":"Use this setting."}]}`)
	stage := []byte(`{"drawAllowed":true,"friendMatchInstructions":[{"title":"Final rule","detail":"Use the final setting."}]}`)
	settings := resolveMatchSettings("efootball-mobile", "single_elimination", competition, stage)
	if !settings.DrawAllowed || len(settings.Instructions) != 1 || settings.Instructions[0].Title != "Final rule" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	invalid := []byte(`{"friendMatchInstructions":[{"title":"","detail":"missing title"}]}`)
	settings = resolveMatchSettings("efootball-mobile", "single_elimination", nil, invalid)
	if len(settings.Instructions) != 4 {
		t.Fatalf("invalid rules must fall back to safe defaults: %+v", settings.Instructions)
	}
}

func TestMatchVerificationPolicyUsesTheSnapshotOnceReported(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	record := matchRecord{
		State: "in_progress", GameID: "efootball-mobile", StageFormat: "single_elimination",
		RulesSnapshot: []byte(`{"matchVerification":{"reportWindowMinutes":20,"reminderBeforeDeadlineMinutes":5,"responseWindowMinutes":15}}`),
	}
	policy := record.response("player", now).VerificationPolicy
	if policy.ReportWindowSeconds != 1200 || policy.ReminderBeforeDeadlineSeconds != 300 || policy.ResponseWindowSeconds != 900 {
		t.Fatalf("rules were not applied before the first report: %+v", policy)
	}
	if policy.FinalReportEvidence.MinItems != 1 || policy.FinalReportEvidence.MaxItems != 3 ||
		!slices.Equal(policy.FinalReportEvidence.MediaTypes, []string{"image/jpeg", "image/png"}) {
		t.Fatalf("unexpected final report evidence policy: %+v", policy.FinalReportEvidence)
	}

	reportWindow, reminderLead, responseWindow := 600, 180, 600
	record.ReportWindowSeconds, record.ReminderLeadSeconds, record.ResponseWindowSeconds = &reportWindow, &reminderLead, &responseWindow
	policy = record.response("player", now).VerificationPolicy
	if policy.ReportWindowSeconds != 600 || policy.ReminderBeforeDeadlineSeconds != 180 || policy.ResponseWindowSeconds != 600 {
		t.Fatalf("a rules edit moved the snapshotted windows: %+v", policy)
	}
}

func TestMatchListRequestValidation(t *testing.T) {
	server := newMatchTestServer()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/me/matches?state=active&limit=50", nil)
	state, limit, cursor, ok := server.parseMatchPageRequest(recorder, request)
	if !ok || state != "active" || limit != 50 || cursor != nil {
		t.Fatalf("unexpected page request: %q %d %+v %v", state, limit, cursor, ok)
	}
	for _, query := range []string{"state=unknown", "state=history&limit=0", "state=active&limit=51", "cursor=not-signed"} {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/v1/me/matches?"+query, nil)
		if _, _, _, ok := server.parseMatchPageRequest(recorder, request); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected %q to be rejected, got %d", query, recorder.Code)
		}
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	for _, value := range []string{"retry-key-123", "018f0d5e-7b7a-4f31-a955-37fc0b6fb111"} {
		if !validIdempotencyKey(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"short", "contains space", strings.Repeat("x", 129)} {
		if validIdempotencyKey(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}

func newMatchTestServer() *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{AccessTokenSecret: "match-test-secret", RequestTimeout: time.Second}, logger, "test")
}
