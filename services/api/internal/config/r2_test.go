package config

import "testing"

func r2TestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "r2")
	t.Setenv("R2_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_BUCKET", "gamics-evidence-test")
	t.Setenv("R2_ACCESS_KEY_ID", "bucket-only-key")
	t.Setenv("R2_SECRET_ACCESS_KEY", "bucket-only-secret")
	t.Setenv("R2_JURISDICTION", "")
	t.Setenv("DATABASE_WRITE_URL", "postgres://writer.example/gamics")
}

func TestR2ConfigurationOverridesLocalStorageSettings(t *testing.T) {
	r2TestEnvironment(t)
	t.Setenv("STORAGE_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("STORAGE_S3_PUBLIC_ENDPOINT", "http://localhost:9000")
	t.Setenv("STORAGE_S3_REGION", "af-south-1")
	t.Setenv("STORAGE_S3_PATH_STYLE", "false")
	t.Setenv("STORAGE_S3_SESSION_TOKEN", "stale-aws-session")
	for _, load := range []func() (Config, error){Load, LoadEvidenceWorker, LoadR2Storage} {
		cfg, err := load()
		if err != nil {
			t.Fatal(err)
		}
		want := "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
		if !cfg.StorageEnabled() || cfg.StorageS3Endpoint != want || cfg.StorageS3PublicEndpoint != want || cfg.StorageS3Region != "auto" || !cfg.StorageS3PathStyle || cfg.StorageS3SessionToken != "" {
			t.Fatalf("incorrect R2 routing: endpoint=%s region=%s", cfg.StorageS3Endpoint, cfg.StorageS3Region)
		}
		if cfg.StorageS3Bucket != "gamics-evidence-test" || cfg.StorageS3AccessKey != "bucket-only-key" || cfg.StorageS3SecretKey != "bucket-only-secret" {
			t.Fatal("R2 credentials not selected")
		}
	}
}

func TestR2RequiresOwnCredentialsAndValidAccount(t *testing.T) {
	r2TestEnvironment(t)
	for _, name := range []string{"R2_ACCOUNT_ID", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "")
			if _, err := Load(); err == nil {
				t.Fatal("missing R2 setting accepted")
			}
		})
	}
	for _, account := range []string{"account", "https://example.test", "0123456789abcdef0123456789abcdef.evil.test"} {
		t.Setenv("R2_ACCOUNT_ID", account)
		if _, err := Load(); err == nil {
			t.Fatal("invalid account accepted")
		}
	}
}

func TestR2JurisdictionEndpoint(t *testing.T) {
	r2TestEnvironment(t)
	t.Setenv("R2_JURISDICTION", "eu")
	cfg, err := LoadR2Storage()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageS3Endpoint != "https://0123456789abcdef0123456789abcdef.eu.r2.cloudflarestorage.com" {
		t.Fatal("jurisdiction ignored")
	}
	t.Setenv("R2_JURISDICTION", "unexpected")
	if _, err = LoadR2Storage(); err == nil {
		t.Fatal("unsupported jurisdiction accepted")
	}
}
