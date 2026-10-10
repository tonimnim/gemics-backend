package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. Each one migrates its own
// random schema and drives the handlers and the worker directly.

type resultReportsSides struct {
	MatchID, CompetitionID, HomeEntry, AwayEntry, HomeUser, AwayUser string
}

// resultReportsEvidenceStore signs downloads without network I/O.
type resultReportsEvidenceStore struct{}

func (resultReportsEvidenceStore) PresignPut(context.Context, string, string, string, int64, time.Duration) (storage.UploadIntent, error) {
	return storage.UploadIntent{}, errors.New("result report tests never upload")
}

func (resultReportsEvidenceStore) PresignGet(_ context.Context, objectKey string, expires time.Duration) (storage.DownloadIntent, error) {
	return storage.DownloadIntent{URL: "https://evidence.test/" + objectKey, ExpiresAt: time.Now().Add(expires)}, nil
}

func (resultReportsEvidenceStore) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, errors.New("result report tests never stat objects")
}

func resultReportsServer(pool *pgxpool.Pool) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{AccessTokenSecret: "result-reports-secret", RequestTimeout: 30 * time.Second,
		StoragePresignTTL: time.Minute}, logger, "test",
		Dependencies{Database: &database.Cluster{Writer: pool, Reader: pool}, EvidenceStore: resultReportsEvidenceStore{}})
}

// resultReportsSetup seeds a four-entry single-elimination competition and
// starts its first semifinal.
func resultReportsSetup(t *testing.T) (*Server, *pgxpool.Pool, integrationCompetition, resultReportsSides) {
	t.Helper()
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	return resultReportsServer(pool), pool, seeded, resultReportsStart(t, pool, readyIntegrationMatches(t, pool, seeded.ID)[0])
}

func resultReportsStart(t *testing.T, pool *pgxpool.Pool, matchID string) resultReportsSides {
	t.Helper()
	checkInBoth(t, pool, matchID)
	sides := resultReportsSides{MatchID: matchID}
	if err := pool.QueryRow(t.Context(), `SELECT match.competition_id::text,match.home_entry_id::text,
		match.away_entry_id::text,home.captain_user_id::text,away.captain_user_id::text
		FROM matches match
		JOIN competition_entries home ON home.id=match.home_entry_id
		JOIN competition_entries away ON away.id=match.away_entry_id
		WHERE match.id=$1`, matchID).Scan(&sides.CompetitionID, &sides.HomeEntry, &sides.AwayEntry,
		&sides.HomeUser, &sides.AwayUser); err != nil {
		t.Fatal(err)
	}
	return sides
}

func resultReportsBody(home, away int) string {
	return fmt.Sprintf(`{"homeScore":%d,"awayScore":%d,"declarationAccepted":true}`, home, away)
}

// resultReportsAnswer is the other entry's answer: confirm or reject.
func resultReportsAnswer(decision string) string {
	return `{"decision":"` + decision + `"}`
}

// resultReportsShot is an entry's one screenshot after a rejection.
func resultReportsShot(evidenceID string) string {
	return `{"evidenceId":"` + evidenceID + `"}`
}

