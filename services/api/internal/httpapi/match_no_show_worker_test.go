package httpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func matchNoShowTestLock() lockedMatchNoShow {
	home, away := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Second)
	return lockedMatchNoShow{
		Version: 7, State: "ready", HomeEntryID: &home, AwayEntryID: &away,
		CheckInClosesAt: &deadline, DatabaseNow: now,
	}
}

func TestExpiredMatchNoShowPlansForfeitForExactlyOneCheckIn(t *testing.T) {
	for name, checked := range map[string]string{
		"home": "00000000-0000-4000-8000-000000000001",
		"away": "00000000-0000-4000-8000-000000000002",
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := planExpiredMatchNoShow(matchNoShowTestLock(), 7, []string{checked})
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Apply || plan.FinalState != "forfeit" || plan.WinnerEntryID == nil ||
				*plan.WinnerEntryID != checked || plan.CompletionReason != "timeout_forfeit" ||
				plan.EventType != "match.forfeited" {
				t.Fatalf("unexpected one-check-in plan: %+v", plan)
			}
		})
	}
}

func TestExpiredMatchNoShowPlansDoubleNoShowCancellation(t *testing.T) {
	plan, err := planExpiredMatchNoShow(matchNoShowTestLock(), 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Apply || plan.FinalState != "cancelled" || plan.WinnerEntryID != nil ||
		plan.CompletionReason != "double_no_show" || plan.EventType != "match.cancelled" {
		t.Fatalf("unexpected zero-check-in plan: %+v", plan)
	}
}

func TestExpiredMatchNoShowNeverDecidesRacedOrInProgressMatch(t *testing.T) {
	locked := matchNoShowTestLock()
	twoChecked := []string{*locked.HomeEntryID, *locked.AwayEntryID}
	plan, err := planExpiredMatchNoShow(locked, locked.Version, twoChecked)
	if err != nil || plan.Apply {
		t.Fatalf("two checked-in participants were decided: plan=%+v err=%v", plan, err)
	}

	locked.State = "in_progress"
	plan, err = planExpiredMatchNoShow(locked, locked.Version, nil)
	if err != nil || plan.Apply {
		t.Fatalf("in-progress match was decided: plan=%+v err=%v", plan, err)
	}
}

func TestExpiredMatchNoShowRevalidatesVersionDeadlineAndParticipants(t *testing.T) {
	locked := matchNoShowTestLock()
	if plan, err := planExpiredMatchNoShow(locked, locked.Version-1, nil); err != nil || plan.Apply {
		t.Fatalf("stale candidate was applied: plan=%+v err=%v", plan, err)
	}

	future := locked.DatabaseNow.Add(time.Second)
	locked.CheckInClosesAt = &future
	if plan, err := planExpiredMatchNoShow(locked, locked.Version, nil); err != nil || plan.Apply {
		t.Fatalf("future deadline was applied: plan=%+v err=%v", plan, err)
	}

	locked = matchNoShowTestLock()
	locked.AwayEntryID = nil
	if _, err := planExpiredMatchNoShow(locked, locked.Version, nil); !errors.Is(err, errMatchNoShowInvalid) {
		t.Fatalf("missing participant error = %v", err)
	}

	locked = matchNoShowTestLock()
	if _, err := planExpiredMatchNoShow(locked, locked.Version,
		[]string{"00000000-0000-4000-8000-000000000099"}); !errors.Is(err, errMatchNoShowInvalid) {
		t.Fatalf("foreign check-in error = %v", err)
	}
}

func TestMatchNoShowWorkerIsBoundedGateFirstAndOptimistic(t *testing.T) {
	raw, err := os.ReadFile("match_no_show_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	selectorStart := strings.Index(source, "func (s *Server) selectExpiredMatchNoShowCandidates")
	finalizerStart := strings.Index(source, "func (s *Server) finalizeOneExpiredMatchNoShow")
	finalizerEnd := strings.Index(source, "func matchNoShowNeedsInspection")
	if selectorStart < 0 || finalizerStart < 0 || finalizerEnd < finalizerStart {
		t.Fatal("worker functions were not found")
	}
	selector := source[selectorStart:finalizerStart]
	for _, fragment := range []string{
		"WHERE state='ready' AND check_in_closes_at IS NOT NULL AND check_in_closes_at<=now()",
		"ORDER BY check_in_closes_at,id",
		"LIMIT $1`, matchNoShowSweepBatch",
	} {
		if !strings.Contains(selector, fragment) {
			t.Errorf("deadline selector does not contain %q", fragment)
		}
	}
	if strings.Contains(selector, "FOR UPDATE") || strings.Contains(selector, "SKIP LOCKED") {
		t.Fatal("candidate discovery locks matches before the competition gate")
	}

	finalizer := source[finalizerStart:finalizerEnd]
	gate := strings.Index(finalizer, "lockCompetitionProgressionGate")
	matchLock := strings.Index(finalizer, "FOR UPDATE OF match")
	update := strings.Index(finalizer, "UPDATE matches SET state=$1")
	progression := strings.Index(finalizer, "applyMatchProgression")
	commit := strings.Index(finalizer, "tx.Commit(ctx)")
	if gate < 0 || matchLock < 0 || update < 0 || progression < 0 || commit < 0 ||
		gate > matchLock || matchLock > update || update > progression || progression > commit {
		t.Fatalf("unsafe finalization order gate=%d match=%d update=%d progression=%d commit=%d",
			gate, matchLock, update, progression, commit)
	}
	for _, fragment := range []string{
		"WHERE id=$5 AND competition_id=$6 AND state='ready' AND version=$7",
		"Cause: progressionCauseTimeoutForfeit",
		"writeMatchNoShowAuditAndOutbox",
	} {
		if !strings.Contains(finalizer, fragment) {
			t.Errorf("deadline finalizer does not contain %q", fragment)
		}
	}
	if !strings.Contains(source, "ORDER BY entry_id FOR UPDATE") {
		t.Fatal("current participant check-ins are not locked in deterministic entry order")
	}
}

func TestMatchNoShowWorkerMigrationIsNarrowAndReversible(t *testing.T) {
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000018_match_no_show_deadlines.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000018_match_no_show_deadlines.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"CREATE INDEX matches_ready_check_in_deadline_idx",
		"ON matches (check_in_closes_at, id)",
		"WHERE state = 'ready' AND check_in_closes_at IS NOT NULL",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Errorf("up migration does not contain %q", fragment)
		}
	}
	if !strings.Contains(string(down), "DROP INDEX IF EXISTS matches_ready_check_in_deadline_idx") {
		t.Fatal("down migration does not drop the deadline index")
	}
}

func TestDeadlineWorkersStartAndInvalidateAfterCommit(t *testing.T) {
	server, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(server), "go s.runMatchNoShowWorker(workerCtx)") {
		t.Fatal("server does not start the match no-show worker")
	}
	if strings.Contains(string(server), "runRefereeDecisionFinalizer") {
		t.Fatal("server still starts the retired referee decision finalizer")
	}

	noShow, err := os.ReadFile("match_no_show_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(noShow), "if applied {\n\t\t\ts.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), candidate.CompetitionID)") {
		t.Fatal("no-show worker does not invalidate competition caches after an applied transaction")
	}
}

func TestMatchNoShowWorkerReturnsWhenUnavailableOrCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		(&Server{}).runMatchNoShowWorker(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker ignored cancellation")
	}
}
