package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/httpapi"
	gamicsmail "github.com/gamics-io/gamics/services/api/internal/mail"
	"github.com/gamics-io/gamics/services/api/internal/mpesa"
	"github.com/gamics-io/gamics/services/api/internal/storage"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(startupCtx, cfg.DatabaseWriteURL, cfg.DatabaseReadURL, cfg.DatabaseWriteMax, cfg.DatabaseReadMax)
	if err != nil {
		logger.Error("open database cluster", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.PingWriter(startupCtx); err != nil {
		logger.Error("database writer unavailable", "error", err)
		os.Exit(1)
	}
	if cfg.RunMigrations {
		if err := database.Migrate(startupCtx, db.Writer); err != nil {
			logger.Error("apply database migrations", "error", err)
			os.Exit(1)
		}
	}
	if err := db.PingReader(startupCtx); err != nil {
		logger.Warn("database reader unavailable; reads will fall back to writer", "error", err)
	}

	securityRedis, redisErr := cache.Open(startupCtx, cfg.RedisSecurityURL)
	if securityRedis == nil {
		logger.Error("invalid security Redis configuration", "error", redisErr)
		os.Exit(1)
	}
	defer securityRedis.Close()
	if redisErr != nil {
		logger.Warn("security Redis unavailable at startup; database fallback enabled", "error", redisErr)
	}
	cacheRedis := securityRedis
	if cfg.RedisCacheURL != cfg.RedisSecurityURL {
		cacheRedis, redisErr = cache.Open(startupCtx, cfg.RedisCacheURL)
		if cacheRedis == nil {
			logger.Error("invalid cache Redis configuration", "error", redisErr)
			os.Exit(1)
		}
		defer cacheRedis.Close()
		if redisErr != nil {
			logger.Warn("cache Redis unavailable at startup; database fallback enabled", "error", redisErr)
		}
	}

	var sender gamicsmail.Sender = gamicsmail.LogSender{Logger: logger}
	if cfg.EmailMode == "smtp" {
		sender = gamicsmail.SMTPSender{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword, From: cfg.SMTPFrom, Timeout: cfg.SMTPTimeout, RequireTLS: cfg.SMTPRequireTLS}
	}

	var mpesaProvider mpesa.Provider
	if cfg.MPesaEnabled() {
		callbackURL, joinErr := url.JoinPath(cfg.MPesaCallbackBaseURL, "v1", "payments", "callbacks", "stk", cfg.MPesaCallbackToken)
		if joinErr != nil {
			logger.Error("build M-Pesa callback URL", "error", joinErr)
			os.Exit(1)
		}
		mpesaProvider, err = mpesa.New(mpesa.Config{
			Environment: cfg.MPesaEnvironment, ConsumerKey: cfg.MPesaConsumerKey,
			ConsumerSecret: cfg.MPesaConsumerSecret, ShortCode: cfg.MPesaShortCode,
			Passkey: cfg.MPesaPasskey, CallbackURL: callbackURL,
			TransactionType: cfg.MPesaTransactionType, Timeout: cfg.MPesaTimeout,
		})
		if err != nil {
			logger.Error("configure M-Pesa", "error", err)
			os.Exit(1)
		}
	}

	var evidenceStore storage.Provider
	if cfg.StorageEnabled() {
		evidenceStore, err = storage.NewS3(storage.S3Config{
			Endpoint: cfg.StorageS3Endpoint, PublicEndpoint: cfg.StorageS3PublicEndpoint,
			Region: cfg.StorageS3Region, Bucket: cfg.StorageS3Bucket,
			AccessKey: cfg.StorageS3AccessKey, SecretKey: cfg.StorageS3SecretKey,
			SessionToken: cfg.StorageS3SessionToken, ForcePathStyle: cfg.StorageS3PathStyle,
			HTTPClient: storage.NewHTTPClient(cfg.StorageHTTPTimeout),
		})
		if err != nil {
			logger.Error("configure evidence storage", "error", err)
			os.Exit(1)
		}
	}

	server := httpapi.New(cfg, logger, version, httpapi.Dependencies{
		Database: db, Redis: securityRedis, CacheRedis: cacheRedis, Mailer: sender, MPesa: mpesaProvider,
		EvidenceStore: evidenceStore,
	})
	if err := server.Run(ctx); err != nil {
		logger.Error("api stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}
