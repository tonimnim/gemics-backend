package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestResultVerificationSelectorsAreBoundedAndLockFree(t *testing.T) {
	source := resultReportsFunctionSource(t, "match_result_verification_worker.go",
		"func (s *Server) selectResultVerificationCandidates(")
	if strings.Contains(source, "FOR UPDATE") || strings.Contains(source, "SKIP LOCKED") {
		t.Fatal("candidate discovery locks rows before the competition gate")
	}
	for fragment, want := range map[string]int{
		"LIMIT $1":                                  4,
		"ANY($2::text[]::uuid[])":                   4,
		"JOIN competitions c ON c.id=":              4,
		"c.status NOT IN ('cancelled','completed')": 4,
	} {
		if got := strings.Count(source, fragment); got != want {
			t.Errorf("selectors contain %q %d times, want %d", fragment, got, want)
		}
	}
	for _, fragment := range []string{
		"v.reminder_sent_at IS NULL\n\t\t  AND v.reminder_at<=now() AND v.report_deadline_at>now()",
		"WHERE v.phase='awaiting_confirmation' AND v.report_deadline_at<=now()",
		"WHERE v.phase='awaiting_screenshots' AND v.response_deadline_at<=now()",
		"WHERE m.state='in_progress' AND m.result_due_at IS NOT NULL AND m.result_due_at<=now()",
		"ORDER BY m.result_due_at,m.id LIMIT $1",
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("selectors do not contain %q", fragment)
		}
	}
}

func TestResultVerificationCandidateIsGateFirstAndInvalidatesAfterCommit(t *testing.T) {
	source := resultReportsFunctionSource(t, "match_result_verification_worker.go",
		"func (s *Server) processResultVerificationCandidate(")
	assertOrder(t, "processResultVerificationCandidate", source,
		"lockCompetitionProgressionGate(ctx, tx, candidate.CompetitionID)", "lockResultMatch(", "lockResultVerification(",
		"lockResultReports(", "loadBlockedEvidence(", "planVerificationDeadline(state)", "applyVerificationDeadline(",
		"tx.Commit(ctx)", "s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), candidate.CompetitionID)")
	apply := resultReportsFunctionSource(t, "match_result_verification_worker.go", "func applyVerificationDeadline(")
	for _, fragment := range []string{`Kind: "worker"`, `RequestID: "match-result-verification-worker"`,
		`"evidence_unavailable"`, "finalizeMatchResolution(ctx, tx, state.Match, state.Verification, plan.Resolution, actor, true)"} {
		if !strings.Contains(apply, fragment) {
			t.Errorf("applyVerificationDeadline does not contain %q", fragment)
		}
	}
	assertFileContains(t, "server.go",
		"go s.runMatchNoShowWorker(workerCtx)",
		"go s.runMatchResultVerificationWorker(workerCtx)",
		"s.registerMatchResultRoutes(mux)")
	assertFileOmits(t, "server.go", "runRefereeDecisionFinalizer")
}

func TestResultVerificationBackoffIsBoundedAndExponential(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var backoff matchResultVerificationBackoff
	if excluded := backoff.excluded(now); excluded == nil || len(excluded) != 0 {
		t.Fatalf("an empty backoff must exclude nothing with a non-nil list: %#v", excluded)
	}
	poison := "70000000-0000-4000-8000-000000000001"
	wantDelays := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		15 * time.Minute, 15 * time.Minute}
	for _, want := range wantDelays {
		backoff.fail(poison, now)
		if got := backoff.delay[poison]; got != want {
			t.Fatalf("delay = %s, want %s", got, want)
		}
	}
	if excluded := backoff.excluded(now.Add(15*time.Minute - time.Second)); len(excluded) != 1 || excluded[0] != poison {
		t.Fatalf("a backing-off candidate must be excluded: %v", excluded)
	}
	if excluded := backoff.excluded(now.Add(15 * time.Minute)); len(excluded) != 0 {
		t.Fatalf("an expired backoff must be retried: %v", excluded)
	}
	backoff.clear(poison)
	if _, tracked := backoff.delay[poison]; tracked {
		t.Fatal("a success must reset the candidate's backoff")
	}

	for index := range matchResultVerificationBackoffLimit + 10 {
		backoff.fail(fmt.Sprintf("70000000-0000-4000-8000-%012d", index), now.Add(time.Duration(index)*time.Second))
	}
	if len(backoff.delay) != matchResultVerificationBackoffLimit || len(backoff.retryAt) != matchResultVerificationBackoffLimit {
		t.Fatalf("backoff grew to %d/%d entries", len(backoff.delay), len(backoff.retryAt))
	}
	if _, kept := backoff.delay[fmt.Sprintf("70000000-0000-4000-8000-%012d", 0)]; kept {
		t.Fatal("at capacity the candidate due soonest must be forgotten")
	}
}

func TestResultVerificationErrorClasses(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("apply: %w", errMatchProgressionConflict):   "progression_conflict",
		fmt.Errorf("remove: %w", errEntryRemovalInvariant):     "removal_invariant",
		errMatchResolutionChanged:                              "plan_invariant",
		fmt.Errorf("plan: %w", errResultVerificationInvariant): "plan_invariant",
		errCompetitionClosed:                                   "plan_invariant",
		errors.New("connection reset by peer"):                 "database",
	} {
		if got := resultVerificationErrorClass(err); got != want {
			t.Errorf("class(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestResultVerificationWorkerReturnsWithoutDatabase(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&Server{}).runMatchResultVerificationWorker(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker without a database did not return")
	}
}

// An action the applier does not know is an invariant, never a finalization.
// The unknown action is refused before the transaction is touched.
func TestVerificationDeadlineRejectsUnknownPlanAction(t *testing.T) {
	for _, action := range []planAction{planNone, planOpen, planMismatch, planWait, planReview, "forfeit"} {
		err := applyVerificationDeadline(context.Background(), nil, verificationState{}, deadlinePlan{Action: action})
		if !errors.Is(err, errResultVerificationInvariant) || !strings.Contains(err.Error(), string(action)) {
			t.Errorf("action %q: %v", action, err)
		}
	}
}
