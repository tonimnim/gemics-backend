package evidence

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

var decisionClock = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testWorker uses a deterministic jitter that always returns its upper bound.
func testWorker(store Store, logger *slog.Logger) *Worker {
	w := newWorker(nil, store, logger, DefaultOptions())
	w.jitter = func(n int64) int64 { return n - 1 }
	return w
}

func queuedJob(attempt int) Job {
	return Job{ID: "10000000-0000-4000-8000-000000000001", Attempts: attempt, QueuedAt: decisionClock, UploadExpiresAt: decisionClock}
}

func TestDecideTimeBasedRetry(t *testing.T) {
	w := testWorker(&fakeStore{}, discardLogger())
	unavailable := &storage.StatusError{Op: "head", StatusCode: 503}
	tests := []struct {
		name    string
		err     error
		attempt int
		elapsed time.Duration
		want    outcome
	}{
		{"success", nil, 1, time.Second, outcome{status: statusSucceeded}},
		{"rejection", invalidImage{Code: codeInvalidImage, Reason: "jpeg_structure"}, 1, time.Second, outcome{status: statusRejected, code: codeInvalidImage, class: "jpeg_structure"}},
		{"first retry", unavailable, 1, 10 * time.Second, outcome{status: statusQueued, class: classStorageUnavailable, delay: 2 * time.Second}},
		{"third retry", unavailable, 3, time.Minute, outcome{status: statusQueued, class: classStorageUnavailable, delay: 8 * time.Second}},
		{"backoff capped", unavailable, 12, 2 * time.Minute, outcome{status: statusQueued, class: classStorageUnavailable, delay: 30 * time.Second}},
		{"clamped to window", unavailable, 12, 4*time.Minute + 55*time.Second, outcome{status: statusQueued, class: classStorageUnavailable, delay: 5 * time.Second}},
		{"window closed", unavailable, 12, 5 * time.Minute, outcome{status: statusFailed, code: codeVerificationUnavailable, class: classStorageUnavailable}},
		{"attempt safety net", unavailable, 100, time.Minute, outcome{status: statusFailed, code: codeVerificationUnavailable, class: classStorageUnavailable}},
		{"transport timeout", &storage.TransportError{Op: "get", Timeout: true}, 1, time.Second, outcome{status: statusQueued, class: classStorageTimeout, delay: 2 * time.Second}},
		{"transport failure", &storage.TransportError{Op: "get"}, 1, time.Second, outcome{status: statusQueued, class: classStorageUnreachable, delay: 2 * time.Second}},
		{"job timeout", context.DeadlineExceeded, 1, time.Second, outcome{status: statusQueued, class: classJobTimeout, delay: 2 * time.Second}},
		{"decode capacity", errDecodeCapacity, 1, time.Second, outcome{status: statusQueued, class: classDecodeCapacity, delay: 2 * time.Second}},
		{"unknown error", errors.New("boom"), 1, time.Second, outcome{status: statusQueued, class: classInternalError, delay: 2 * time.Second}},
		{"lease recoveries", errLeaseRecoveryExhausted, 4, time.Second, outcome{status: statusFailed, code: codeProcessingAborted, class: classLeaseRecoveryExhausted}},
		{"attempts exhausted", errAttemptsExhausted, 101, time.Second, outcome{status: statusFailed, code: codeVerificationUnavailable, class: classAttemptsExhausted}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := w.decide(test.err, queuedJob(test.attempt), decisionClock.Add(test.elapsed))
			if got != test.want {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestNotFoundBeforeAndAfterUploadGrace(t *testing.T) {
	w := testWorker(&fakeStore{}, discardLogger())
	job := queuedJob(2)
	job.UploadExpiresAt = decisionClock.Add(10 * time.Minute)
	missingAt := job.UploadExpiresAt.Add(DefaultOptions().UploadGrace)
	tests := []struct {
		name string
		now  time.Time
		want outcome
	}{
		{"upload in flight", decisionClock.Add(time.Minute), outcome{status: statusQueued, class: classUploadPending, delay: 4 * time.Second}},
		{"past retry window, before grace", decisionClock.Add(8 * time.Minute), outcome{status: statusQueued, class: classUploadPending, delay: 4 * time.Second}},
		{"clamped to grace", missingAt.Add(-time.Second), outcome{status: statusQueued, class: classUploadPending, delay: time.Second}},
		{"grace elapsed", missingAt, outcome{status: statusFailed, code: codeUploadMissing, class: classUploadMissing}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := w.decide(storage.ErrNotFound, job, test.now); got != test.want {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
		})
	}
	expired := queuedJob(1)
	expired.UploadExpiresAt = decisionClock.Add(-time.Hour)
	if got := w.decide(storage.ErrNotFound, expired, decisionClock); got.status != statusFailed || got.code != codeUploadMissing {
		t.Fatalf("object missing after its upload URL expired was retried: %+v", got)
	}
}

func TestSystemicStorageErrorsRetryUntilWindow(t *testing.T) {
	w := testWorker(&fakeStore{}, discardLogger())
	tests := []struct {
		err   error
		class string
	}{
		{&storage.StatusError{Op: "head", StatusCode: 401}, classStorageAccessDenied},
		{&storage.StatusError{Op: "head", StatusCode: 403}, classStorageAccessDenied},
		{&storage.StatusError{Op: "get", StatusCode: 400}, classStorageBadRequest},
		{&storage.StatusError{Op: "get", StatusCode: 302}, classStorageRedirect},
		{storage.ErrChecksumAbsent, classStorageChecksumUnavailable},
	}
	for _, test := range tests {
		t.Run(test.class, func(t *testing.T) {
			if !systemicClasses[test.class] {
				t.Fatalf("%s is not treated as systemic", test.class)
			}
			during := w.decide(test.err, queuedJob(5), decisionClock.Add(4*time.Minute))
			after := w.decide(test.err, queuedJob(5), decisionClock.Add(5*time.Minute))
			if during.status != statusQueued || during.class != test.class || after.status != statusFailed || after.class != test.class {
				t.Fatalf("systemic fault handled as %+v then %+v", during, after)
			}
		})
	}
}

func TestClassifyStorageStatus(t *testing.T) {
	for status, want := range map[int]string{
		301: classStorageRedirect, 400: classStorageBadRequest, 401: classStorageAccessDenied, 403: classStorageAccessDenied,
		408: classStorageUnavailable, 409: classStorageHTTPError, 418: classStorageHTTPError, 429: classStorageUnavailable,
		500: classStorageUnavailable, 503: classStorageUnavailable,
	} {
		if got := classify(fmt.Errorf("wrapped: %w", &storage.StatusError{Op: "head", StatusCode: status})); got != want {
			t.Fatalf("HTTP %d classified as %s, want %s", status, got, want)
		}
	}
	if got := classify(fmt.Errorf("wrapped: %w", storage.ErrNotFound)); got != classStorageNotFound {
		t.Fatalf("not found classified as %s", got)
	}
}

func TestBackoffCappedAndClamped(t *testing.T) {
	options := DefaultOptions()
	for _, jitter := range []func(int64) int64{func(int64) int64 { return 0 }, func(n int64) int64 { return n - 1 }} {
		w := newWorker(nil, &fakeStore{}, discardLogger(), options)
		w.jitter = jitter
		previous := time.Duration(0)
		for attempt := 1; attempt <= 40; attempt++ {
			ceiling := min(options.MaxBackoff, 2*time.Second<<min(attempt-1, 10))
			delay := w.retryDelay(attempt, time.Hour)
			if delay < ceiling/2 || delay > ceiling || delay < time.Second || delay > options.MaxBackoff || delay < previous {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", attempt, delay, ceiling/2, ceiling)
			}
			previous = delay
		}
		if delay := w.retryDelay(40, 1500*time.Millisecond); delay != 1500*time.Millisecond {
			t.Fatalf("delay not clamped to the deadline: %s", delay)
		}
	}
}

func TestQuarantineSkipsStorage(t *testing.T) {
	store := &fakeStore{}
	w := testWorker(store, discardLogger())
	job := queuedJob(4)
	job.LeaseRecoveries = DefaultOptions().MaxLeaseRecoveries + 1
	job.DBNow, job.claimed = decisionClock, time.Now()
	result, etag := w.evaluate(context.Background(), job)
	if result != (outcome{status: statusFailed, code: codeProcessingAborted, class: classLeaseRecoveryExhausted}) || etag != "" {
		t.Fatalf("quarantined job was not aborted: %+v", result)
	}
	job.LeaseRecoveries, job.Attempts = 0, DefaultOptions().MaxAttempts+1
	if result, _ = w.evaluate(context.Background(), job); result.class != classAttemptsExhausted || result.status != statusFailed {
		t.Fatalf("attempt safety net not applied: %+v", result)
	}
	if store.stats != 0 || store.reads != 0 {
		t.Fatalf("storage touched %d/%d times", store.stats, store.reads)
	}
}

func TestLogsNeverContainRawErrors(t *testing.T) {
	const secrets = "X-Amz-Signature=0123456789abcdef https://objects.internal/gamics-evidence/evidence/2026/secret-object.png connection refused"
	body := fixture(t, "png")
	job, store := storedObject(t, body, "image/png")
	job.ID, job.ObjectKey, job.Attempts = "10000000-0000-4000-8000-000000000001", "evidence/2026/secret-object.png", 1
	job.QueuedAt, job.UploadExpiresAt, job.DBNow, job.claimed = decisionClock, decisionClock, decisionClock, time.Now()
	store.err = errors.New("GET " + secrets)
	var logs bytes.Buffer
	w := testWorker(store, slog.New(slog.NewJSONHandler(&logs, nil)))
	result, _ := w.evaluate(context.Background(), job)
	w.record(context.Background(), job, result, 12*time.Millisecond)
	w.logDatabaseFailure(context.Background(), "evidence completion persistence failed",
		&pgconn.PgError{Code: "23514", Message: "value " + secrets + " violates check"}, "evidence_id", job.ID)
	output := logs.String()
	for _, leaked := range []string{"X-Amz-Signature", "secret-object", "objects.internal", "connection refused", "violates", base64.StdEncoding.EncodeToString(job.Checksum)} {
		if strings.Contains(output, leaked) {
			t.Fatalf("log leaked %q: %s", leaked, output)
		}
	}
	for _, expected := range []string{`"class":"internal_error"`, `"status":"queued"`, `"lease_recoveries":0`, `"delay_ms":2000`, `"sqlstate":"23514"`, `"class":"db_unavailable"`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log is missing %s: %s", expected, output)
		}
	}
}

func TestOptionsBounds(t *testing.T) {
	defaults := DefaultOptions()
	if err := defaults.validate(); err != nil {
		t.Fatal(err)
	}
	if got := defaults.MemoryRequirement(); got != 306<<20 {
		t.Fatalf("default memory requirement is %d bytes, want 306 MiB", got)
	}
	invalid := map[string]func(*Options){
		"concurrency":         func(o *Options) { o.Concurrency = 33 },
		"lease":               func(o *Options) { o.Lease = o.JobTimeout },
		"max attempts":        func(o *Options) { o.MaxAttempts = 501 },
		"short window":        func(o *Options) { o.RetryWindow = 59 * time.Second },
		"long window":         func(o *Options) { o.RetryWindow = 25 * time.Hour },
		"short backoff":       func(o *Options) { o.MaxBackoff = 999 * time.Millisecond },
		"long backoff":        func(o *Options) { o.MaxBackoff = 6 * time.Minute },
		"backoff past window": func(o *Options) { o.RetryWindow, o.MaxBackoff = time.Minute, time.Minute },
		"negative grace":      func(o *Options) { o.UploadGrace = -time.Second },
		"long grace":          func(o *Options) { o.UploadGrace = 11 * time.Minute },
		"single recovery":     func(o *Options) { o.MaxLeaseRecoveries = 1 },
		"many recoveries":     func(o *Options) { o.MaxLeaseRecoveries = 11 },
		"small budget":        func(o *Options) { o.DecodeBudgetBytes = MinDecodeBudgetBytes - 1 },
		"large budget":        func(o *Options) { o.DecodeBudgetBytes = MaxDecodeBudgetBytes + 1 },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			options := defaults
			mutate(&options)
			if err := options.validate(); err == nil {
				t.Fatalf("accepted %+v", options)
			}
		})
	}
	if _, err := NewWorker(nil, &fakeStore{}, discardLogger(), defaults); err == nil {
		t.Fatal("worker built without a database pool")
	}
}

func TestBoundedLabels(t *testing.T) {
	if boundedLabelPattern.String() != `^[a-z][a-z0-9_]{0,63}$` {
		t.Fatal("label pattern drifted from the database CHECK")
	}
	for _, class := range append(append([]string(nil), errorClasses...), classStorageNotFound, codeUploadMissing, codeVerificationUnavailable, codeProcessingAborted) {
		if boundedLabel(class, "fallback") != class {
			t.Fatalf("%s violates the database CHECK", class)
		}
	}
	for _, raw := range []string{"storage HEAD returned 403", "Upper", "9lead", strings.Repeat("a", 65)} {
		if got := boundedLabel(raw, classInternalError); got != classInternalError {
			t.Fatalf("%q was written as %q", raw, got)
		}
	}
}

func TestMetricsExposeBoundedErrorClasses(t *testing.T) {
	w := testWorker(&fakeStore{}, discardLogger())
	w.countError(classStorageTimeout)
	w.countError("unexpected class")
	body := w.metrics(3, 1.5, 2)
	for _, expected := range []string{
		`gamics_evidence_errors_total{class="storage_timeout"} 1`,
		`gamics_evidence_errors_total{class="internal_error"} 1`,
		"gamics_evidence_decode_budget_bytes 201326592",
		"gamics_evidence_decode_budget_in_use_bytes 0",
		"gamics_evidence_decode_budget_waits_total 0",
		"gamics_evidence_queue_depth 3",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "unexpected class") || strings.Count(body, "gamics_evidence_errors_total{") != len(errorClasses) {
		t.Fatalf("error label set is not fixed:\n%s", body)
	}
}
