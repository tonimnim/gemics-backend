package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	DatabaseWriteURL  string
	DatabaseReadURL   string
	DatabaseWriteMax  int32
	DatabaseReadMax   int32
	RedisURL          string
	AllowedOrigins    []string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	RequestTimeout    time.Duration
	AccessTokenSecret string
	OTPHashSecret     string
	AccessTokenTTL    time.Duration
	OTPTTL            time.Duration
	OTPMaxAttempts    int
	OTPRequestWindow  time.Duration
	OTPEmailLimit     int
	OTPIPLimit        int
	EmailMode         string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
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

	accessTokenTTL, err := duration("ACCESS_TOKEN_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	otpTTL, err := duration("OTP_TTL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	otpRequestWindow, err := duration("OTP_REQUEST_WINDOW", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	writeURL := value("DATABASE_WRITE_URL", os.Getenv("DATABASE_URL"))
	readURL := value("DATABASE_READ_URL", writeURL)

	cfg := Config{
		Environment:       value("APP_ENV", "development"),
		HTTPAddr:          value("HTTP_ADDR", ":8080"),
		DatabaseWriteURL:  writeURL,
		DatabaseReadURL:   readURL,
		DatabaseWriteMax:  int32(integer("DATABASE_WRITE_MAX_CONNS", 20)),
		DatabaseReadMax:   int32(integer("DATABASE_READ_MAX_CONNS", 40)),
		RedisURL:          value("REDIS_URL", "redis://localhost:6379/0"),
		AllowedOrigins:    csv(value("CORS_ALLOWED_ORIGINS", "http://localhost:3000")),
		ShutdownTimeout:   shutdownTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		RequestTimeout:    requestTimeout,
		AccessTokenSecret: value("AUTH_TOKEN_SECRET", "development-only-auth-token-secret-change-me"),
		OTPHashSecret:     value("OTP_HASH_SECRET", "development-only-otp-hash-secret-change-me"),
		AccessTokenTTL:    accessTokenTTL,
		OTPTTL:            otpTTL,
		OTPMaxAttempts:    integer("OTP_MAX_ATTEMPTS", 5),
		OTPRequestWindow:  otpRequestWindow,
		OTPEmailLimit:     integer("OTP_EMAIL_LIMIT", 5),
		OTPIPLimit:        integer("OTP_IP_LIMIT", 20),
		EmailMode:         strings.ToLower(value("EMAIL_MODE", "log")),
		SMTPHost:          strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:          integer("SMTP_PORT", 587),
		SMTPUsername:      strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:      os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:          strings.TrimSpace(os.Getenv("SMTP_FROM")),
		LogLevel:          slog.LevelInfo,
	}

	if cfg.Environment == "production" {
		if cfg.DatabaseWriteURL == "" || cfg.DatabaseReadURL == "" {
			return Config{}, fmt.Errorf("DATABASE_WRITE_URL and DATABASE_READ_URL are required in production")
		}
		if len(cfg.AccessTokenSecret) < 32 || len(cfg.OTPHashSecret) < 32 ||
			strings.HasPrefix(cfg.AccessTokenSecret, "development-only-") || strings.HasPrefix(cfg.OTPHashSecret, "development-only-") {
			return Config{}, fmt.Errorf("AUTH_TOKEN_SECRET and OTP_HASH_SECRET must be at least 32 characters")
		}
		if cfg.EmailMode != "smtp" || cfg.SMTPHost == "" || cfg.SMTPFrom == "" {
			return Config{}, fmt.Errorf("production requires EMAIL_MODE=smtp, SMTP_HOST and SMTP_FROM")
		}
	}
	if cfg.EmailMode != "log" && cfg.EmailMode != "smtp" {
		return Config{}, fmt.Errorf("EMAIL_MODE must be log or smtp")
	}
	if cfg.OTPMaxAttempts < 1 || cfg.OTPEmailLimit < 1 || cfg.OTPIPLimit < 1 {
		return Config{}, fmt.Errorf("OTP limits must be positive")
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

func integer(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	result, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return result
}
