package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	DatabaseURL       string
	AllowedOrigins    []string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	RequestTimeout    time.Duration
	LogLevel          slog.Level
}

func Load() (Config, error) {
	shutdownTimeout, err := duration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	requestTimeout, err := duration("REQUEST_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:       value("APP_ENV", "development"),
		HTTPAddr:          value("HTTP_ADDR", ":8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AllowedOrigins:    csv(value("CORS_ALLOWED_ORIGINS", "http://localhost:3000")),
		ShutdownTimeout:   shutdownTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		RequestTimeout:    requestTimeout,
		LogLevel:          slog.LevelInfo,
	}

	if cfg.Environment == "production" && cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required in production")
	}

	return cfg, nil
}

func value(key, fallback string) string {
	if result := strings.TrimSpace(os.Getenv(key)); result != "" {
		return result
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	result, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func csv(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}
