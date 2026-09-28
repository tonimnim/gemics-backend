package config

import (
	"testing"
	"time"
)

func TestEvidenceWorkerUsesOnlyStorageAndDatabaseCredentials(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_WRITE_URL", "postgres://writer.example/gamics")
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("STORAGE_S3_ENDPOINT", "https://s3.example.test")
	t.Setenv("STORAGE_S3_REGION", "af-south-1")
	t.Setenv("STORAGE_S3_BUCKET", "private-evidence")
	t.Setenv("STORAGE_S3_ACCESS_KEY", "worker")
	t.Setenv("STORAGE_S3_SECRET_KEY", "worker-only")
	t.Setenv("AUTH_TOKEN_SECRET", "")
	t.Setenv("OTP_HASH_SECRET", "")
	t.Setenv("EMAIL_MODE", "log")
	cfg, err := LoadEvidenceWorker()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessTokenSecret != "" || cfg.MPesaConsumerSecret != "" || cfg.SMTPPassword != "" {
		t.Fatal("unrelated secrets loaded")
	}
	t.Setenv("STORAGE_S3_ENDPOINT", "http://s3.example.test")
	if _, err = LoadEvidenceWorker(); err == nil {
		t.Fatal("insecure production worker storage accepted")
	}
}

var evidenceWorkerLimitKeys = []string{
	"EVIDENCE_WORKER_CONCURRENCY", "EVIDENCE_WORKER_RETRY_WINDOW", "EVIDENCE_WORKER_MAX_BACKOFF",
	"EVIDENCE_WORKER_UPLOAD_GRACE", "EVIDENCE_WORKER_MAX_LEASE_RECOVERIES", "EVIDENCE_WORKER_DECODE_BUDGET_BYTES",
}

func TestEvidenceWorkerLimitDefaults(t *testing.T) {
	for _, key := range evidenceWorkerLimitKeys {
		t.Setenv(key, "")
	}
	limits, err := LoadEvidenceWorkerLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := EvidenceWorkerLimits{
		Concurrency: 2, RetryWindow: 5 * time.Minute, MaxBackoff: 30 * time.Second,
		UploadGrace: 2 * time.Minute, MaxLeaseRecoveries: 2, DecodeBudgetBytes: 192 << 20,
	}
	if limits != want {
		t.Fatalf("got %+v, want %+v", limits, want)
	}
	t.Setenv("EVIDENCE_WORKER_CONCURRENCY", " 32 ")
	t.Setenv("EVIDENCE_WORKER_RETRY_WINDOW", "24h")
	t.Setenv("EVIDENCE_WORKER_MAX_BACKOFF", "5m")
	t.Setenv("EVIDENCE_WORKER_UPLOAD_GRACE", "0s")
	t.Setenv("EVIDENCE_WORKER_MAX_LEASE_RECOVERIES", "10")
	t.Setenv("EVIDENCE_WORKER_DECODE_BUDGET_BYTES", "1073741824")
	if limits, err = LoadEvidenceWorkerLimits(); err != nil || limits.Concurrency != 32 || limits.UploadGrace != 0 || limits.DecodeBudgetBytes != 1<<30 {
		t.Fatalf("upper bounds rejected: %+v %v", limits, err)
	}
}

func TestEvidenceWorkerRetrySettingsBounds(t *testing.T) {
	tests := []struct{ key, value string }{
		{"EVIDENCE_WORKER_CONCURRENCY", "0"},
		{"EVIDENCE_WORKER_CONCURRENCY", "33"},
		{"EVIDENCE_WORKER_CONCURRENCY", "two"},
		{"EVIDENCE_WORKER_CONCURRENCY", "2.5"},
		{"EVIDENCE_WORKER_RETRY_WINDOW", "59s"},
		{"EVIDENCE_WORKER_RETRY_WINDOW", "25h"},
		{"EVIDENCE_WORKER_RETRY_WINDOW", "300"},
		{"EVIDENCE_WORKER_MAX_BACKOFF", "999ms"},
		{"EVIDENCE_WORKER_MAX_BACKOFF", "6m"},
		{"EVIDENCE_WORKER_MAX_BACKOFF", "fast"},
		{"EVIDENCE_WORKER_UPLOAD_GRACE", "-1s"},
		{"EVIDENCE_WORKER_UPLOAD_GRACE", "11m"},
		{"EVIDENCE_WORKER_MAX_LEASE_RECOVERIES", "1"},
		{"EVIDENCE_WORKER_MAX_LEASE_RECOVERIES", "11"},
		{"EVIDENCE_WORKER_DECODE_BUDGET_BYTES", "126877695"},
		{"EVIDENCE_WORKER_DECODE_BUDGET_BYTES", "1073741825"},
		{"EVIDENCE_WORKER_DECODE_BUDGET_BYTES", "192MiB"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			for _, key := range evidenceWorkerLimitKeys {
				t.Setenv(key, "")
			}
			t.Setenv(test.key, test.value)
			if limits, err := LoadEvidenceWorkerLimits(); err == nil {
				t.Fatalf("accepted %+v", limits)
			}
		})
	}
	t.Run("backoff not shorter than window", func(t *testing.T) {
		for _, key := range evidenceWorkerLimitKeys {
			t.Setenv(key, "")
		}
		t.Setenv("EVIDENCE_WORKER_RETRY_WINDOW", "1m")
		t.Setenv("EVIDENCE_WORKER_MAX_BACKOFF", "1m")
		if _, err := LoadEvidenceWorkerLimits(); err == nil {
			t.Fatal("backoff equal to the retry window accepted")
		}
	})
}
