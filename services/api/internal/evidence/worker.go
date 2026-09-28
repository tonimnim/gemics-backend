package evidence

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	randv2 "math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

// Job states, as stored in evidence_media_processing_jobs.status.
const (
	statusSucceeded = "succeeded"
	statusRejected  = "rejected"
	statusQueued    = "queued"
	statusFailed    = "failed"
)

// Terminal codes the worker adds to the rejection codes in image.go.
const (
	codeUploadMissing           = "upload_missing"
	codeVerificationUnavailable = "verification_unavailable"
	codeProcessingAborted       = "processing_aborted"
)

// Error classes are the only failure detail the worker persists, logs or
// exports, so last_error and metric labels stay bounded.
const (
	classUploadPending              = "upload_pending"
	classUploadMissing              = "upload_missing"
	classStorageNotFound            = "storage_not_found"
	classStorageChecksumUnavailable = "storage_checksum_unavailable"
	classStorageAccessDenied        = "storage_access_denied"
	classStorageBadRequest          = "storage_bad_request"
	classStorageRedirect            = "storage_redirect"
	classStorageUnavailable         = "storage_unavailable"
	classStorageHTTPError           = "storage_http_error"
	classStorageTimeout             = "storage_timeout"
	classStorageUnreachable         = "storage_unreachable"
	classJobTimeout                 = "job_timeout"
	classDecodeCapacity             = "decode_capacity"
	classAttemptsExhausted          = "attempts_exhausted"
	classLeaseRecoveryExhausted     = "lease_recovery_exhausted"
	classDatabaseTimeout            = "db_timeout"
	classDatabaseUnavailable        = "db_unavailable"
	classInternalError              = "internal_error"
)

// errorClasses fixes the gamics_evidence_errors_total label set. A not-found
// object is always reported as upload_pending or upload_missing instead.
var errorClasses = []string{
	classUploadPending, classUploadMissing, classStorageChecksumUnavailable, classStorageAccessDenied,
	classStorageBadRequest, classStorageRedirect, classStorageUnavailable, classStorageHTTPError,
	classStorageTimeout, classStorageUnreachable, classJobTimeout, classDecodeCapacity,
	classAttemptsExhausted, classLeaseRecoveryExhausted, classDatabaseTimeout, classDatabaseUnavailable,
	classInternalError,
}

// Systemic classes point at deployment faults rather than one upload. They
// are retried within the window like any transient fault, but logged at WARN.
var systemicClasses = map[string]bool{
	classStorageAccessDenied: true, classStorageBadRequest: true,
	classStorageChecksumUnavailable: true, classStorageRedirect: true,
}

