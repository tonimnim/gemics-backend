package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var callbackTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
var mpesaShortCodePattern = regexp.MustCompile(`^[0-9]{5,12}$`)

type Config struct {
	Environment           string
	HTTPAddr              string
	DatabaseWriteURL      string
	DatabaseReadURL       string
	DatabaseWriteMax      int32
	DatabaseReadMax       int32
	DatabaseMaxReplicaLag time.Duration
	DatabaseFallbackMax   int
	RunMigrations         bool
	RedisURL              string
	RedisSecurityURL      string
	RedisCacheURL         string
	CacheNamespace        string
	GameCatalogCacheTTL   time.Duration
	AuthSessionCacheTTL   time.Duration
	AllowedOrigins        []string
	TrustedProxyCIDRs     []string
	ShutdownTimeout       time.Duration
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	MaxHeaderBytes        int
	RequestTimeout        time.Duration
	AccessTokenSecret     string
	OTPHashSecret         string
	AccessTokenTTL        time.Duration
	OTPTTL                time.Duration
	OTPMaxAttempts        int
	OTPRequestWindow      time.Duration
	OTPEmailLimit         int
	OTPIPLimit            int
	EmailMode             string
	SMTPHost              string
	SMTPPort              int
	SMTPUsername          string
	SMTPPassword          string
	SMTPFrom              string
	SMTPTimeout           time.Duration
	SMTPRequireTLS        bool
	MPesaEnvironment      string
	MPesaConsumerKey      string
	MPesaConsumerSecret   string
	MPesaShortCode        string
	MPesaPasskey          string
	MPesaCallbackBaseURL  string
	MPesaCallbackToken    string
	MPesaTransactionType  string
	MPesaTimeout          time.Duration
	MPesaMaxAmountMinor   int64
	MPesaUserLimit        int
	MPesaPhoneLimit       int
	MPesaIPLimit          int
	MPesaUserWindow       time.Duration
	MPesaPhoneWindow      time.Duration
	MPesaIPWindow         time.Duration
	StorageMode           string
	StorageS3Endpoint     string
	StorageS3Region       string
	StorageS3Bucket       string
	StorageS3AccessKey    string
	StorageS3SecretKey    string
	StorageS3SessionToken string
	StorageS3PathStyle    bool
	StoragePresignTTL     time.Duration
	StorageHTTPTimeout    time.Duration
	EvidenceMaxBytes      int64
	LogLevel              slog.Level
}

