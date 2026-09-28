package evidence

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSource normalizes line endings: Windows checkouts may convert to CRLF.
func readSource(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func requireFragments(t *testing.T, name, source string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(source, fragment) {
			t.Fatalf("%s is missing %q", name, fragment)
		}
	}
}

func TestEvidenceWorkerHardeningMigration(t *testing.T) {
	up := readSource(t, "..", "..", "migrations", "000021_evidence_worker_hardening.up.sql")
	down := readSource(t, "..", "..", "migrations", "000021_evidence_worker_hardening.down.sql")
	for name, source := range map[string]string{"up": up, "down": down} {
		trimmed := strings.TrimSpace(source)
		if !strings.HasPrefix(trimmed, "BEGIN;") || !strings.HasSuffix(trimmed, "COMMIT;") {
			t.Fatalf("%s migration must be wrapped in BEGIN;...COMMIT;", name)
		}
	}
	pattern := "'" + boundedLabelPattern.String() + "'"
	requireFragments(t, "up migration", up,
		"evidence.media_kind='video' AND job.status IN ('queued','running')",
		"processing_error_code='video_unsupported'",
		"WHERE media_kind='video' AND status IN ('pending','processing')",
		"last_error !~ "+pattern,
		"processing_error_code='legacy_error'",
		"ADD CONSTRAINT evidence_uploads_image_only_intake_chk",
		"CHECK (media_kind = 'image' OR status IN ('completed','failed','rejected','expired'))",
		"ADD CONSTRAINT evidence_uploads_processing_error_code_chk",
		"processing_error_code ~ "+pattern,
		"ADD COLUMN lease_recoveries integer NOT NULL DEFAULT 0 CHECK (lease_recoveries >= 0)",
		"ADD CONSTRAINT evidence_media_processing_jobs_last_error_class_chk",
		"last_error ~ "+pattern,
	)
	requireFragments(t, "down migration", down,
		"DROP CONSTRAINT IF EXISTS evidence_media_processing_jobs_last_error_class_chk",
		"DROP COLUMN IF EXISTS lease_recoveries",
		"DROP CONSTRAINT IF EXISTS evidence_uploads_processing_error_code_chk",
		"DROP CONSTRAINT IF EXISTS evidence_uploads_image_only_intake_chk",
		"intentionally stay failed",
	)
	if strings.Contains(up, "CONCURRENTLY") {
		t.Fatal("migrations run inside a transaction")
	}
	requireFragments(t, "claim", claimSQL,
		"job.status AS prior_status",
		"lease_recoveries=lease_recoveries+(candidate.prior_status='running')::int",
		"RETURNING job.evidence_id,job.attempts,job.lease_recoveries,job.created_at",
		"evidence.upload_expires_at",
		"claimed.created_at,now()",
	)
}

func TestContainerHealthcheckFollowsProcessPort(t *testing.T) {
	dockerfile := readSource(t, "..", "..", "Dockerfile")
	requireFragments(t, "Dockerfile", dockerfile,
		"ENV HEALTHCHECK_PORT=8080",
		`CMD wget -q -O /dev/null "http://127.0.0.1:${HEALTHCHECK_PORT}/healthz" || exit 1`,
	)
	if strings.Contains(dockerfile, "127.0.0.1:8080/healthz") {
		t.Fatal("the image healthcheck still pins the API port")
	}
}

// The compose file lives at the repository root, outside this module; it is
// absent when only services/api is checked out or copied.
func TestComposeConfiguresEvidenceWorker(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "compose.yaml")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		t.Skip("repository compose.yaml not present")
	}
	compose := readSource(t, path)
	start := strings.Index(compose, "\n  evidence-worker:\n")
	if start < 0 {
		t.Fatal("compose.yaml has no evidence-worker service")
	}
	service := compose[start+1:]
	if end := strings.Index(service, "\n\n"); end >= 0 {
		service = service[:end]
	}
	requireFragments(t, "evidence-worker service", service,
		`HEALTHCHECK_PORT: "8081"`,
		"EVIDENCE_WORKER_HTTP_ADDR: :8081",
		"EVIDENCE_WORKER_RETRY_WINDOW: ${EVIDENCE_WORKER_RETRY_WINDOW:-5m}",
		"EVIDENCE_WORKER_MAX_BACKOFF: ${EVIDENCE_WORKER_MAX_BACKOFF:-30s}",
		"EVIDENCE_WORKER_UPLOAD_GRACE: ${EVIDENCE_WORKER_UPLOAD_GRACE:-2m}",
		"EVIDENCE_WORKER_MAX_LEASE_RECOVERIES: ${EVIDENCE_WORKER_MAX_LEASE_RECOVERIES:-2}",
		"EVIDENCE_WORKER_DECODE_BUDGET_BYTES: ${EVIDENCE_WORKER_DECODE_BUDGET_BYTES:-201326592}",
		"GOMEMLIMIT: 384MiB",
		"http://127.0.0.1:8081/healthz",
	)
}