// resultReportsPost posts a result action: "" submits the result,
// "confirmation" answers it and "screenshot" sends a screenshot.
func resultReportsPost(t *testing.T, server *Server, userID, matchID, key, body, action string) *httptest.ResponseRecorder {
	path, handler := "/v1/matches/"+matchID+"/score-reports", server.createScoreReport
	switch action {
	case "confirmation":
		path, handler = path+"/confirmation", server.createResultConfirmation
	case "screenshot":
		path, handler = path+"/screenshot", server.createResultScreenshot
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.SetPathValue("matchId", matchID)
	request.Header.Set("Idempotency-Key", key)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func resultReportsMustPost(t *testing.T, server *Server, userID, matchID, key, body, action string) matchRoomResponse {
	t.Helper()
	recorder := resultReportsPost(t, server, userID, matchID, key, body, action)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("result action %s: %d %s", key, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Match matchRoomResponse `json:"match"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.Match
}

// resultReportsReject has the home entry submit 2-1 and the away entry reject
// it, which opens the screenshot window.
func resultReportsReject(t *testing.T, server *Server, sides resultReportsSides) {
	t.Helper()
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-result-1", resultReportsBody(2, 1), "")
	resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "away-reject-1", resultReportsAnswer("reject"), "confirmation")
}

func resultReportsRoom(t *testing.T, server *Server, userID, matchID string) matchRoomResponse {
	t.Helper()
	recorder := resultReportsGet(t, server.getMatch, userID, "matchId", matchID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("room: %d %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data matchRoomResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func resultReportsGet(t *testing.T, handler http.HandlerFunc, userID, pathValue, value string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetPathValue(pathValue, value)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func resultReportsCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func resultReportsProcess(t *testing.T, server *Server, sides resultReportsSides) {
	t.Helper()
	candidate := matchResultVerificationCandidate{MatchID: sides.MatchID, CompetitionID: sides.CompetitionID}
	if err := server.processResultVerificationCandidate(t.Context(), candidate); err != nil {
		t.Fatalf("process %s: %v", sides.MatchID, err)
	}
}

func resultReportsInsertEvidence(t *testing.T, pool *pgxpool.Pool, ownerID, status string, createdAt time.Time) string {
	t.Helper()
	var evidenceID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO evidence_uploads
		(owner_user_id,object_key,media_type,byte_size,checksum_sha256,status,upload_expires_at,
		 completed_at,processed_at,created_at)
		VALUES ($1,'it/'||gen_random_uuid()::text||'.png','image/png',2048,decode(repeat('00',32),'hex'),$2::text,
		 $3::timestamptz+interval '10 minutes',$3::timestamptz,
		 CASE WHEN $2::text='completed' THEN $3::timestamptz END,$3::timestamptz)
		RETURNING id::text`, ownerID, status, createdAt).Scan(&evidenceID); err != nil {
		t.Fatal(err)
	}
	return evidenceID
}

func resultReportsInsertUser(t *testing.T, pool *pgxpool.Pool, label string) string {
	t.Helper()
	var userID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,display_name,status)
		VALUES ($1||'-'||gen_random_uuid()::text||'@gamics.test',$1,'active') RETURNING id::text`, label).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return userID
}

// resultReportsAssertNoScoreLeaks requires every push and every audit row
// written before the result is final to be free of scores.
func resultReportsAssertNoScoreLeaks(t *testing.T, pool *pgxpool.Pool, competitionID string) {
	t.Helper()
	if leaks := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events
		WHERE (event_type LIKE 'result.%' OR event_type IN ('match.result_confirmed','competition.entry_removed'))
		  AND payload::text ~ 'Score'`); leaks != 0 {
		t.Fatalf("%d verification pushes carry a score", leaks)
	}
	if leaks := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events
		WHERE action IN ('result.reported','result.screenshot_submitted','result.rejected','result.report_reminder_sent')
		  AND after_state::text ~ 'Score'`); leaks != 0 {
		t.Fatalf("%d pre-resolution audit rows of %s carry a score", leaks, competitionID)
	}
}

func resultReportsAssertRatingsUntouched(t *testing.T, pool *pgxpool.Pool, sides resultReportsSides) {
	t.Helper()
	if rated := resultReportsCount(t, pool, `SELECT count(*) FROM player_game_ratings WHERE user_id IN ($1,$2)`,
		sides.HomeUser, sides.AwayUser); rated != 0 {
		t.Fatalf("a forfeit or removal changed %d ratings", rated)
	}
}

func TestIntegrationSubmittedResultIsConfirmed(t *testing.T) {
	server, pool, seeded, sides := resultReportsSetup(t)
	room := resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-result-1", resultReportsBody(2, 1), "")
	if room.State != "awaiting_confirmation" || room.Lifecycle != "awaiting_opponent_confirmation" ||
		room.ResultVerification.Phase != "awaiting_confirmation" || room.ResultVerification.ConfirmationDeadline == nil ||
		room.ResultVerification.SubmittedResult == nil || !room.ResultVerification.SubmittedResult.SubmittedByMe {
		t.Fatalf("unexpected room after the submission: %+v", room)
	}
	if windows := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications WHERE match_id=$1
		AND report_deadline_at=first_reported_at+interval '600 seconds' AND reminder_at=report_deadline_at-interval '180 seconds'`,
		sides.MatchID); windows != 1 {
		t.Fatal("the confirmation window was not snapshotted from the database clock")
	}
	if notified := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events
		WHERE event_type='result.report_received' AND aggregate_id=$1 AND payload->>'entryId'=$2`,
		sides.MatchID, sides.AwayEntry); notified != 1 {
		t.Fatal("the other entry was not asked to confirm")
	}
	opponent := resultReportsRoom(t, server, sides.AwayUser, sides.MatchID)
	submitted := opponent.ResultVerification.SubmittedResult
	if opponent.Lifecycle != "confirmation_required" ||
		!slices.Equal(opponent.AllowedActions, []string{"confirm_result", "reject_result"}) ||
		submitted == nil || submitted.SubmittedByMe || submitted.Side != "home" || submitted.HomeScore != 2 || submitted.AwayScore != 1 {
		t.Fatalf("the opponent can't see what to confirm: %+v", opponent)
	}
	if own := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "home-confirm-1", resultReportsAnswer("confirm"),
		"confirmation"); own.Code != http.StatusConflict || !strings.Contains(own.Body.String(), "own_result") {
		t.Fatalf("the submitter confirmed their own result: %d %s", own.Code, own.Body.String())
	}
	if again := resultReportsPost(t, server, sides.AwayUser, sides.MatchID, "away-result-1", resultReportsBody(2, 1), ""); again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "result_awaiting_confirmation") {
		t.Fatalf("the opponent submitted over a pending result: %d %s", again.Code, again.Body.String())
	}

	room = resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "away-confirm-1", resultReportsAnswer("confirm"), "confirmation")
	if room.State != "completed" || room.Result == nil || room.Result.HomeScore != 2 || room.Result.Origin != "agreed_reports" ||
		room.ResultVerification.Resolution == nil || *room.ResultVerification.Resolution != "agreed" {
		t.Fatalf("the confirmation did not settle the result: %+v", room)
	}
	if canonical := resultReportsCount(t, pool, `SELECT count(*) FROM result_submissions
		WHERE match_id=$1 AND status='confirmed' AND origin='agreed_reports' AND submitted_by=$2 AND decided_by=$3
		  AND home_score=2 AND away_score=1`, sides.MatchID, sides.HomeUser, sides.AwayUser); canonical != 1 {
		t.Fatal("the canonical row is missing or has the wrong authorship")
	}
	if resolved := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications
		WHERE match_id=$1 AND phase='resolved' AND resolution='agreed' AND canonical_submission_id IS NOT NULL`, sides.MatchID); resolved != 1 {
		t.Fatal("the verification row was not resolved")
	}
	var homeRating, awayRating int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT rating FROM player_game_ratings WHERE user_id=$1),(SELECT rating FROM player_game_ratings WHERE user_id=$2)`,
		sides.HomeUser, sides.AwayUser).Scan(&homeRating, &awayRating); err != nil {
		t.Fatal(err)
	}
	if homeRating <= 1500 || awayRating >= 1500 || homeRating+awayRating != 3000 {
		t.Fatalf("Elo was not applied: home=%d away=%d", homeRating, awayRating)
	}
	if applied := resultReportsCount(t, pool, `SELECT count(*) FROM match_progression_applications
		WHERE source_match_id=$1 AND cause='player_confirmation'`, sides.MatchID); applied != 1 {
		t.Fatal("progression was not applied once")
	}
	if advanced := resultReportsCount(t, pool, `SELECT count(*) FROM match_slots
		WHERE source_kind='winner_of' AND source_match_id=$1 AND resolved_entry_id=$2`, sides.MatchID, sides.HomeEntry); advanced != 1 {
		t.Fatal("the winner did not advance")
	}
	for action, want := range map[string]int{"result.reported": 1, "result.confirmed": 1} {
		if got := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE action=$1 AND subject_id=$2`,
			action, sides.MatchID); got != want {
			t.Fatalf("%d %s audit rows, want %d", got, action, want)
		}
	}
	resultReportsAssertNoScoreLeaks(t, pool, seeded.ID)
}

