package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These DB-gated tests drive the match chat handlers on a ready, checked-in
// semifinal: posting, the room code, agreeing a time, reading, reporting, staff
// review, closing and retention.

func matchChatCall(t *testing.T, handler http.HandlerFunc, method, target, userID, pathKey, pathValue, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetPathValue(pathKey, pathValue)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func matchChatDecode[T any](t *testing.T, recorder *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return envelope.Data
}

func matchChatRefused(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	body := competitionVisibilityDecode[map[string]any](t, recorder, status)
	if body["error"] != code {
		t.Fatalf("error %v, want %s: %s", body["error"], code, recorder.Body.String())
	}
}

func matchChatGet(t *testing.T, server *Server, userID, matchID, query string) matchChatView {
	t.Helper()
	return matchChatDecode[matchChatView](t, matchChatCall(t, server.getMatchChat, http.MethodGet,
		"/v1/matches/"+matchID+"/chat"+query, userID, "matchId", matchID, ""), http.StatusOK)
}

func matchChatPost(t *testing.T, server *Server, userID, matchID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return matchChatCall(t, server.postMatchChatMessage, http.MethodPost, "/v1/matches/"+matchID+"/chat/messages",
		userID, "matchId", matchID, body)
}

func TestIntegrationMatchChat(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	match := sides.MatchID
	outsider := resultReportsInsertUser(t, pool, "outsider")

	empty := matchChatGet(t, server, sides.HomeUser, match, "")
	if !empty.Open || empty.LastSeq != 0 || len(empty.Messages) != 0 || empty.RoomCode != nil {
		t.Fatalf("new chat = %+v", empty)
	}
	matchChatRefused(t, matchChatCall(t, server.getMatchChat, http.MethodGet, "/", outsider, "matchId", match, ""),
		http.StatusNotFound, "match_not_found")
	matchChatRefused(t, matchChatPost(t, server, outsider, match, `{"clientMessageId":"x1","kind":"text","text":"hi"}`),
		http.StatusNotFound, "match_not_found")

	// Contact details are hidden, and a retry with the same id is the same message.
	first := `{"clientMessageId":"home-1","kind":"text","text":"call me 0712 345 678 or wa.me/254712345678"}`
	sent := matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.HomeUser, match, first), http.StatusCreated)
	if sent.Seq != 1 || sent.Sender != "home" || !sent.Mine || !sent.ContactHidden || sent.Text == nil ||
		strings.ContainsAny(*sent.Text, "0123456789") || !strings.Contains(*sent.Text, "[hidden]") {
		t.Fatalf("first message = %+v", sent)
	}
	replay := matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.HomeUser, match, first), http.StatusOK)
	if replay.Seq != 1 {
		t.Fatalf("replay = %+v", replay)
	}
	for _, body := range []string{
		`{"clientMessageId":"bad","kind":"text","text":"  "}`,
		`{"clientMessageId":"bad","kind":"text","text":"hi","quickReply":"ready_now"}`,
		`{"clientMessageId":"bad","kind":"quick_reply","quickReply":"see_you"}`,
		`{"clientMessageId":"bad","kind":"room_code","roomCode":"<script>"}`,
		`{"clientMessageId":"bad","kind":"time_answer","replyToSeq":1}`,
		`{"clientMessageId":"","kind":"text","text":"hi"}`,
	} {
		matchChatRefused(t, matchChatPost(t, server, sides.HomeUser, match, body), http.StatusBadRequest, "invalid_chat_message")
	}

	// The room code is pinned for both players.
	matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.AwayUser, match,
		`{"clientMessageId":"away-1","kind":"room_code","roomCode":" ABC 123 "}`), http.StatusCreated)
	pinned := matchChatGet(t, server, sides.HomeUser, match, "")
	if pinned.RoomCode == nil || pinned.RoomCode.Code != "ABC 123" || pinned.RoomCode.SetBy != "away" {
		t.Fatalf("room code = %+v", pinned.RoomCode)
	}

	// A proposed time must fall before the match has to be played.
	now := time.Now().UTC()
	proposed := now.Add(time.Hour)
	if pinned.PlayBy != nil {
		proposed = now.Add(pinned.PlayBy.Sub(now) / 2)
		late := pinned.PlayBy.Add(time.Hour).Format(time.RFC3339)
		matchChatRefused(t, matchChatPost(t, server, sides.HomeUser, match,
			`{"clientMessageId":"late","kind":"time_proposal","proposedStartAt":"`+late+`"}`),
			http.StatusBadRequest, "invalid_proposed_time")
	}
	proposal := matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.HomeUser, match,
		`{"clientMessageId":"home-2","kind":"time_proposal","proposedStartAt":"`+proposed.Format(time.RFC3339)+`"}`),
		http.StatusCreated)
	matchChatRefused(t, matchChatPost(t, server, sides.HomeUser, match,
		`{"clientMessageId":"home-3","kind":"time_answer","replyToSeq":3,"accepted":true}`), http.StatusConflict, "own_proposal")
	matchChatRefused(t, matchChatPost(t, server, sides.AwayUser, match,
		`{"clientMessageId":"away-2","kind":"time_answer","replyToSeq":1,"accepted":true}`), http.StatusConflict,
		"proposal_not_current")
	matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.AwayUser, match,
		`{"clientMessageId":"away-3","kind":"time_answer","replyToSeq":3,"accepted":true}`), http.StatusCreated)
	matchChatRefused(t, matchChatPost(t, server, sides.AwayUser, match,
		`{"clientMessageId":"away-4","kind":"time_answer","replyToSeq":3,"accepted":false}`), http.StatusConflict,
		"proposal_answered")

	// Home sent seq 3 and away answered with seq 4, so home has one unread.
	room := resultReportsRoom(t, server, sides.HomeUser, match)
	if room.Chat.LastSeq != 4 || room.Chat.Unread != 1 || room.Chat.RoomCode == nil || *room.Chat.RoomCode != "ABC 123" ||
		room.Chat.AgreedStartAt == nil || !room.Chat.AgreedStartAt.Equal(*proposal.ProposedStartAt) {
		t.Fatalf("home room chat = %+v", room.Chat)
	}
	if read := matchChatCall(t, server.readMatchChat, http.MethodPost, "/", sides.HomeUser, "matchId", match,
		`{"seq":4}`); read.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", read.Code, read.Body.String())
	}
	if unread := resultReportsRoom(t, server, sides.HomeUser, match).Chat.Unread; unread != 0 {
		t.Fatalf("home unread after reading = %d", unread)
	}
	page := matchChatGet(t, server, sides.AwayUser, match, "?after=2&limit=1")
	if len(page.Messages) != 1 || page.Messages[0].Seq != 3 || !page.HasMore || page.OpponentReadSeq != 4 ||
		page.Messages[0].Mine {
		t.Fatalf("page = %+v", page)
	}
	// Each opponent got one push: the next messages fell inside the spacing.
	if pushes := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='match.chat_message'
		AND aggregate_id=$1`, match); pushes != 2 {
		t.Fatalf("%d chat pushes, want 2", pushes)
	}

	// Reports name the opponent's message once.
	report := matchChatDecode[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, matchChatCall(t, server.reportMatchChatMessage, http.MethodPost, "/", sides.AwayUser, "matchId", match,
		`{"seq":1,"reason":"harassment","note":"keeps messaging"}`), http.StatusCreated)
	matchChatRefused(t, matchChatCall(t, server.reportMatchChatMessage, http.MethodPost, "/", sides.AwayUser, "matchId",
		match, `{"seq":1,"reason":"spam"}`), http.StatusConflict, "chat_message_already_reported")
	matchChatRefused(t, matchChatCall(t, server.reportMatchChatMessage, http.MethodPost, "/", sides.AwayUser, "matchId",
		match, `{"seq":2,"reason":"spam"}`), http.StatusConflict, "own_chat_message")

	// Staff read the transcript, which is audited, and resolve the report.
	staff := resultReportsInsertUser(t, pool, "staff")
	transcript := matchChatDecode[struct {
		Messages []matchChatMessageView `json:"messages"`
		Reports  []matchChatReportView  `json:"reports"`
	}](t, matchChatCall(t, server.getAdminMatchChat, http.MethodGet, "/", staff, "matchId", match, ""), http.StatusOK)
	if len(transcript.Messages) != 4 || len(transcript.Reports) != 1 || transcript.Reports[0].Reason != "harassment" {
		t.Fatalf("transcript = %+v", transcript)
	}
	if views := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE action='match_chat.viewed'
		AND actor_user_id=$1`, staff); views != 1 {
		t.Fatalf("%d audited transcript reads, want 1", views)
	}
	open := matchChatDecode[[]matchChatReportView](t, matchChatCall(t, server.listMatchChatReports, http.MethodGet,
		"/v1/admin/chat-reports", staff, "", "", ""), http.StatusOK)
	if len(open) != 1 || open[0].ID != report.ID || open[0].Reported.UserID != sides.HomeUser {
		t.Fatalf("open reports = %+v", open)
	}
	resolved := matchChatDecode[matchChatReportView](t, matchChatCall(t, server.resolveMatchChatReport, http.MethodPost,
		"/", staff, "id", report.ID, `{"outcome":"dismissed","note":"banter"}`), http.StatusOK)
	if resolved.Status != "dismissed" || resolved.ResolvedBy == nil || *resolved.ResolvedBy != staff {
		t.Fatalf("resolved = %+v", resolved)
	}
	matchChatRefused(t, matchChatCall(t, server.resolveMatchChatReport, http.MethodPost, "/", staff, "id", report.ID,
		`{"outcome":"actioned"}`), http.StatusConflict, "chat_report_resolved")

	// A final match leaves the chat read-only; a retry still answers.
	resultFlowExec(t, pool, `UPDATE matches SET state='completed',completed_at=now(),completion_reason='played' WHERE id=$1`, match)
	matchChatRefused(t, matchChatPost(t, server, sides.HomeUser, match, `{"clientMessageId":"home-9","kind":"text","text":"gg"}`),
		http.StatusConflict, "chat_closed")
	matchChatDecode[matchChatMessageView](t, matchChatPost(t, server, sides.HomeUser, match, first), http.StatusOK)
	if closed := matchChatGet(t, server, sides.HomeUser, match, ""); closed.Open || len(closed.Messages) != 4 {
		t.Fatalf("closed chat = %+v", closed)
	}

	// Retention: kept for 7 days after the match, and for 30 days after a report is resolved.
	resultFlowExec(t, pool, `UPDATE matches SET completed_at=now()-interval '8 days' WHERE id=$1`, match)
	if deleted := server.deleteExpiredMatchChats(t.Context()); deleted != 0 {
		t.Fatalf("deleted %d chats with a report resolved today", deleted)
	}
	resultFlowExec(t, pool, `UPDATE match_chat_reports SET resolved_at=now()-interval '31 days' WHERE match_id=$1`, match)
	if deleted := server.deleteExpiredMatchChats(t.Context()); deleted != 1 {
		t.Fatalf("deleted %d chats, want 1", deleted)
	}
	if left := resultReportsCount(t, pool, `SELECT count(*) FROM match_chat_messages WHERE match_id=$1`, match); left != 0 {
		t.Fatalf("%d messages survived the chat", left)
	}
}
