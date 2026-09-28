package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var r2AccountPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// LoadR2Storage also serves the live provider conformance check without requiring
// unrelated database, mail or payment configuration.
func LoadR2Storage() (Config, error) {
	cfg := Config{StorageMode: "r2"}
	err := configureR2(&cfg)
	return cfg, err
}

func configureR2(cfg *Config) error {
	if cfg.StorageMode != "r2" {
		return nil
	}
	account := strings.ToLower(strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID")))
	if !r2AccountPattern.MatchString(account) {
		return fmt.Errorf("R2_ACCOUNT_ID must be the 32-character Cloudflare account ID")
	}
	jurisdiction := strings.ToLower(strings.TrimSpace(os.Getenv("R2_JURISDICTION")))
	if jurisdiction != "" && jurisdiction != "eu" {
		return fmt.Errorf("R2_JURISDICTION must be empty (default) or eu")
	}
	host := account
	if jurisdiction != "" {
		host += "." + jurisdiction
	}
	cfg.StorageS3Endpoint = "https://" + host + ".r2.cloudflarestorage.com"
	// R2 presigns use the S3 API endpoint, never an r2.dev URL or custom CDN domain.
	cfg.StorageS3PublicEndpoint = cfg.StorageS3Endpoint
	cfg.StorageS3Region = "auto"
	cfg.StorageS3PathStyle = true
	cfg.StorageS3Bucket = strings.TrimSpace(os.Getenv("R2_BUCKET"))
	cfg.StorageS3AccessKey = strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID"))
	cfg.StorageS3SecretKey = os.Getenv("R2_SECRET_ACCESS_KEY")
	// Do not inherit a MinIO/AWS session token when switching providers.
	cfg.StorageS3SessionToken = ""
	if cfg.StorageS3Bucket == "" || cfg.StorageS3AccessKey == "" || strings.TrimSpace(cfg.StorageS3SecretKey) == "" {
		return fmt.Errorf("R2_BUCKET, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY are required for R2")
	}
	return nil
}