func TestIntegrationRejectionOpensTheScreenshotWindow(t *testing.T) {
	server, pool, seeded, sides := resultReportsSetup(t)
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-result-1", resultReportsBody(7, 3), "")
	room := resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "away-reject-1", resultReportsAnswer("reject"), "confirmation")
	if room.State != "disputed" || room.Lifecycle != "screenshot_required" ||
		!slices.Equal(room.AllowedActions, []string{"submit_screenshot"}) || room.ResultVerification.ScreenshotDeadline == nil ||
		room.ResultVerification.Phase != "awaiting_screenshots" {
		t.Fatalf("unexpected room after the rejection: %+v", room)
	}
	if rejected := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications
		WHERE match_id=$1 AND rejected_by=$2 AND mismatch_at IS NOT NULL`, sides.MatchID, sides.AwayUser); rejected != 1 {
		t.Fatal("the rejection is not recorded against the player who rejected")
	}
	replay := resultReportsPost(t, server, sides.AwayUser, sides.MatchID, "away-reject-1", resultReportsAnswer("reject"), "confirmation")
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("the rejection did not replay: %d %s", replay.Code, replay.Body.String())
	}
	if home := resultReportsRoom(t, server, sides.HomeUser, sides.MatchID); home.Lifecycle != "screenshot_required" {
		t.Fatalf("the submitter was not asked for a screenshot: %+v", home)
	}

	evidenceID := resultReportsInsertEvidence(t, pool, sides.HomeUser, "completed", time.Now())
	room = resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-shot-1", resultReportsShot(evidenceID), "screenshot")
	if room.Lifecycle != "awaiting_opponent_screenshot" || room.ResultVerification.MyScreenshot == nil ||
		room.ResultVerification.MyScreenshot.EvidenceID != evidenceID {
		t.Fatalf("unexpected room after the first screenshot: %+v", room)
	}
	if code := resultReportsGet(t, server.getEvidenceAccess, sides.AwayUser, "id", evidenceID).Code; code != http.StatusNotFound {
		t.Fatalf("the other entry reached the screenshot: %d", code)
	}
	if code := resultReportsGet(t, server.getEvidenceAccess, sides.HomeUser, "id", evidenceID).Code; code != http.StatusOK {
		t.Fatalf("the uploader cannot reach the screenshot: %d", code)
	}
	opponentRoom := resultReportsGet(t, server.getMatch, sides.AwayUser, "matchId", sides.MatchID).Body.String()
	if strings.Contains(opponentRoom, evidenceID) || !strings.Contains(opponentRoom, `"opponentScreenshotSubmitted":true`) {
		t.Fatalf("the other entry sees more than that a screenshot was sent: %s", opponentRoom)
	}
	if rejections := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='result.mismatch'
		AND aggregate_id=$1`, sides.MatchID); rejections != 1 {
		t.Fatal("the rejection was not pushed once")
	}
	if screenshots := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reports
		WHERE match_id=$1 AND kind='final' AND home_score IS NULL AND game_results IS NULL`, sides.MatchID); screenshots != 1 {
		t.Fatal("a screenshot submission carries a score")
	}
	resultReportsAssertNoScoreLeaks(t, pool, seeded.ID)
}

func TestIntegrationSecondScreenshotQueuesTheReview(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	resultReportsReject(t, server, sides)
	homeEvidence := resultReportsInsertEvidence(t, pool, sides.HomeUser, "completed", time.Now())
	awayEvidence := resultReportsInsertEvidence(t, pool, sides.AwayUser, "completed", time.Now())
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-shot-1", resultReportsShot(homeEvidence), "screenshot")
	room := resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "away-shot-1", resultReportsShot(awayEvidence), "screenshot")
	if room.State != "disputed" || room.Lifecycle != "under_review" || room.ResultVerification.Phase != "in_review" {
		t.Fatalf("both screenshots did not send the match to review: %+v", room)
	}
	if queued := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reviews
		WHERE match_id=$1 AND status='queued' AND reason='reports_differ'`, sides.MatchID); queued != 1 {
		t.Fatal("the review was not queued")
	}
	if pushed := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='result.under_review'
		AND aggregate_id=$1 AND NOT payload ? 'reason'`, sides.MatchID); pushed != 1 {
		t.Fatal("the review push is missing or carries the reason")
	}
	another := resultReportsInsertEvidence(t, pool, sides.AwayUser, "completed", time.Now())
	reused := resultReportsPost(t, server, sides.AwayUser, sides.MatchID, "away-shot-2", resultReportsShot(another), "screenshot")
	if reused.Code != http.StatusConflict || !strings.Contains(reused.Body.String(), "screenshot_already_submitted") {
		t.Fatalf("a second screenshot was accepted: %d %s", reused.Code, reused.Body.String())
	}
}

func TestIntegrationConcurrentScoreReports(t *testing.T) {
	server, pool, seeded, sides := resultReportsSetup(t)
	var group sync.WaitGroup
	codes := make(chan int, 2)
	for index, userID := range []string{sides.HomeUser, sides.AwayUser} {
		group.Add(1)
		go func() {
			defer group.Done()
			codes <- resultReportsPost(t, server, userID, sides.MatchID, fmt.Sprintf("race-%d-key", index), resultReportsBody(3, 0), "").Code
		}()
	}
	group.Wait()
	close(codes)
	got := make([]int, 0, 2)
	for code := range codes {
		got = append(got, code)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusCreated, http.StatusConflict}) {
		t.Fatalf("two concurrent submissions returned %v, want one result", got)
	}
	if submitted := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reports WHERE match_id=$1`, sides.MatchID); submitted != 1 {
		t.Fatalf("concurrent submissions stored %d results", submitted)
	}

	second := resultReportsStart(t, pool, readyIntegrationMatches(t, pool, seeded.ID)[0])
	statuses := make(chan int, 2)
	for index := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			statuses <- resultReportsPost(t, server, second.HomeUser, second.MatchID, fmt.Sprintf("double-%d-key", index),
				resultReportsBody(1, 0), "").Code
		}()
	}
	group.Wait()
	close(statuses)
	got = got[:0]
	for code := range statuses {
		got = append(got, code)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusCreated, http.StatusConflict}) {
		t.Fatalf("one entry's concurrent submissions returned %v", got)
	}
	if reports := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reports WHERE match_id=$1`, second.MatchID); reports != 1 {
		t.Fatalf("one entry stored %d results", reports)
	}
}

func TestIntegrationScoreReportIdempotentReplay(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	first := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "replay-key-1", resultReportsBody(2, 0), "")
	replay := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "replay-key-1", resultReportsBody(2, 0), "")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated ||
		!sameJSON(t, first.Body.Bytes(), replay.Body.Bytes()) || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay differs: %d %s / %d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	if conflict := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "replay-key-1", resultReportsBody(0, 2), ""); conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("a changed body reused the key: %d %s", conflict.Code, conflict.Body.String())
	}
	if reports := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reports WHERE match_id=$1`, sides.MatchID); reports != 1 {
		t.Fatalf("the replay stored %d reports", reports)
	}
}

