package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// The image decoder process has no reason to receive authentication signing,
// payment, mail or push credentials. Load only its database/storage settings.
func LoadEvidenceWorker() (Config, error) {
	cfg := Config{
		Environment:           value("APP_ENV", "development"),
		DatabaseWriteURL:      value("DATABASE_WRITE_URL", os.Getenv("DATABASE_URL")),
		StorageMode:           strings.ToLower(value("STORAGE_MODE", "disabled")),
		StorageS3Endpoint:     strings.TrimSpace(os.Getenv("STORAGE_S3_ENDPOINT")),
		StorageS3Region:       strings.TrimSpace(os.Getenv("STORAGE_S3_REGION")),
		StorageS3Bucket:       strings.TrimSpace(os.Getenv("STORAGE_S3_BUCKET")),
		StorageS3AccessKey:    strings.TrimSpace(os.Getenv("STORAGE_S3_ACCESS_KEY")),
		StorageS3SecretKey:    os.Getenv("STORAGE_S3_SECRET_KEY"),
		StorageS3SessionToken: os.Getenv("STORAGE_S3_SESSION_TOKEN"),
	}
	cfg.StorageS3PublicEndpoint = cfg.StorageS3Endpoint // This process never issues client URLs.
	if err := configureR2(&cfg); err != nil {
		return Config{}, err
	}
	var err error
	cfg.StorageS3PathStyle, err = boolean("STORAGE_S3_PATH_STYLE", true)
	if err != nil {
		return Config{}, err
	}
	cfg.StorageHTTPTimeout, err = duration("STORAGE_HTTP_TIMEOUT", 10*time.Second)
	if err != nil || cfg.StorageHTTPTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid storage timeout")
	}
	if cfg.DatabaseWriteURL == "" {
		return Config{}, fmt.Errorf("worker DATABASE_WRITE_URL is required")
	}
	if !cfg.StorageEnabled() || cfg.StorageS3Region == "" || cfg.StorageS3Bucket == "" || cfg.StorageS3AccessKey == "" || cfg.StorageS3SecretKey == "" {
		return Config{}, fmt.Errorf("worker requires S3 endpoint, region, bucket and credentials")
	}
	origin, err := storageOrigin(cfg.StorageS3Endpoint)
	if err != nil {
		return Config{}, fmt.Errorf("invalid worker S3 endpoint")
	}
	if cfg.Environment == "production" && origin.Scheme != "https" {
		return Config{}, fmt.Errorf("production worker storage requires HTTPS")
	}
	if cfg.StorageMode == "r2" {
		cfg.StorageS3PathStyle = true
	}
	return cfg, nil
}

// These bounds mirror evidence.Options, which NewWorker re-validates; config
// cannot import that package. The decode budget floor admits the largest
// image the structural limits allow (96 MiB) together with its 25 MiB body.
const (
	minEvidenceDecodeBudgetBytes = 121 << 20
	maxEvidenceDecodeBudgetBytes = 1 << 30
)

// EvidenceWorkerLimits holds the image worker's concurrency, retry and
// decoder-memory settings.
type EvidenceWorkerLimits struct {
	Concurrency        int
	RetryWindow        time.Duration
	MaxBackoff         time.Duration
	UploadGrace        time.Duration
	MaxLeaseRecoveries int
	DecodeBudgetBytes  int64
}

// LoadEvidenceWorkerLimits parses the worker's tuning variables strictly: a
// malformed or out-of-range value stops the process instead of silently
// falling back to a default.
func LoadEvidenceWorkerLimits() (EvidenceWorkerLimits, error) {
	concurrency, err := boundedInteger("EVIDENCE_WORKER_CONCURRENCY", 2, 1, 32)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	retryWindow, err := boundedDuration("EVIDENCE_WORKER_RETRY_WINDOW", 5*time.Minute, time.Minute, 24*time.Hour)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	maxBackoff, err := boundedDuration("EVIDENCE_WORKER_MAX_BACKOFF", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	if maxBackoff >= retryWindow {
		return EvidenceWorkerLimits{}, fmt.Errorf("EVIDENCE_WORKER_MAX_BACKOFF must be shorter than EVIDENCE_WORKER_RETRY_WINDOW")
	}
	uploadGrace, err := boundedDuration("EVIDENCE_WORKER_UPLOAD_GRACE", 2*time.Minute, 0, 10*time.Minute)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	recoveries, err := boundedInteger("EVIDENCE_WORKER_MAX_LEASE_RECOVERIES", 2, 2, 10)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	budget, err := boundedInteger("EVIDENCE_WORKER_DECODE_BUDGET_BYTES", 192<<20, minEvidenceDecodeBudgetBytes, maxEvidenceDecodeBudgetBytes)
	if err != nil {
		return EvidenceWorkerLimits{}, err
	}
	return EvidenceWorkerLimits{
		Concurrency: int(concurrency), RetryWindow: retryWindow, MaxBackoff: maxBackoff,
		UploadGrace: uploadGrace, MaxLeaseRecoveries: int(recoveries), DecodeBudgetBytes: budget,
	}, nil
}
