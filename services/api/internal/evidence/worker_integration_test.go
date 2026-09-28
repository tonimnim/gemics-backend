package evidence

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

// integrationPool opens a disposable schema holding the two queue tables with
// the columns and CHECKs the worker relies on (000012, 000019, 000021). Each
// test creates and drops only its own random schema; never point this test at
// a production database.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("GAMICS_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("GAMICS_TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	t.Cleanup(admin.Close)
	schema := "evidence_test_" + rand.Text()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE TABLE evidence_uploads (
		id uuid PRIMARY KEY, object_key text, media_kind text DEFAULT 'image', media_type text DEFAULT 'image/png',
		byte_size bigint DEFAULT 1,checksum_sha256 bytea DEFAULT decode(repeat('00',32),'hex'),
		status text DEFAULT 'processing',processed_at timestamptz,processing_error_code text,provider_etag text,updated_at timestamptz,
		upload_expires_at timestamptz NOT NULL DEFAULT now()+interval '15 minutes',
		CONSTRAINT evidence_uploads_processing_error_code_chk
			CHECK (processing_error_code IS NULL OR processing_error_code ~ '^[a-z][a-z0-9_]{0,63}$'));
		CREATE TABLE evidence_media_processing_jobs (
		evidence_id uuid PRIMARY KEY REFERENCES evidence_uploads(id),status text DEFAULT 'queued',attempts int DEFAULT 0,
		available_at timestamptz DEFAULT now(),locked_at timestamptz,locked_by text,last_error text,
		created_at timestamptz DEFAULT now(),updated_at timestamptz,finished_at timestamptz,
		lease_recoveries integer NOT NULL DEFAULT 0 CHECK (lease_recoveries >= 0),
		CONSTRAINT evidence_media_processing_jobs_last_error_class_chk
			CHECK (last_error IS NULL OR last_error ~ '^[a-z][a-z0-9_]{0,63}$'));`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func insertQueuedEvidence(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO evidence_uploads(id,object_key) VALUES ($1,'test.png');
		INSERT INTO evidence_media_processing_jobs(evidence_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
}

func expireLeases(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE evidence_media_processing_jobs SET locked_at=now()-interval '5 minutes' WHERE status='running'`); err != nil {
		t.Fatal(err)
	}
}