func TestIntegrationResultVerificationWorkerRemindsOnce(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-initial-1", resultReportsBody(2, 1), "")
	shiftVerificationClock(t, pool, sides.MatchID, 8*time.Minute)
	var backoff matchResultVerificationBackoff
	server.sweepMatchResultVerifications(t.Context(), &backoff)
	server.sweepMatchResultVerifications(t.Context(), &backoff)
	if reminders := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='result.report_reminder'
		AND aggregate_id=$1 AND payload->>'entryId'=$2`, sides.MatchID, sides.AwayEntry); reminders != 1 {
		t.Fatalf("%d reminders were sent, want exactly one", reminders)
	}
	if sent := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications
		WHERE match_id=$1 AND reminder_sent_at IS NOT NULL AND phase='awaiting_confirmation'`, sides.MatchID); sent != 1 {
		t.Fatal("the reminder changed more than reminder_sent_at")
	}
}

func TestIntegrationUnansweredResultStands(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-result-1", resultReportsBody(2, 1), "")
	shiftVerificationClock(t, pool, sides.MatchID, 10*time.Minute+time.Second)

	// Two replicas processing the same candidate apply it exactly once.
	var group sync.WaitGroup
	failures := make(chan error, 2)
	candidate := matchResultVerificationCandidate{MatchID: sides.MatchID, CompetitionID: sides.CompetitionID}
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			failures <- server.processResultVerificationCandidate(t.Context(), candidate)
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatalf("a concurrent candidate failed: %v", err)
		}
	}

	var state, reason, winner string
	if err := pool.QueryRow(t.Context(), `SELECT state,completion_reason,winner_entry_id::text FROM matches WHERE id=$1`,
		sides.MatchID).Scan(&state, &reason, &winner); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || reason != "played" || winner != sides.HomeEntry {
		t.Fatalf("an unanswered result = %s/%s/%s, want it to stand", state, reason, winner)
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM result_submissions WHERE match_id=$1 AND status='confirmed' AND origin='unanswered'
		  AND decided_by IS NULL AND home_score=2 AND away_score=1`: 1,
		`SELECT count(*) FROM match_result_verifications WHERE match_id=$1 AND resolution='confirmation_timeout'`:      1,
		`SELECT count(*) FROM match_progression_applications WHERE source_match_id=$1 AND cause='player_confirmation'`: 1,
		`SELECT count(*) FROM audit_events WHERE action='result.confirmed' AND subject_id=$1`:                          1,
		`SELECT count(*) FROM outbox_events WHERE event_type='match.result_confirmed' AND aggregate_id=$1`:             1,
		`SELECT count(*) FROM competition_entry_removals WHERE match_id=$1`:                                            0,
	} {
		if got := resultReportsCount(t, pool, query, sides.MatchID); got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	if rated := resultReportsCount(t, pool, `SELECT count(*) FROM player_game_ratings WHERE user_id IN ($1,$2)`,
		sides.HomeUser, sides.AwayUser); rated != 2 {
		t.Fatalf("a standing result rated %d players, want both", rated)
	}
}

func TestIntegrationResponseDeadlineRemovesNonResponders(t *testing.T) {
	for _, test := range []struct {
		name        string
		homeReplies bool
		wantState   string
		wantRemoved int
	}{
		{"one screenshot wins by forfeit", true, "forfeit", 1},
		{"no screenshot removes both", false, "cancelled", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, pool, _, sides := resultReportsSetup(t)
			if _, err := pool.Exec(t.Context(), `UPDATE competition_entries SET status='withdrawal_pending' WHERE id=$1`,
				sides.AwayEntry); err != nil {
				t.Fatal(err)
			}
			resultReportsReject(t, server, sides)
			if test.homeReplies {
				evidenceID := resultReportsInsertEvidence(t, pool, sides.HomeUser, "completed", time.Now())
				resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "home-shot-1", resultReportsShot(evidenceID), "screenshot")
			}
			shiftVerificationClock(t, pool, sides.MatchID, 11*time.Minute)
			resultReportsProcess(t, server, sides)
			if state := resultReportsCount(t, pool, `SELECT count(*) FROM matches WHERE id=$1 AND state=$2
				AND completion_reason='response_timeout'`, sides.MatchID, test.wantState); state != 1 {
				t.Fatalf("the match is not %s by response timeout", test.wantState)
			}
			if removed := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entry_removals
				WHERE match_id=$1 AND reason_code='response_timeout'`, sides.MatchID); removed != test.wantRemoved {
				t.Fatalf("%d entries removed, want %d", removed, test.wantRemoved)
			}
			if removed := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entry_removals removal
				JOIN competition_entries entry ON entry.id=removal.entry_id
				WHERE removal.entry_id=$1 AND removal.previous_status='withdrawal_pending'
				  AND removal.actor_kind='worker' AND entry.status='disqualified'`, sides.AwayEntry); removed != 1 {
				t.Fatal("the silent entry was not removed exactly once")
			}
			// A later refund rejection restores only withdrawal_pending entries.
			if _, err := pool.Exec(t.Context(), `UPDATE competition_entries SET status='registered',updated_at=now()
				WHERE id=$1 AND status='withdrawal_pending'`, sides.AwayEntry); err != nil {
				t.Fatal(err)
			}
			if live := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entries WHERE id=$1 AND status<>'disqualified'`,
				sides.AwayEntry); live != 0 {
				t.Fatal("a refund rejection revived a removed entry")
			}
			if test.homeReplies && resultReportsCount(t, pool, `SELECT count(*) FROM competition_entries
				WHERE id=$1 AND status='accepted'`, sides.HomeEntry) != 1 {
				t.Fatal("the entry that sent its screenshot was removed")
			}
			resultReportsAssertRatingsUntouched(t, pool, sides)
		})
	}
}

