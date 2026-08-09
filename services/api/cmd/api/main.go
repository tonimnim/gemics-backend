package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/httpapi"
	gamicsmail "github.com/gamics-io/gamics/services/api/internal/mail"
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
	if err := database.Migrate(startupCtx, db.Writer); err != nil {
		logger.Error("apply database migrations", "error", err)
		os.Exit(1)
	}
	if err := db.PingReader(startupCtx); err != nil {
		logger.Warn("database reader unavailable; reads will fall back to writer", "error", err)
	}

	redisClient, err := cache.Open(startupCtx, cfg.RedisURL)
	if err != nil {
		logger.Warn("redis unavailable; database rate-limit fallback enabled", "error", err)
		redisClient = nil
	} else {
		defer redisClient.Close()
	}

	var sender gamicsmail.Sender = gamicsmail.LogSender{Logger: logger}
	if cfg.EmailMode == "smtp" {
		sender = gamicsmail.SMTPSender{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom}
	}

	server := httpapi.New(cfg, logger, version, httpapi.Dependencies{Database: db, Redis: redisClient, Mailer: sender})
	if err := server.Run(ctx); err != nil {
		logger.Error("api stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}