func TestQueueConcurrentClaimsRecoveryAndFencing(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	insertQueuedEvidence(t, pool, "10000000-0000-4000-8000-000000000001")
	w, err := NewWorker(pool, &fakeStore{}, discardLogger(), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	jobs := make(chan Job, 20)
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			job, err := w.claim(ctx)
			if err == nil {
				jobs <- job
			} else if !errors.Is(err, pgx.ErrNoRows) {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	close(jobs)
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one lease, got %d", len(jobs))
	}
	old := <-jobs
	if old.LeaseRecoveries != 0 || old.QueuedAt.IsZero() || old.DBNow.Before(old.QueuedAt) || !old.UploadExpiresAt.After(old.QueuedAt) {
		t.Fatalf("claim did not return the retry clock: %#v", old)
	}
	expireLeases(t, pool)
	recovered, err := w.claim(ctx)
	if err != nil || recovered.Attempts != 2 || recovered.LeaseRecoveries != 1 || !recovered.QueuedAt.Equal(old.QueuedAt) {
		t.Fatalf("lease not recovered: %#v %v", recovered, err)
	}
	if applied, err := w.finish(ctx, old, outcome{status: statusSucceeded}, "old"); err != nil || applied {
		t.Fatalf("stale lease committed: %v %v", applied, err)
	}
	retry := outcome{status: statusQueued, class: classStorageUnavailable, delay: time.Second}
	if applied, err := w.finish(ctx, recovered, retry, ""); err != nil || !applied {
		t.Fatalf("retry not saved: %v %v", applied, err)
	}
	var lastError string
	if err = pool.QueryRow(ctx, `SELECT last_error FROM evidence_media_processing_jobs`).Scan(&lastError); err != nil || lastError != classStorageUnavailable {
		t.Fatalf("retry class not recorded: %q %v", lastError, err)
	}
	if _, err = w.claim(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("backoff was ignored: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE evidence_media_processing_jobs SET available_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	final, err := w.claim(ctx)
	if err != nil || final.LeaseRecoveries != 1 {
		t.Fatalf("queued claim counted as a lease recovery: %#v %v", final, err)
	}
	if applied, err := w.finish(ctx, final, outcome{status: statusSucceeded}, "verified"); err != nil || !applied {
		t.Fatalf("completion failed: %v %v", applied, err)
	}
	var status, jobStatus string
	var errorCode, jobError *string
	if err = pool.QueryRow(ctx, `SELECT evidence.status,job.status,evidence.processing_error_code,job.last_error
		FROM evidence_uploads evidence JOIN evidence_media_processing_jobs job ON job.evidence_id=evidence.id`).Scan(&status, &jobStatus, &errorCode, &jobError); err != nil ||
		status != "completed" || jobStatus != "succeeded" || errorCode != nil || jobError != nil {
		t.Fatalf("non-atomic completion: %s %s %v %v %v", status, jobStatus, errorCode, jobError, err)
	}
	if _, err = w.claim(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("completed job reclaimed: %v", err)
	}
}

func TestIntegrationQueueQuarantinesRepeatedLeaseRecoveries(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	insertQueuedEvidence(t, pool, "10000000-0000-4000-8000-000000000002")
	store := &fakeStore{}
	w, err := NewWorker(pool, store, discardLogger(), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	for recoveries := 0; recoveries <= w.options.MaxLeaseRecoveries+1; recoveries++ {
		if recoveries > 0 {
			expireLeases(t, pool)
		}
		if job, err = w.claim(ctx); err != nil || job.LeaseRecoveries != recoveries {
			t.Fatalf("claim %d: %#v %v", recoveries, job, err)
		}
	}
	w.process(ctx, job)
	var status, code, jobStatus, class string
	if err = pool.QueryRow(ctx, `SELECT evidence.status,evidence.processing_error_code,job.status,job.last_error
		FROM evidence_uploads evidence JOIN evidence_media_processing_jobs job ON job.evidence_id=evidence.id`).Scan(&status, &code, &jobStatus, &class); err != nil {
		t.Fatal(err)
	}
	if status != statusFailed || code != codeProcessingAborted || jobStatus != statusFailed || class != classLeaseRecoveryExhausted {
		t.Fatalf("poison job not quarantined: %s %s %s %s", status, code, jobStatus, class)
	}
	if store.stats != 0 || store.reads != 0 {
		t.Fatal("quarantined job touched storage")
	}
}

func TestIntegrationQueueFailsTransientErrorsAfterRetryWindow(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	insertQueuedEvidence(t, pool, "10000000-0000-4000-8000-000000000003")
	if _, err := pool.Exec(ctx, `UPDATE evidence_media_processing_jobs SET created_at=now()-interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{err: &storage.StatusError{Op: "head", StatusCode: 503}}
	w, err := NewWorker(pool, store, discardLogger(), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	job, err := w.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.process(ctx, job)
	var status, code, class string
	if err = pool.QueryRow(ctx, `SELECT evidence.status,evidence.processing_error_code,job.last_error
		FROM evidence_uploads evidence JOIN evidence_media_processing_jobs job ON job.evidence_id=evidence.id`).Scan(&status, &code, &class); err != nil {
		t.Fatal(err)
	}
	if status != statusFailed || code != codeVerificationUnavailable || class != classStorageUnavailable || store.stats != 1 {
		t.Fatalf("stale transient failure retried: %s %s %s", status, code, class)
	}
}

func TestIntegrationQueueLastErrorRejectsFreeText(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	insertQueuedEvidence(t, pool, "10000000-0000-4000-8000-000000000004")
	for _, statement := range []string{
		`UPDATE evidence_media_processing_jobs SET last_error='storage HEAD returned 403: <Key>evidence/a.png</Key>'`,
		`UPDATE evidence_uploads SET processing_error_code='Invalid Image'`,
	} {
		_, err := pool.Exec(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("free text accepted by %q: %v", statement, err)
		}
	}
}
