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
		{"cancelled", matchRecord{State: "cancelled"}, "cancelled", nil},
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

func TestMatchRoomSaysWhoWonFromTheViewerSide(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	home, away, stranger := "home-entry", "away-entry", "other-entry"
	tests := []struct {
		name       string
		state      string
		winner     *string
		viewer     string
		lifecycle  string
		winnerSide string
		outcome    string
	}{
		{"home wins, home views", "completed", &home, "home", "completed", "home", "won"},
		{"home wins, away views", "completed", &home, "away", "completed", "home", "lost"},
		{"away wins, home views", "completed", &away, "home", "completed", "away", "lost"},
		{"away wins, away views", "completed", &away, "away", "completed", "away", "won"},
		{"round-robin draw", "completed", nil, "away", "completed", "", "drawn"},
		{"home wins by forfeit, home views", "forfeit", &home, "home", "forfeited", "home", "won"},
		{"home wins by forfeit, away views", "forfeit", &home, "away", "forfeited", "home", "lost"},
		{"away wins by forfeit, home views", "forfeit", &away, "home", "forfeited", "away", "lost"},
		{"away wins by forfeit, away views", "forfeit", &away, "away", "forfeited", "away", "won"},
		{"forfeit without a winner", "forfeit", nil, "home", "forfeited", "", "no_result"},
		{"winner outside the match", "completed", &stranger, "home", "completed", "", "no_result"},
		{"cancelled, home views", "cancelled", nil, "home", "cancelled", "", "no_result"},
		{"cancelled, away views", "cancelled", nil, "away", "cancelled", "", "no_result"},
		{"cancelled with a stale winner", "cancelled", &home, "home", "cancelled", "", "no_result"},
		{"pending", "pending", nil, "home", "assigned", "", ""},
		{"ready", "ready", nil, "away", "assigned", "", ""},
		{"in progress", "in_progress", nil, "home", "checked_in", "", ""},
		{"awaiting the second report", "awaiting_confirmation", nil, "home", "awaiting_resolution", "", ""},
		{"disputed", "disputed", nil, "away", "under_review", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := matchRecord{State: test.state, CurrentSide: test.viewer, HomeEntryID: &home, AwayEntryID: &away,
				WinnerEntryID: test.winner}
			room := record.response("player", now)
			summary := summarizeMatch(room)
			for name, got := range map[string][3]string{
				"room":    {room.Lifecycle, optionalValue(room.WinnerSide), optionalValue(room.Outcome)},
				"summary": {summary.Lifecycle, optionalValue(summary.WinnerSide), optionalValue(summary.Outcome)},
			} {
				if want := [3]string{test.lifecycle, test.winnerSide, test.outcome}; got != want {
					t.Fatalf("%s lifecycle/winnerSide/outcome = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestMatchRoomJSONCarriesTheWinnerAndOutcome(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	home, away, reason := "home-entry", "away-entry", "timeout_forfeit"
	record := matchRecord{ID: testMatchID, State: "forfeit", CompletionReason: &reason, CurrentSide: "away",
		HomeEntryID: &home, AwayEntryID: &away, WinnerEntryID: &home}
	decode := func(value any) map[string]json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	room := record.response("away-player", now)
	for name, fields := range map[string]map[string]json.RawMessage{"room": decode(room), "summary": decode(summarizeMatch(room))} {
		if string(fields["lifecycle"]) != `"forfeited"` || string(fields["winnerSide"]) != `"home"` ||
			string(fields["outcome"]) != `"lost"` {
			t.Fatalf("%s does not tell the away viewer they lost the forfeit: %v", name, fields)
		}
	}
	record.State, record.WinnerEntryID, record.CompletionReason = "in_progress", nil, nil
	fields := decode(record.response("away-player", now))
	if string(fields["winnerSide"]) != "null" || string(fields["outcome"]) != "null" {
		t.Fatalf("a live match must carry null winnerSide and outcome: %v", fields)
	}
}

func TestMatchRoomScopesEntryRemovedToTheRemovingMatch(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	home, away := "home-entry", "away-entry"
	removedAt := now.Add(-time.Hour)
	removal := func(side, entryID, reason string) matchRemovalView {
		return matchRemovalView{Side: side, EntryID: entryID, ReasonCode: reason, RemovedAt: removedAt}
	}
	disqualified := "disqualified"
	tests := []struct {
		name         string
		state        string
		winner       *string
		viewer       string
		status       *string
		removals     []matchRemovalView
		entryRemoved bool
		outcome      string
	}{
		{"an earlier win of an entry removed later", "completed", &home, "home", &disqualified, nil, false, "won"},
		{"the match that removed the viewer", "forfeit", &away, "home", &disqualified,
			[]matchRemovalView{removal("home", home, "report_timeout")}, true, "lost"},
		{"the winner sees the opponent's removal", "forfeit", &away, "away", nil,
			[]matchRemovalView{removal("home", home, "report_timeout")}, false, "won"},
		{"both entries removed", "cancelled", nil, "away", &disqualified,
			[]matchRemovalView{removal("home", home, "no_result_reported"), removal("away", away, "no_result_reported")},
			true, "no_result"},
		{"a double no-show removes nobody", "cancelled", nil, "home", nil, nil, false, "no_result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			viewerEntry := home
			if test.viewer == "away" {
				viewerEntry = away
			}
			record := matchRecord{State: test.state, CurrentSide: test.viewer, CurrentEntryID: viewerEntry,
				CurrentEntryStatus: test.status, HomeEntryID: &home, AwayEntryID: &away, WinnerEntryID: test.winner,
				Removals: test.removals}
			room := record.response("player", now)
			if room.ResultVerification.EntryRemoved != test.entryRemoved || optionalValue(room.Outcome) != test.outcome {
				t.Fatalf("entryRemoved=%v outcome=%q, want %v %q", room.ResultVerification.EntryRemoved,
					optionalValue(room.Outcome), test.entryRemoved, test.outcome)
			}
			if room.Removals == nil || len(room.Removals) != len(test.removals) ||
				(len(test.removals) > 0 && !slices.Equal(room.Removals, test.removals)) {
				t.Fatalf("removals = %+v, want %+v", room.Removals, test.removals)
			}
		})
	}
}

func TestMatchRoomRemovalsJSONCarriesNoScore(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	home, away := "home-entry", "away-entry"
	record := matchRecord{State: "forfeit", CurrentSide: "away", CurrentEntryID: away, HomeEntryID: &home,
		AwayEntryID: &away, WinnerEntryID: &away, ConfirmedResult: &matchConfirmedResultResponse{HomeScore: 7, AwayScore: 3}}
	raw, err := json.Marshal(record.response("away-player", now))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"removals":[]`) {
		t.Fatalf("a match without removals must carry an empty list: %s", raw)
	}
	if err = record.decodeRemovals([]byte(`[{"side":"home","entryId":"home-entry","reasonCode":"report_timeout",
		"removedAt":"2026-09-28T14:31:00.123456+03:00"}]`)); err != nil {
		t.Fatal(err)
	}
	if got := record.Removals[0].RemovedAt; !got.Equal(time.Date(2026, 9, 28, 11, 31, 0, 123456000, time.UTC)) ||
		got.Location() != time.UTC {
		t.Fatalf("removedAt was not normalized to UTC: %s", got)
	}
	raw, err = json.Marshal(record.response("away-player", now))
	if err != nil {
		t.Fatal(err)
	}
	var room struct {
		Removals []map[string]json.RawMessage `json:"removals"`
	}
	if err = json.Unmarshal(raw, &room); err != nil {
		t.Fatal(err)
	}
	if len(room.Removals) != 1 || !slices.Equal(slices.Sorted(maps.Keys(room.Removals[0])),
		[]string{"entryId", "reasonCode", "removedAt", "side"}) {
		t.Fatalf("unexpected removals: %s", raw)
	}
	if strings.Contains(string(raw), `"homeScore":7`) {
		t.Fatalf("a forfeit room leaks a score: %s", raw)
	}
	if err = (&matchRecord{}).decodeRemovals(nil); err != nil {
		t.Fatalf("no removal row must decode as empty: %v", err)
	}
}

func TestMatchPresentationNeverOffersFixturesOfEntriesThatLeft(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	opens, closes := now.Add(-5*time.Minute), now.Add(5*time.Minute)
	homeID, awayID := "home-player", "away-player"
	status := func(value string) *string { return &value }
	tests := []struct {
		name      string
		state     string
		status    *string
		lifecycle string
		actions   []string
	}{
		{"pending fixture of a removed entry", "pending", status("disqualified"), "out_of_competition", nil},
		{"pending fixture of a withdrawn entry", "pending", status("withdrawn"), "out_of_competition", nil},
		{"open check-in of a removed entry", "ready", status("disqualified"), "out_of_competition", nil},
		{"live match of a withdrawn entry", "in_progress", status("withdrawn"), "out_of_competition", nil},
		{"the removing forfeit stays forfeited", "forfeit", status("disqualified"), "forfeited", nil},
		{"a cancelled match stays cancelled", "cancelled", status("disqualified"), "cancelled", nil},
		{"pending fixture of a live entry", "pending", status("accepted"), "assigned", nil},
		{"a pending withdrawal still plays", "ready", status("withdrawal_pending"), "ready_for_check_in", []string{"check_in"}},
		{"open check-in of a live entry", "ready", nil, "ready_for_check_in", []string{"check_in"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := matchRecord{State: test.state, CurrentSide: "home", CurrentEntryStatus: test.status,
				HomePlayerID: &homeID, AwayPlayerID: &awayID}
			lifecycle, actions := record.presentation(now, &opens, &closes)
			if lifecycle != test.lifecycle || !slices.Equal(actions, test.actions) {
				t.Fatalf("presentation = %q %v, want %q %v", lifecycle, actions, test.lifecycle, test.actions)
			}
		})
	}
}

func TestMatchQueriesScopeRemovalsAndTheActiveList(t *testing.T) {
	for _, fragment := range []string{
		"m.state IN ('pending','ready','in_progress','awaiting_confirmation','disputed')",
		"AND mine_entry.status NOT IN ('withdrawn','disqualified')",
	} {
		if !strings.Contains(activeMatchClause, fragment) {
			t.Errorf("activeMatchClause does not contain %q", fragment)
		}
	}
	// Removals are read by primary key and only for this match.
	if !strings.Contains(matchSelectColumns,
		"WHERE removal.entry_id IN (m.home_entry_id,m.away_entry_id) AND removal.match_id=m.id") {
		t.Error("matchSelectColumns does not scope removals to this match")
	}
	if strings.Contains(matchSelectColumns, "mine_entry.status='disqualified'") {
		t.Error("entryRemoved must not come from the entry's current status")
	}
}

func TestOpenAPIMatchSummaryDeclaresTheOutcome(t *testing.T) {
	summary := openAPIBlock(t, openAPIFile(t, "openapi.yaml"), "MatchSummary", 4)
	for field, want := range map[string][]string{
		"winnerSide": {"home", "away", "null"},
		"outcome":    {"won", "lost", "drawn", "no_result", "null"},
	} {
		if !strings.Contains(summary, "\n        - "+field+"\n") {
			t.Errorf("MatchSummary does not require %s", field)
		}
		if got := openAPIEnum(t, openAPIBlock(t, summary, field, 8)); !slices.Equal(got, want) {
			t.Errorf("MatchSummary %s enum = %v, want %v", field, got, want)
		}
	}
}

func TestOpenAPIMatchRoomDeclaresItsRemovals(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	room := openAPIBlock(t, contract, "MatchRoom", 4)
	if !strings.Contains(room, "\n            - removals\n") || !strings.Contains(room, "#/components/schemas/MatchRemoval'") {
		t.Error("MatchRoom does not require its removals")
	}
	removal := openAPIBlock(t, contract, "MatchRemoval", 4)
	// The reason codes are exactly the competition_entry_removals CHECK list.
	if got := openAPIEnum(t, openAPIBlock(t, removal, "reasonCode", 8)); !slices.Equal(got,
		[]string{"report_timeout", "response_timeout", "no_result_reported", "platform_review"}) {
		t.Errorf("MatchRemoval reasonCode enum = %v", got)
	}
	if strings.Contains(strings.ToLower(removal), "score:") {
		t.Error("MatchRemoval must not carry a score")
	}
}

// optionalValue renders a nullable string for table comparisons.
func optionalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
