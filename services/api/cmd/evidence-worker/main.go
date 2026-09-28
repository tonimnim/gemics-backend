package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/evidence"
	"github.com/gamics-io/gamics/services/api/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("evidence worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadEvidenceWorker()
	if err != nil {
		return err
	}
	if !cfg.StorageEnabled() {
		return errors.New("evidence worker requires STORAGE_MODE=s3 or r2")
	}
	limits, err := config.LoadEvidenceWorkerLimits()
	if err != nil {
		return err
	}
	options := evidence.DefaultOptions()
	options.Concurrency = limits.Concurrency
	options.RetryWindow = limits.RetryWindow
	options.MaxBackoff = limits.MaxBackoff
	options.UploadGrace = limits.UploadGrace
	options.MaxLeaseRecoveries = limits.MaxLeaseRecoveries
	options.DecodeBudgetBytes = limits.DecodeBudgetBytes
	memoryLimit, err := checkMemoryLimit(options)
	if err != nil {
		return err
	}
	if memoryLimit == math.MaxInt64 {
		logger.Warn("GOMEMLIMIT is not set; the decode budget is not checked against process memory")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(startup, cfg.DatabaseWriteURL, cfg.DatabaseWriteURL, int32(min(max(options.Concurrency, 1), 8)), 1)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.PingWriter(startup); err != nil {
		return errors.New("evidence database unavailable")
	}
	store, err := storage.NewS3(storage.S3Config{
		Endpoint: cfg.StorageS3Endpoint, PublicEndpoint: cfg.StorageS3PublicEndpoint, Region: cfg.StorageS3Region,
		Bucket: cfg.StorageS3Bucket, AccessKey: cfg.StorageS3AccessKey, SecretKey: cfg.StorageS3SecretKey,
		SessionToken: cfg.StorageS3SessionToken, ForcePathStyle: cfg.StorageS3PathStyle,
		HTTPClient: storage.NewHTTPClient(cfg.StorageHTTPTimeout),
	})
	if err != nil {
		return err
	}
	worker, err := evidence.NewWorker(db.Writer, store, logger, options)
	if err != nil {
		return err
	}
	addr := os.Getenv("EVIDENCE_WORKER_HTTP_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	server := &http.Server{Addr: addr, Handler: worker.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	logger.Info("evidence worker ready", "concurrency", options.Concurrency,
		"retry_window", options.RetryWindow.String(), "max_backoff", options.MaxBackoff.String(),
		"upload_grace", options.UploadGrace.String(), "max_lease_recoveries", options.MaxLeaseRecoveries,
		"decode_budget_bytes", options.DecodeBudgetBytes, "memory_required_bytes", options.MemoryRequirement(),
		"memory_limit_bytes", memoryLimit)
	select {
	case err = <-errorsCh:
		stop()
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdown)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// checkMemoryLimit refuses a GOMEMLIMIT that cannot hold every in-flight body
// plus the whole decode budget: the collector would thrash long before the
// budget was exhausted, and the container would be killed mid-decode.
func checkMemoryLimit(options evidence.Options) (int64, error) {
	limit := debug.SetMemoryLimit(-1)
	if required := options.MemoryRequirement(); limit != math.MaxInt64 && required > limit {
		return 0, fmt.Errorf("GOMEMLIMIT of %d bytes is below the %d bytes required by EVIDENCE_WORKER_CONCURRENCY and EVIDENCE_WORKER_DECODE_BUDGET_BYTES", limit, required)
	}
	return limit, nil
}
