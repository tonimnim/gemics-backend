package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const evidenceCompletionOwner = "20000000-0000-4000-8000-000000000001"

// evidenceCompletionSchema holds the evidence columns and CHECKs, through
// 000021, that completion and the image worker's claim rely on.
const evidenceCompletionSchema = `CREATE TABLE evidence_uploads (
 id uuid PRIMARY KEY,owner_user_id uuid NOT NULL,object_key text NOT NULL,media_kind text NOT NULL DEFAULT 'image',
 media_type text NOT NULL DEFAULT 'image/png',byte_size bigint NOT NULL DEFAULT 2000000,
 checksum_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex'),status text NOT NULL DEFAULT 'pending',
 upload_expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',declared_duration_seconds numeric,
 verified_duration_seconds numeric,processing_error_code text,completed_at timestamptz,
 processed_at timestamptz,bound_kind text,bound_id uuid,provider_etag text,updated_at timestamptz,
 CONSTRAINT evidence_uploads_processing_state_chk
  CHECK (status<>'processing' OR (completed_at IS NOT NULL AND processed_at IS NULL)),
 CONSTRAINT evidence_uploads_processing_error_code_chk
  CHECK (processing_error_code IS NULL OR processing_error_code ~ '^[a-z][a-z0-9_]{0,63}$'));
CREATE TABLE evidence_media_processing_jobs (
 evidence_id uuid PRIMARY KEY REFERENCES evidence_uploads(id),status text NOT NULL DEFAULT 'queued',
 attempts integer NOT NULL DEFAULT 0,available_at timestamptz NOT NULL DEFAULT now(),last_error text,
 created_at timestamptz NOT NULL DEFAULT now(),
 lease_recoveries integer NOT NULL DEFAULT 0 CHECK (lease_recoveries >= 0));
CREATE TABLE outbox_events (
 aggregate_type text NOT NULL,aggregate_id text NOT NULL,event_type text NOT NULL,payload jsonb NOT NULL);`

// newEvidenceCompletionServer serves completeEvidenceUpload against a private
// schema and real Redis limits. seedSQL runs before 000021's image-only intake
// CHECK is added NOT VALID, so it may insert legacy rows that predate it while
// every later write must satisfy it. Only this test's schema and rate-limit
// key are cleaned up.
func newEvidenceCompletionServer(t *testing.T, seedSQL string) (*Server, *pgxpool.Pool) {
	t.Helper()
	redisURL := os.Getenv("GAMICS_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("GAMICS_TEST_REDIS_URL not configured")
	}
	pool := openIntegrationSchema(t)
	for _, statement := range []string{evidenceCompletionSchema, seedSQL, `ALTER TABLE evidence_uploads
		ADD CONSTRAINT evidence_uploads_image_only_intake_chk
		CHECK (media_kind='image' OR status IN ('completed','failed','rejected','expired')) NOT VALID`} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal("invalid test Redis configuration")
	}
	cache := redis.NewClient(options)
	t.Cleanup(func() { _ = cache.Close() })
	s := &Server{db: &database.Cluster{Writer: pool, Reader: pool}, redis: cache,
		config: config.Config{CacheNamespace: "test:" + rand.Text(), EvidenceActionLimit: 120},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), evidenceStore: completionNoStorageIO{t}}
	t.Cleanup(func() {
		if err := cache.Del(context.Background(), s.securityKey("evidence:{"+evidenceCompletionOwner+"}:actions")).Err(); err != nil {
			t.Error(err)
		}
	})
	return s, pool
}

func completeEvidenceAsOwner(ctx context.Context, s *Server, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/evidence/uploads/"+id+"/complete", nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(ctx, identityContextKey{}, identity{UserID: evidenceCompletionOwner}))
	w := httptest.NewRecorder()
	s.completeEvidenceUpload(w, r)
	return w
}

