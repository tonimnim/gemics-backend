package config

import "testing"

func TestProductionDefaultsToExternalMigrationJob(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("RUN_MIGRATIONS", "")
	t.Setenv("DATABASE_WRITE_URL", "postgres://writer.example/gamics")
	t.Setenv("DATABASE_READ_URL", "postgres://reader.example/gamics")
	t.Setenv("AUTH_TOKEN_SECRET", "12345678901234567890123456789012")
	t.Setenv("OTP_HASH_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("EMAIL_MODE", "smtp")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "login@example.com")
	t.Setenv("MPESA_ENVIRONMENT", "disabled")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.RunMigrations {
		t.Fatal("production API replicas must not migrate on startup")
	}
}

func TestMPesaRequiresPublicCallbackOrigin(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("MPESA_ENVIRONMENT", "sandbox")
	t.Setenv("MPESA_CONSUMER_KEY", "key")
	t.Setenv("MPESA_CONSUMER_SECRET", "secret")
	t.Setenv("MPESA_SHORT_CODE", "174379")
	t.Setenv("MPESA_PASSKEY", "passkey")
	t.Setenv("MPESA_CALLBACK_TOKEN", "12345678901234567890123456789012")
	t.Setenv("MPESA_CALLBACK_BASE_URL", "https://localhost")
	if _, err := Load(); err == nil {
		t.Fatal("localhost callback must be rejected")
	}
	t.Setenv("MPESA_CALLBACK_BASE_URL", "https://api.gamics.example")
	if _, err := Load(); err != nil {
		t.Fatalf("public callback origin rejected: %v", err)
	}
}

func TestStorageDisabledByDefault(t *testing.T) {
	t.Setenv("STORAGE_MODE", "")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.StorageEnabled() {
		t.Fatal("evidence storage must fail closed until explicitly configured")
	}
}

func TestProductionStorageRequiresHTTPS(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_WRITE_URL", "postgres://writer.example/gamics")
	t.Setenv("DATABASE_READ_URL", "postgres://reader.example/gamics")
	t.Setenv("AUTH_TOKEN_SECRET", "12345678901234567890123456789012")
	t.Setenv("OTP_HASH_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("EMAIL_MODE", "smtp")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "login@example.com")
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("STORAGE_S3_ENDPOINT", "http://objects.internal")
	t.Setenv("STORAGE_S3_REGION", "af-south-1")
	t.Setenv("STORAGE_S3_BUCKET", "gamics-evidence")
	t.Setenv("STORAGE_S3_ACCESS_KEY", "access")
	t.Setenv("STORAGE_S3_SECRET_KEY", "secret")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted plaintext object-storage credentials")
	}
	t.Setenv("STORAGE_S3_ENDPOINT", "https://objects.example.com")
	if _, err := Load(); err != nil {
		t.Fatalf("valid S3 storage rejected: %v", err)
	}
}