func Load() (Config, error) {
	environment := value("APP_ENV", "development")
	shutdownTimeout, err := duration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	requestTimeout, err := duration("REQUEST_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	readHeaderTimeout, err := duration("READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := duration("READ_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := duration("WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := duration("IDLE_TIMEOUT", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	replicaLag, err := duration("DATABASE_MAX_REPLICA_LAG", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	gameCatalogCacheTTL, err := duration("GAME_CATALOG_CACHE_TTL", 12*time.Hour)
	if err != nil {
		return Config{}, err
	}
	authSessionCacheTTL, err := duration("AUTH_SESSION_CACHE_TTL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	runMigrations, err := boolean("RUN_MIGRATIONS", environment != "production")
	if err != nil {
		return Config{}, err
	}
	smtpTimeout, err := duration("SMTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	smtpRequireTLS, err := boolean("SMTP_REQUIRE_TLS", environment == "production")
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
	mpesaTimeout, err := duration("MPESA_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	mpesaUserWindow, err := duration("MPESA_USER_WINDOW", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	mpesaPhoneWindow, err := duration("MPESA_PHONE_WINDOW", time.Hour)
	if err != nil {
		return Config{}, err
	}
	mpesaIPWindow, err := duration("MPESA_IP_WINDOW", time.Hour)
	if err != nil {
		return Config{}, err
	}
	storagePresignTTL, err := duration("STORAGE_PRESIGN_TTL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	storageHTTPTimeout, err := duration("STORAGE_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	storagePathStyle, err := boolean("STORAGE_S3_PATH_STYLE", true)
	if err != nil {
		return Config{}, err
	}
	writeURL := value("DATABASE_WRITE_URL", os.Getenv("DATABASE_URL"))
	readURL := value("DATABASE_READ_URL", writeURL)
	legacyRedisURL := value("REDIS_URL", "redis://localhost:6379/0")
	securityRedisURL := value("REDIS_SECURITY_URL", legacyRedisURL)
	cacheRedisURL := value("REDIS_CACHE_URL", securityRedisURL)

	cfg := Config{
		Environment:           environment,
		HTTPAddr:              value("HTTP_ADDR", ":8080"),
		DatabaseWriteURL:      writeURL,
		DatabaseReadURL:       readURL,
		DatabaseWriteMax:      int32(integer("DATABASE_WRITE_MAX_CONNS", 8)),
		DatabaseReadMax:       int32(integer("DATABASE_READ_MAX_CONNS", 16)),
		DatabaseMaxReplicaLag: replicaLag,
		DatabaseFallbackMax:   integer("DATABASE_FALLBACK_MAX_CONCURRENCY", 16),
		RunMigrations:         runMigrations,
		RedisURL:              legacyRedisURL,
		RedisSecurityURL:      securityRedisURL,
		RedisCacheURL:         cacheRedisURL,
		CacheNamespace:        value("CACHE_NAMESPACE", "gamics:"+environment+":v1"),
		GameCatalogCacheTTL:   gameCatalogCacheTTL,
		AuthSessionCacheTTL:   authSessionCacheTTL,
		AllowedOrigins:        csv(value("CORS_ALLOWED_ORIGINS", "http://localhost:3000")),
		TrustedProxyCIDRs:     csv(os.Getenv("TRUSTED_PROXY_CIDRS")),
		ShutdownTimeout:       shutdownTimeout,
		ReadHeaderTimeout:     readHeaderTimeout,
		ReadTimeout:           readTimeout,
		WriteTimeout:          writeTimeout,
		IdleTimeout:           idleTimeout,
		MaxHeaderBytes:        integer("MAX_HEADER_BYTES", 32<<10),
		RequestTimeout:        requestTimeout,
		AccessTokenSecret:     value("AUTH_TOKEN_SECRET", "development-only-auth-token-secret-change-me"),
		OTPHashSecret:         value("OTP_HASH_SECRET", "development-only-otp-hash-secret-change-me"),
		AccessTokenTTL:        accessTokenTTL,
		OTPTTL:                otpTTL,
		OTPMaxAttempts:        integer("OTP_MAX_ATTEMPTS", 5),
		OTPRequestWindow:      otpRequestWindow,
		OTPEmailLimit:         integer("OTP_EMAIL_LIMIT", 5),
		OTPIPLimit:            integer("OTP_IP_LIMIT", 20),
		EmailMode:             strings.ToLower(value("EMAIL_MODE", "log")),
		SMTPHost:              strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:              integer("SMTP_PORT", 587),
		SMTPUsername:          strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:          os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:              strings.TrimSpace(os.Getenv("SMTP_FROM")),
		SMTPTimeout:           smtpTimeout,
		SMTPRequireTLS:        smtpRequireTLS,
		MPesaEnvironment:      strings.ToLower(value("MPESA_ENVIRONMENT", "disabled")),
		MPesaConsumerKey:      strings.TrimSpace(os.Getenv("MPESA_CONSUMER_KEY")),
		MPesaConsumerSecret:   os.Getenv("MPESA_CONSUMER_SECRET"),
		MPesaShortCode:        strings.TrimSpace(os.Getenv("MPESA_SHORT_CODE")),
		MPesaPasskey:          os.Getenv("MPESA_PASSKEY"),
		MPesaCallbackBaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("MPESA_CALLBACK_BASE_URL")), "/"),
		MPesaCallbackToken:    strings.TrimSpace(os.Getenv("MPESA_CALLBACK_TOKEN")),
		MPesaTransactionType:  value("MPESA_TRANSACTION_TYPE", "CustomerPayBillOnline"),
		MPesaTimeout:          mpesaTimeout,
		MPesaMaxAmountMinor:   int64(integer("MPESA_MAX_AMOUNT_MINOR", 1_000_000)),
		MPesaUserLimit:        integer("MPESA_USER_LIMIT", 5),
		MPesaPhoneLimit:       integer("MPESA_PHONE_LIMIT", 8),
		MPesaIPLimit:          integer("MPESA_IP_LIMIT", 20),
		MPesaUserWindow:       mpesaUserWindow,
		MPesaPhoneWindow:      mpesaPhoneWindow,
		MPesaIPWindow:         mpesaIPWindow,
		StorageMode:           strings.ToLower(value("STORAGE_MODE", "disabled")),
		StorageS3Endpoint:     strings.TrimRight(strings.TrimSpace(os.Getenv("STORAGE_S3_ENDPOINT")), "/"),
		StorageS3Region:       strings.TrimSpace(os.Getenv("STORAGE_S3_REGION")),
		StorageS3Bucket:       strings.TrimSpace(os.Getenv("STORAGE_S3_BUCKET")),
		StorageS3AccessKey:    strings.TrimSpace(os.Getenv("STORAGE_S3_ACCESS_KEY")),
		StorageS3SecretKey:    os.Getenv("STORAGE_S3_SECRET_KEY"),
		StorageS3SessionToken: os.Getenv("STORAGE_S3_SESSION_TOKEN"),
		StorageS3PathStyle:    storagePathStyle,
		StoragePresignTTL:     storagePresignTTL,
		StorageHTTPTimeout:    storageHTTPTimeout,
		EvidenceMaxBytes:      int64(integer("EVIDENCE_MAX_BYTES", 10<<20)),
		LogLevel:              slog.LevelInfo,
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
	if cfg.MPesaEnvironment != "disabled" && cfg.MPesaEnvironment != "sandbox" && cfg.MPesaEnvironment != "production" {
		return Config{}, fmt.Errorf("MPESA_ENVIRONMENT must be disabled, sandbox or production")
	}
	if cfg.MPesaEnvironment != "disabled" {
		if cfg.MPesaConsumerKey == "" || cfg.MPesaConsumerSecret == "" || cfg.MPesaShortCode == "" || cfg.MPesaPasskey == "" {
			return Config{}, fmt.Errorf("enabled M-Pesa requires consumer credentials, shortcode and passkey")
		}
		if !mpesaShortCodePattern.MatchString(cfg.MPesaShortCode) {
			return Config{}, fmt.Errorf("MPESA_SHORT_CODE must contain 5-12 digits")
		}
		if !callbackTokenPattern.MatchString(cfg.MPesaCallbackToken) {
			return Config{}, fmt.Errorf("MPESA_CALLBACK_TOKEN must be 32-128 URL-safe characters")
		}
		callbackURL, err := url.ParseRequestURI(cfg.MPesaCallbackBaseURL)
		if err != nil || callbackURL.Scheme != "https" || callbackURL.Host == "" || callbackURL.User != nil || callbackURL.RawQuery != "" || callbackURL.Fragment != "" || (callbackURL.Path != "" && callbackURL.Path != "/") || !publicCallbackHost(callbackURL.Hostname()) {
			return Config{}, fmt.Errorf("MPESA_CALLBACK_BASE_URL must be a public HTTPS origin without a path")
		}
		if cfg.MPesaTransactionType != "CustomerPayBillOnline" && cfg.MPesaTransactionType != "CustomerBuyGoodsOnline" {
			return Config{}, fmt.Errorf("unsupported MPESA_TRANSACTION_TYPE")
		}
	}
	if cfg.StorageMode != "disabled" && cfg.StorageMode != "s3" {
		return Config{}, fmt.Errorf("STORAGE_MODE must be disabled or s3")
	}
	if cfg.StorageMode == "s3" {
		if cfg.StorageS3Endpoint == "" || cfg.StorageS3Region == "" || cfg.StorageS3Bucket == "" || cfg.StorageS3AccessKey == "" || cfg.StorageS3SecretKey == "" {
			return Config{}, fmt.Errorf("enabled S3 storage requires endpoint, region, bucket and credentials")
		}
		storageURL, parseErr := url.ParseRequestURI(cfg.StorageS3Endpoint)
		if parseErr != nil || (storageURL.Scheme != "http" && storageURL.Scheme != "https") || storageURL.Host == "" || storageURL.User != nil || storageURL.RawQuery != "" || storageURL.Fragment != "" || (storageURL.Path != "" && storageURL.Path != "/") {
			return Config{}, fmt.Errorf("STORAGE_S3_ENDPOINT must be an HTTP(S) origin without credentials, query or path")
		}
		if cfg.Environment == "production" && storageURL.Scheme != "https" {
			return Config{}, fmt.Errorf("production S3 storage requires an HTTPS endpoint")
		}
	}
	if cfg.OTPMaxAttempts < 1 || cfg.OTPEmailLimit < 1 || cfg.OTPIPLimit < 1 {
		return Config{}, fmt.Errorf("OTP limits must be positive")
	}
	if cfg.MPesaMaxAmountMinor < 100 || cfg.MPesaUserLimit < 1 || cfg.MPesaPhoneLimit < 1 || cfg.MPesaIPLimit < 1 || cfg.MPesaUserWindow <= 0 || cfg.MPesaPhoneWindow <= 0 || cfg.MPesaIPWindow <= 0 {
		return Config{}, fmt.Errorf("M-Pesa amount and velocity limits must be positive")
	}
	if cfg.DatabaseWriteMax < 1 || cfg.DatabaseReadMax < 1 || cfg.DatabaseFallbackMax < 1 {
		return Config{}, fmt.Errorf("database connection and fallback limits must be positive")
	}
	if cfg.DatabaseMaxReplicaLag <= 0 || cfg.GameCatalogCacheTTL <= 0 || cfg.AuthSessionCacheTTL <= 0 {
		return Config{}, fmt.Errorf("database lag and cache durations must be positive")
	}
	if cfg.ReadHeaderTimeout <= 0 || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 || cfg.RequestTimeout <= 0 || cfg.SMTPTimeout <= 0 || cfg.MaxHeaderBytes < 8192 {
		return Config{}, fmt.Errorf("HTTP timeout values must be positive and MAX_HEADER_BYTES must be at least 8192")
	}
	if cfg.StoragePresignTTL < time.Minute || cfg.StoragePresignTTL > 15*time.Minute || cfg.StorageHTTPTimeout <= 0 || cfg.EvidenceMaxBytes < 1 || cfg.EvidenceMaxBytes > 25<<20 {
		return Config{}, fmt.Errorf("storage upload TTL, timeout or evidence size is outside the safe range")
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS contains invalid CIDR %q", cidr)
		}
	}
	if strings.ContainsAny(cfg.CacheNamespace, " \t\r\n") {
		return Config{}, fmt.Errorf("CACHE_NAMESPACE cannot contain whitespace")
	}

	return cfg, nil
}

func (c Config) MPesaEnabled() bool   { return c.MPesaEnvironment != "disabled" }
func (c Config) StorageEnabled() bool { return c.StorageMode == "s3" }

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

func boolean(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	result, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func publicCallbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") || host == "" {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.IsGlobalUnicast() && !address.IsPrivate()
	}
	return strings.Contains(host, ".")
}