func TestIntegrationUnreportedMatchRemovesBothEntries(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	shiftResultDue(t, pool, sides.MatchID, 2*time.Hour)
	var backoff matchResultVerificationBackoff
	server.sweepMatchResultVerifications(t.Context(), &backoff)
	if cancelled := resultReportsCount(t, pool, `SELECT count(*) FROM matches WHERE id=$1 AND state='cancelled'
		AND completion_reason='no_result_reported' AND winner_entry_id IS NULL`, sides.MatchID); cancelled != 1 {
		t.Fatal("R7 did not cancel the match")
	}
	if removed := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entry_removals
		WHERE match_id=$1 AND reason_code='no_result_reported'`, sides.MatchID); removed != 2 {
		t.Fatalf("R7 removed %d entries, want 2", removed)
	}
	if rows := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications WHERE match_id=$1`, sides.MatchID); rows != 0 {
		t.Fatal("R7 created a verification row")
	}
	resultReportsAssertRatingsUntouched(t, pool, sides)
}

func TestIntegrationStuckScreenshotEscalatesInsteadOfRemoving(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	resultReportsReject(t, server, sides)
	shiftVerificationClock(t, pool, sides.MatchID, 11*time.Minute)
	var mismatchAt time.Time
	if err := pool.QueryRow(t.Context(), `SELECT mismatch_at FROM match_result_verifications WHERE match_id=$1`,
		sides.MatchID).Scan(&mismatchAt); err != nil {
		t.Fatal(err)
	}
	evidenceID := resultReportsInsertEvidence(t, pool, sides.AwayUser, "processing", mismatchAt.Add(time.Minute))
	resultReportsProcess(t, server, sides)
	if queued := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reviews review
		JOIN match_result_verifications verification ON verification.match_id=review.match_id
		JOIN matches match ON match.id=review.match_id
		WHERE review.match_id=$1 AND review.reason='evidence_unavailable' AND review.status='queued'
		  AND verification.phase='in_review' AND match.state='disputed'`, sides.MatchID); queued != 1 {
		t.Fatal("a stuck screenshot did not send the match to review")
	}
	if removed := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entry_removals WHERE match_id=$1`, sides.MatchID); removed != 0 {
		t.Fatal("an entry was removed while its screenshot was stuck at Gamics")
	}

	// Processing catches up after the escalation. The reviewer must still find
	// the screenshot the review exists for, and be able to open it; the
	// opponent must not.
	if _, err := pool.Exec(t.Context(), `UPDATE evidence_uploads SET status='completed',processed_at=now(),updated_at=now()
		WHERE id=$1`, evidenceID); err != nil {
		t.Fatal(err)
	}
	reviewer := resultReportsInsertUser(t, pool, "reviewer")
	if _, err := pool.Exec(t.Context(), `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,'reviewer')`, reviewer); err != nil {
		t.Fatal(err)
	}
	var reviewID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM match_result_reviews WHERE match_id=$1`,
		sides.MatchID).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	detail, err := loadResultReviewDetail(t.Context(), pool, reviewID, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ResponseWindowUploads) != 1 || detail.ResponseWindowUploads[0].EvidenceID != evidenceID ||
		detail.ResponseWindowUploads[0].Status != "completed" {
		t.Fatalf("the review lost the screenshot it was opened for: %+v", detail.ResponseWindowUploads)
	}
	if code := resultReportsGet(t, server.getEvidenceAccess, reviewer, "id", evidenceID).Code; code != http.StatusOK {
		t.Fatalf("an unconflicted reviewer cannot open the stuck screenshot: %d", code)
	}
	if code := resultReportsGet(t, server.getEvidenceAccess, sides.HomeUser, "id", evidenceID).Code; code != http.StatusNotFound {
		t.Fatalf("the opponent reached the stuck screenshot: %d", code)
	}
}

func TestIntegrationNonMemberReportDoesNotWaitForTheGate(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	holder, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.WithoutCancel(t.Context())) //nolint:errcheck
	if err = lockCompetitionProgressionGate(t.Context(), holder, sides.CompetitionID); err != nil {
		t.Fatal(err)
	}
	outsider := resultReportsInsertUser(t, pool, "outsider")
	outsiderDone := make(chan int, 1)
	go func() {
		outsiderDone <- resultReportsPost(t, server, outsider, sides.MatchID, "outsider-key-1", resultReportsBody(1, 0), "").Code
	}()
	select {
	case code := <-outsiderDone:
		if code != http.StatusNotFound {
			t.Fatalf("a non-member got %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a non-member request waited on the competition gate")
	}

	memberDone := make(chan int, 1)
	go func() {
		memberDone <- resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "member-key-1", resultReportsBody(1, 0), "").Code
	}()
	select {
	case code := <-memberDone:
		t.Fatalf("a member report finished (%d) while another transaction held the gate", code)
	case <-time.After(300 * time.Millisecond):
	}
	if err = holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code := <-memberDone; code != http.StatusCreated {
		t.Fatalf("the member report failed after the gate was released: %d", code)
	}
}

func TestIntegrationReportsRejectClosedCompetitions(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	if _, err := pool.Exec(t.Context(), `UPDATE competitions SET status='cancelled' WHERE id=$1`, sides.CompetitionID); err != nil {
		t.Fatal(err)
	}
	recorder := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "closed-key-1", resultReportsBody(1, 0), "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "competition_closed") {
		t.Fatalf("a cancelled competition accepted a report: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestIntegrationConflictedReviewerCannotReadMatchEvidence(t *testing.T) {
	server, pool, _, sides := resultReportsSetup(t)
	reviewer := resultReportsInsertUser(t, pool, "reviewer")
	if _, err := pool.Exec(t.Context(), `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,'reviewer'),($2,'reviewer')`,
		sides.HomeUser, reviewer); err != nil {
		t.Fatal(err)
	}
	resultReportsReject(t, server, sides)
	evidenceID := resultReportsInsertEvidence(t, pool, sides.AwayUser, "completed", time.Now())
	resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "away-shot-1", resultReportsShot(evidenceID), "screenshot")
	if code := resultReportsGet(t, server.getEvidenceAccess, sides.HomeUser, "id", evidenceID).Code; code != http.StatusNotFound {
		t.Fatalf("a reviewer who plays the match reached the opponent's screenshot: %d", code)
	}
	if code := resultReportsGet(t, server.getEvidenceAccess, reviewer, "id", evidenceID).Code; code != http.StatusOK {
		t.Fatalf("an unconflicted reviewer cannot reach the screenshot: %d", code)
	}
}