// Uses real PostgreSQL parameter inference/transactions and real Redis limits.
func TestEvidenceCompletionAtomicAndIdempotent(t *testing.T) {
	s, pool := newEvidenceCompletionServer(t, `INSERT INTO evidence_uploads(id,owner_user_id,object_key) VALUES
 ('10000000-0000-4000-8000-000000000001','`+evidenceCompletionOwner+`','test-one.png'),
 ('10000000-0000-4000-8000-000000000002','`+evidenceCompletionOwner+`','test-two.png')`)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	id := "10000000-0000-4000-8000-000000000001"
	// Race initial completions while the evidence is still pending, then retry
	// after commit. Both paths must create exactly one durable job and event.
	var group sync.WaitGroup
	statuses := make(chan int, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); statuses <- completeEvidenceAsOwner(ctx, s, id).Code }()
	}
	group.Wait()
	close(statuses)
	for code := range statuses {
		if code != http.StatusOK {
			t.Errorf("idempotent completion returned %d", code)
		}
	}
	w := completeEvidenceAsOwner(ctx, s, id)
	if w.Code != http.StatusOK {
		t.Fatalf("completion must queue successfully: HTTP %d %s", w.Code, w.Body.String())
	}
	var state, payloadID, eventType, jobStatus string
	var jobs, events, attempts, leaseRecoveries int
	if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM evidence_media_processing_jobs),
 (SELECT count(*) FROM outbox_events) FROM evidence_uploads WHERE id=$1`, id).Scan(&state, &jobs, &events); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || jobs != 1 || events != 1 {
		t.Fatalf("state=%s jobs=%d events=%d", state, jobs, events)
	}
	if err := pool.QueryRow(ctx, `SELECT status,attempts,lease_recoveries FROM evidence_media_processing_jobs
 WHERE evidence_id=$1 AND available_at<=now()`, id).Scan(&jobStatus, &attempts, &leaseRecoveries); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "queued" || attempts != 0 || leaseRecoveries != 0 {
		t.Fatalf("job is not immediately claimable: status=%s attempts=%d lease_recoveries=%d", jobStatus, attempts, leaseRecoveries)
	}
	if err := pool.QueryRow(ctx, `SELECT payload->>'evidenceId',event_type FROM outbox_events`).Scan(&payloadID, &eventType); err != nil {
		t.Fatal(err)
	}
	if payloadID != id || eventType != "evidence.image_processing_requested" {
		t.Fatal("incorrect outbox event")
	}

	// A failed outbox insert must roll back the status change AND queue insert.
	second := "10000000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_second CHECK (aggregate_id<>'10000000-0000-4000-8000-000000000002')`); err != nil {
		t.Fatal(err)
	}
	if w = completeEvidenceAsOwner(ctx, s, second); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected retryable outbox failure, got %d", w.Code)
	}
	if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM evidence_media_processing_jobs WHERE evidence_id=$1),
 (SELECT count(*) FROM outbox_events WHERE aggregate_id=$1::text) FROM evidence_uploads WHERE id=$1`, second).Scan(&state, &jobs, &events); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || jobs != 0 || events != 0 {
		t.Fatalf("failed transaction leaked: state=%s jobs=%d events=%d", state, jobs, events)
	}
}

// A video intent created before evidence became screenshot-only is settled as
// failed on completion, even after its link expired, with no storage I/O and
// no verification job.
func TestIntegrationLegacyVideoEvidenceCompletionFails(t *testing.T) {
	live, expired := "10000000-0000-4000-8000-000000000011", "10000000-0000-4000-8000-000000000012"
	s, pool := newEvidenceCompletionServer(t, `INSERT INTO evidence_uploads
 (id,owner_user_id,object_key,media_kind,media_type,declared_duration_seconds,upload_expires_at) VALUES
 ('`+live+`','`+evidenceCompletionOwner+`','legacy-live.mp4','video','video/mp4',12,now()+interval '10 minutes'),
 ('`+expired+`','`+evidenceCompletionOwner+`','legacy-expired.mov','video','video/quicktime',12,now()-interval '2 hours')`)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, id := range []string{live, expired} {
		w := completeEvidenceAsOwner(ctx, s, id)
		assertMediaError(t, w, http.StatusUnprocessableEntity, "evidence_media_unsupported")
		var status, code string
		var jobs, events int
		if err := pool.QueryRow(ctx, `SELECT status,processing_error_code,
 (SELECT count(*) FROM evidence_media_processing_jobs WHERE evidence_id=$1),
 (SELECT count(*) FROM outbox_events WHERE aggregate_id=$1::text)
 FROM evidence_uploads WHERE id=$1`, id).Scan(&status, &code, &jobs, &events); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || code != "video_unsupported" || jobs != 0 || events != 0 {
			t.Fatalf("legacy video %s: status=%s code=%s jobs=%d events=%d", id, status, code, jobs, events)
		}
		assertMediaError(t, completeEvidenceAsOwner(ctx, s, id), http.StatusConflict, "evidence_not_pending")
	}
}

type completionNoStorageIO struct{ t *testing.T }

func (s completionNoStorageIO) PresignPut(context.Context, string, string, string, int64, time.Duration) (storage.UploadIntent, error) {
	s.t.Error("unexpected storage I/O")
	return storage.UploadIntent{}, errors.New("unexpected I/O")
}
func (s completionNoStorageIO) PresignGet(context.Context, string, time.Duration) (storage.DownloadIntent, error) {
	s.t.Error("unexpected storage I/O")
	return storage.DownloadIntent{}, errors.New("unexpected I/O")
}
func (s completionNoStorageIO) Stat(context.Context, string) (storage.ObjectInfo, error) {
	s.t.Error("unexpected storage I/O")
	return storage.ObjectInfo{}, errors.New("unexpected I/O")
}
