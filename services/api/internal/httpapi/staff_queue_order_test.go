package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// Lists of every status show open records first, oldest first, then finished
// records, newest first.
func TestStaffQueueKeyOrdersOpenFirstThenNewestFinished(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	oldOpen := staffQueueKey(true, base.Add(-48*time.Hour), time.Time{})
	newOpen := staffQueueKey(true, base, time.Time{})
	newFinished := staffQueueKey(false, time.Time{}, base)
	oldFinished := staffQueueKey(false, time.Time{}, base.Add(-48*time.Hour))
	keys := []int64{oldOpen, newOpen, newFinished, oldFinished}
	if !slices.IsSorted(keys) {
		t.Fatalf("keys %v are not open-oldest, open-newest, finished-newest, finished-oldest", keys)
	}
	for _, key := range keys {
		if key <= 0 {
			t.Fatalf("key %d is not positive, which the cursor check requires", key)
		}
	}
	if !strings.Contains(staffQueueOrderKey("open", "a", "b"), "9000000000000000000") {
		t.Fatal("the SQL order key and finishedQueueKeyBase disagree")
	}
	if finishedQueueKeyBase != 9_000_000_000_000_000_000 {
		t.Fatal("finishedQueueKeyBase changed without the SQL expression")
	}
}

// TestIntegrationVerificationQueueListsEveryStatus pages through every
// verification request two at a time and checks the order across both groups.
func TestIntegrationVerificationQueueListsEveryStatus(t *testing.T) {
	h := newRegistrationHarness(t)
	suffix := strings.ToUpper(rand.Text())[:6]
	now := time.Now().UTC().Truncate(time.Microsecond)
	var reviewer string
	type seed struct {
		status    string
		requested time.Duration
		finished  time.Duration
	}
	// Expected order: open oldest first, then finished newest first.
	seeds := []seed{
		{"requested", -5 * time.Hour, 0},
		{"under_review", -2 * time.Hour, 0},
		{"requested", -1 * time.Hour, 0},
		{"approved", -6 * time.Hour, -30 * time.Minute},
		{"rejected", -7 * time.Hour, -3 * time.Hour},
		{"withdrawn", -12 * time.Hour, -10 * time.Hour},
	}
	want := make([]string, 0, len(seeds))
	for index, item := range seeds {
		player := h.session(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
			`{"username":"Queue`+suffix+string(rune('a'+index))+`","konamiId":"QUEU-`+suffix+`-00`+string(rune('a'+index))+
				`","password":"queue password"}`, ""), http.StatusCreated)
		if reviewer == "" {
			reviewer = player.Player.ID
		}
		var reviewedAt, reviewedBy any
		reason := ""
		if item.status == "approved" || item.status == "rejected" {
			reviewedAt, reviewedBy = now.Add(item.finished), reviewer
		}
		if item.status == "rejected" {
			reason = "User ID does not match."
		}
		updated := now.Add(item.requested)
		if item.finished != 0 {
			updated = now.Add(item.finished)
		}
		var id string
		if err := h.server.db.Writer.QueryRow(t.Context(), `INSERT INTO game_account_verification_requests
			(game_account_id,user_id,status,decision_reason,reviewed_by,reviewed_at,requested_at,updated_at)
			SELECT id,user_id,$2,$3,$4,$5,$6,$7 FROM game_accounts WHERE user_id=$1 RETURNING id::text`,
			player.Player.ID, item.status, reason, reviewedBy, reviewedAt, now.Add(item.requested), updated).Scan(&id); err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}

	var got []string
	cursor := ""
	for page := 0; page < 10; page++ {
		query := url.Values{"status": {"all"}, "limit": {"2"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		request := httptest.NewRequest(http.MethodGet, "/v1/admin/game-account-verifications?"+query.Encode(), nil)
		request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: reviewer}))
		recorder := httptest.NewRecorder()
		h.server.listGameAccountVerificationQueue(recorder, request)
		h.expect(recorder, http.StatusOK, "")
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Page staffQueuePage `json:"page"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, item := range body.Data {
			got = append(got, item.ID)
		}
		if !body.Page.HasMore {
			break
		}
		cursor = *body.Page.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("every-status order = %v, want %v", got, want)
	}

	// A cursor from one view never pages another.
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/game-account-verifications?status=requested&cursor="+
		url.QueryEscape(cursor), nil)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: reviewer}))
	recorder := httptest.NewRecorder()
	h.server.listGameAccountVerificationQueue(recorder, request)
	if cursor != "" {
		h.expect(recorder, http.StatusBadRequest, "invalid_cursor")
	}
}