var (
	errAttemptsExhausted      = errors.New("evidence attempts exhausted")
	errLeaseRecoveryExhausted = errors.New("evidence lease recoveries exhausted")
	boundedLabelPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

const (
	// MinDecodeBudgetBytes admits the largest image the structural limits
	// allow together with its encoded body, so no admissible job waits forever.
	MinDecodeBudgetBytes = MaxDecodedBytes + maxEncodedBytes
	MaxDecodeBudgetBytes = 1 << 30
	// baselineMemory is the process heap outside image buffers: connection
	// pools, HTTP clients and runtime overhead.
	baselineMemory int64 = 64 << 20
)

type Options struct {
	Concurrency  int
	PollInterval time.Duration
	JobTimeout   time.Duration
	Lease        time.Duration
	// MaxAttempts is a safety net only; RetryWindow bounds transient retries.
	MaxAttempts        int
	RetryWindow        time.Duration
	MaxBackoff         time.Duration
	UploadGrace        time.Duration
	MaxLeaseRecoveries int
	DecodeBudgetBytes  int64
}

func DefaultOptions() Options {
	return Options{
		Concurrency: 2, PollInterval: time.Second, JobTimeout: 30 * time.Second, Lease: 2 * time.Minute, MaxAttempts: 100,
		RetryWindow: 5 * time.Minute, MaxBackoff: 30 * time.Second, UploadGrace: 2 * time.Minute,
		MaxLeaseRecoveries: 2, DecodeBudgetBytes: 192 << 20,
	}
}

func (o Options) validate() error {
	if o.Concurrency < 1 || o.Concurrency > 32 || o.PollInterval < 100*time.Millisecond || o.JobTimeout <= 0 || o.Lease < 2*o.JobTimeout || o.MaxAttempts < 1 || o.MaxAttempts > 500 {
		return errors.New("invalid evidence worker limits")
	}
	if o.RetryWindow < time.Minute || o.RetryWindow > 24*time.Hour || o.MaxBackoff < time.Second || o.MaxBackoff > 5*time.Minute || o.MaxBackoff >= o.RetryWindow {
		return errors.New("invalid evidence worker retry window or backoff")
	}
	// An OOM kill also expires the lease of the innocent co-located job, so
	// one recovery must never be enough to quarantine a job.
	if o.UploadGrace < 0 || o.UploadGrace > 10*time.Minute || o.MaxLeaseRecoveries < 2 || o.MaxLeaseRecoveries > 10 {
		return errors.New("invalid evidence worker upload grace or lease recovery limit")
	}
	if o.DecodeBudgetBytes < MinDecodeBudgetBytes || o.DecodeBudgetBytes > MaxDecodeBudgetBytes {
		return errors.New("invalid evidence worker decode budget")
	}
	return nil
}

// MemoryRequirement is the peak heap these options can demand: one encoded
// body per concurrent job, the whole decode budget and the process baseline.
func (o Options) MemoryRequirement() int64 {
	return int64(o.Concurrency)*maxEncodedBytes + o.DecodeBudgetBytes + baselineMemory
}

type Worker struct {
	pool        *pgxpool.Pool
	store       Store
	log         *slog.Logger
	options     Options
	budget      *decodeBudget
	jitter      func(int64) int64
	errorCounts map[string]*atomic.Uint64
	succeeded   atomic.Uint64
	rejected    atomic.Uint64
	retried     atomic.Uint64
	failed      atomic.Uint64
	active      atomic.Int64
}

type Job struct {
	ID, ObjectKey, MediaType, Owner string
	ByteSize                        int64
	Checksum                        []byte
	Attempts                        int
	LeaseRecoveries                 int
	UploadExpiresAt                 time.Time
	QueuedAt                        time.Time
	// DBNow is the database clock at claim time and claimed the local
	// monotonic reading taken with it.
	DBNow   time.Time
	claimed time.Time
}

// now is the database clock advanced by local monotonic time since the claim,
// so retry deadlines never depend on the worker host's wall clock.
func (j Job) now() time.Time {
	return j.DBNow.Add(time.Since(j.claimed))
}

// outcome is the next durable state of a job. code is client-visible and only
// written for terminal failures; class is the bounded internal cause.
type outcome struct {
	status string
	code   string
	class  string
	delay  time.Duration
}

func NewWorker(pool *pgxpool.Pool, store Store, logger *slog.Logger, options Options) (*Worker, error) {
	if pool == nil || store == nil || logger == nil {
		return nil, errors.New("worker dependencies are required")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	return newWorker(pool, store, logger, options), nil
}

func newWorker(pool *pgxpool.Pool, store Store, logger *slog.Logger, options Options) *Worker {
	counts := make(map[string]*atomic.Uint64, len(errorClasses))
	for _, class := range errorClasses {
		counts[class] = new(atomic.Uint64)
	}
	return &Worker{
		pool: pool, store: store, log: logger, options: options,
		budget: newDecodeBudget(options.DecodeBudgetBytes), jitter: randv2.Int64N, errorCounts: counts,
	}
}

// Claim is a short, atomic statement. No database transaction/connection is
// retained while downloading or decoding an image. SKIP LOCKED permits multiple
// independent worker replicas; lease owners fence all final writes. Reclaiming
// an expired lease counts as a lease recovery, which quarantines poison jobs.
const claimSQL = `WITH candidate AS (
	SELECT job.evidence_id,job.status AS prior_status FROM evidence_media_processing_jobs job
	JOIN evidence_uploads evidence ON evidence.id=job.evidence_id
	WHERE evidence.status='processing' AND evidence.media_kind='image'
	AND ((job.status='queued' AND job.available_at<=now())
	 OR (job.status='running' AND job.locked_at<now()-make_interval(secs=>$1)))
	ORDER BY job.available_at,job.evidence_id
	LIMIT 1 FOR UPDATE OF job SKIP LOCKED
), claimed AS (
	UPDATE evidence_media_processing_jobs job SET status='running',attempts=attempts+1,
	 lease_recoveries=lease_recoveries+(candidate.prior_status='running')::int,
	 locked_at=now(),locked_by=$2,updated_at=now()
	FROM candidate WHERE job.evidence_id=candidate.evidence_id
	RETURNING job.evidence_id,job.attempts,job.lease_recoveries,job.created_at
)
SELECT evidence.id,evidence.object_key,evidence.media_type,evidence.byte_size,evidence.checksum_sha256,
	evidence.upload_expires_at,claimed.attempts,claimed.lease_recoveries,claimed.created_at,now()
FROM claimed JOIN evidence_uploads evidence ON evidence.id=claimed.evidence_id`

func (w *Worker) claim(ctx context.Context) (Job, error) {
	job := Job{Owner: rand.Text()}
	err := w.pool.QueryRow(ctx, claimSQL, w.options.Lease.Seconds(), job.Owner).Scan(
		&job.ID, &job.ObjectKey, &job.MediaType, &job.ByteSize, &job.Checksum,
		&job.UploadExpiresAt, &job.Attempts, &job.LeaseRecoveries, &job.QueuedAt, &job.DBNow)
	job.claimed = time.Now()
	return job, err
}

func (w *Worker) Run(ctx context.Context) {
	var group sync.WaitGroup
	for range w.options.Concurrency {
		group.Add(1)
		go func() {
			defer group.Done()
			for ctx.Err() == nil {
				claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				job, err := w.claim(claimCtx)
				cancel()
				if err == nil {
					w.process(ctx, job)
					continue
				}
				if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
					w.logDatabaseFailure(ctx, "evidence queue claim failed", err)
				}
				timer := time.NewTimer(w.options.PollInterval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	group.Wait()
}

func (w *Worker) process(ctx context.Context, job Job) {
	w.active.Add(1)
	defer w.active.Add(-1)
	started := time.Now()
	result, etag := w.evaluate(ctx, job)
	if ctx.Err() != nil {
		return
	} // Durable lease will be recovered after shutdown.
	finishCtx, finishCancel := context.WithTimeout(ctx, 5*time.Second)
	applied, err := w.finish(finishCtx, job, result, etag)
	finishCancel()
	if err != nil {
		w.logDatabaseFailure(ctx, "evidence completion persistence failed", err, "evidence_id", job.ID)
		return
	}
	if !applied {
		return
	} // A later lease owns the result.
	w.record(ctx, job, result, time.Since(started))
}

// evaluate verifies the job and decides its next state. A job whose leases
// keep expiring may be what is killing workers, so it is failed without
// touching storage.
func (w *Worker) evaluate(ctx context.Context, job Job) (outcome, string) {
	var etag string
	var err error
	switch {
	case job.LeaseRecoveries > w.options.MaxLeaseRecoveries:
		err = errLeaseRecoveryExhausted
	case job.Attempts > w.options.MaxAttempts:
		err = errAttemptsExhausted
	default:
		workCtx, cancel := context.WithTimeout(ctx, w.options.JobTimeout)
		etag, err = verify(workCtx, w.store, w.budget, job)
		cancel()
	}
	return w.decide(err, job, job.now()), etag
}

// decide maps a verification error to the job's next state. now is the
// database clock; every deadline derives from database timestamps.
func (w *Worker) decide(err error, job Job, now time.Time) outcome {
	if err == nil {
		return outcome{status: statusSucceeded}
	}
	var invalid invalidImage
	if errors.As(err, &invalid) {
		return outcome{status: statusRejected, code: invalid.Code, class: invalid.Reason}
	}
	if errors.Is(err, errLeaseRecoveryExhausted) {
		return outcome{status: statusFailed, code: codeProcessingAborted, class: classLeaseRecoveryExhausted}
	}
	class := classify(err)
	deadline := job.QueuedAt.Add(w.options.RetryWindow)
	if class == classStorageNotFound {
		// The signed PUT may still be in flight until its URL expires.
		missingAt := job.UploadExpiresAt.Add(w.options.UploadGrace)
		if !now.Before(missingAt) {
			return outcome{status: statusFailed, code: codeUploadMissing, class: classUploadMissing}
		}
		class = classUploadPending
		if missingAt.After(deadline) {
			deadline = missingAt
		}
	}
	if job.Attempts >= w.options.MaxAttempts || !now.Before(deadline) {
		return outcome{status: statusFailed, code: codeVerificationUnavailable, class: class}
	}
	return outcome{status: statusQueued, class: class, delay: w.retryDelay(job.Attempts, deadline.Sub(now))}
}

// retryDelay is capped exponential backoff with equal jitter, at least one
// second and never past the retry deadline.
func (w *Worker) retryDelay(attempt int, remaining time.Duration) time.Duration {
	ceiling := min(w.options.MaxBackoff, 2*time.Second<<min(max(attempt-1, 0), 10))
	half := ceiling / 2
	delay := half + time.Duration(w.jitter(int64(half)+1))
	return min(max(delay, time.Second), remaining)
}

func classify(err error) string {
	var status *storage.StatusError
	var transport *storage.TransportError
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return classStorageNotFound
	case errors.Is(err, storage.ErrChecksumAbsent):
		return classStorageChecksumUnavailable
	case errors.As(err, &status):
		return classifyStatus(status.StatusCode)
	case errors.As(err, &transport):
		if transport.Timeout {
			return classStorageTimeout
		}
		return classStorageUnreachable
	case errors.Is(err, errDecodeCapacity):
		return classDecodeCapacity
	case errors.Is(err, errAttemptsExhausted):
		return classAttemptsExhausted
	case errors.Is(err, context.DeadlineExceeded):
		return classJobTimeout
	default:
		return classInternalError
	}
}

func classifyStatus(code int) string {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return classStorageAccessDenied
	case code == http.StatusBadRequest:
		return classStorageBadRequest
	case code >= 300 && code < 400:
		return classStorageRedirect
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500:
		return classStorageUnavailable
	default:
		return classStorageHTTPError
	}
}

func classifyDatabase(err error) (class, sqlState string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		sqlState = pgErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return classDatabaseTimeout, sqlState
	}
	return classDatabaseUnavailable, sqlState
}

// logDatabaseFailure records a bounded class and SQLSTATE, never the driver's
// error text, which can quote statement parameters.
func (w *Worker) logDatabaseFailure(ctx context.Context, message string, err error, attrs ...any) {
	class, sqlState := classifyDatabase(err)
	w.countError(class)
	attrs = append(attrs, "class", class)
	if sqlState != "" {
		attrs = append(attrs, "sqlstate", sqlState)
	}
	w.log.ErrorContext(ctx, message, attrs...)
}

// record counts and logs an applied outcome. Logs carry only identifiers and
// bounded values: never error text, object keys, URLs or checksums.
func (w *Worker) record(ctx context.Context, job Job, result outcome, elapsed time.Duration) {
	level := slog.LevelInfo
	switch result.status {
	case statusSucceeded:
		w.succeeded.Add(1)
	case statusRejected:
		w.rejected.Add(1)
	case statusQueued:
		w.retried.Add(1)
		w.countError(result.class)
		if systemicClasses[result.class] {
			level = slog.LevelWarn
		}
	case statusFailed:
		w.failed.Add(1)
		w.countError(result.class)
		level = slog.LevelWarn
	}
	w.log.Log(ctx, level, "evidence verification", "evidence_id", job.ID, "status", result.status,
		"code", result.code, "class", result.class, "attempt", job.Attempts, "lease_recoveries", job.LeaseRecoveries,
		"delay_ms", result.delay.Milliseconds(), "duration_ms", elapsed.Milliseconds())
}

func (w *Worker) countError(class string) {
	counter, ok := w.errorCounts[class]
	if !ok {
		counter = w.errorCounts[classInternalError]
	}
	counter.Add(1)
}

// boundedLabel applies the database CHECK on codes and classes before the
// fenced write, so an unexpected value degrades instead of failing it.
func boundedLabel(value, fallback string) string {
	if value == "" || boundedLabelPattern.MatchString(value) {
		return value
	}
	return fallback
}

func (w *Worker) finish(ctx context.Context, job Job, result outcome, etag string) (bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var owned bool
	err = tx.QueryRow(ctx, `SELECT true FROM evidence_media_processing_jobs
		WHERE evidence_id=$1 AND status='running' AND locked_by=$2 FOR UPDATE`, job.ID, job.Owner).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	code := boundedLabel(result.code, codeVerificationUnavailable)
	class := boundedLabel(result.class, classInternalError)
	if result.status != statusQueued {
		evidenceStatus := result.status
		if result.status == statusSucceeded {
			evidenceStatus = "completed"
		}
		_, err = tx.Exec(ctx, `UPDATE evidence_uploads SET status=$2,processed_at=now(),
			processing_error_code=NULLIF($3,''),provider_etag=NULLIF($4,''),updated_at=now()
			WHERE id=$1 AND status='processing'`, job.ID, evidenceStatus, code, etag)
		if err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE evidence_media_processing_jobs SET status=$2,last_error=NULLIF($3,''),
		available_at=now()+make_interval(secs=>$4),locked_at=NULL,locked_by=NULL,
		finished_at=CASE WHEN $2='queued' THEN NULL ELSE now() END,updated_at=now()
		WHERE evidence_id=$1`, job.ID, result.status, class, result.delay.Seconds())
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// This endpoint belongs on an internal monitoring network only. It exposes no
// user identifiers, object keys, bearer credentials or signed URLs.
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := w.pool.Ping(ctx); err != nil {
			http.Error(rw, "database unavailable", 503)
			return
		}
		rw.WriteHeader(200)
	})
	mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var queued, failed int64
		var age float64
		err := w.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE job.status IN ('queued','running')),
			COALESCE(max(EXTRACT(EPOCH FROM now()-job.created_at)) FILTER (WHERE job.status IN ('queued','running')),0),
			count(*) FILTER (WHERE job.status='failed')
			FROM evidence_media_processing_jobs job JOIN evidence_uploads evidence ON evidence.id=job.evidence_id
			WHERE evidence.media_kind='image' AND job.status IN ('queued','running','failed')`).Scan(&queued, &age, &failed)
		if err != nil {
			http.Error(rw, "metrics unavailable", 503)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(rw, w.metrics(queued, age, failed))
	})
	return mux
}

func (w *Worker) metrics(queued int64, age float64, failed int64) string {
	var body strings.Builder
	fmt.Fprintf(&body, "gamics_evidence_queue_depth %d\ngamics_evidence_oldest_job_seconds %g\ngamics_evidence_failed_jobs %d\ngamics_evidence_active %d\ngamics_evidence_succeeded_total %d\ngamics_evidence_rejected_total %d\ngamics_evidence_retries_total %d\ngamics_evidence_failed_total %d\n",
		queued, age, failed, w.active.Load(), w.succeeded.Load(), w.rejected.Load(), w.retried.Load(), w.failed.Load())
	for _, class := range errorClasses {
		fmt.Fprintf(&body, "gamics_evidence_errors_total{class=%q} %d\n", class, w.errorCounts[class].Load())
	}
	fmt.Fprintf(&body, "gamics_evidence_decode_budget_bytes %d\ngamics_evidence_decode_budget_in_use_bytes %d\ngamics_evidence_decode_budget_waits_total %d\n",
		w.budget.size, w.budget.inUse.Load(), w.budget.waits.Load())
	return body.String()
}
