package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestProductionDefaultsToExternalMigrationJob(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("GOOGLE_CLIENT_IDS", "android-client.apps.googleusercontent.com")
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

// Safaricom drops callbacks to URLs containing these words, in any case, so
// startup refuses them in the origin and in the random token alike.
func TestMPesaCallbackRejectsDarajaBlockedWords(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("MPESA_ENVIRONMENT", "sandbox")
	t.Setenv("MPESA_CONSUMER_KEY", "key")
	t.Setenv("MPESA_CONSUMER_SECRET", "secret")
	t.Setenv("MPESA_SHORT_CODE", "174379")
	t.Setenv("MPESA_PASSKEY", "passkey")
	for _, tc := range []struct{ origin, token string }{
		{"https://mpesa.gamics.example", "12345678901234567890123456789012"},
		{"https://api.m-pesa-gamics.example", "12345678901234567890123456789012"},
		{"https://SAFARICOM-hooks.example", "12345678901234567890123456789012"},
		{"https://api.gamics.example", "1234567890123456789012345678EXEC"},
		{"https://api.gamics.example", "12345678901234567890123456789sQl"},
		{"https://api.gamics.example", "1234567890123456789012345678cmd9"},
		{"https://query.gamics.example", "12345678901234567890123456789012"},
	} {
		t.Setenv("MPESA_CALLBACK_BASE_URL", tc.origin)
		t.Setenv("MPESA_CALLBACK_TOKEN", tc.token)
		if _, err := Load(); err == nil {
			t.Errorf("callback %s/%s was accepted", tc.origin, tc.token)
		}
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

func TestStoragePublicEndpointDefaultsToInternalOrigin(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("STORAGE_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("STORAGE_S3_PUBLIC_ENDPOINT", "")
	t.Setenv("STORAGE_S3_REGION", "af-south-1")
	t.Setenv("STORAGE_S3_BUCKET", "gamics-evidence")
	t.Setenv("STORAGE_S3_ACCESS_KEY", "access")
	t.Setenv("STORAGE_S3_SECRET_KEY", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageS3PublicEndpoint != cfg.StorageS3Endpoint || cfg.StorageS3PublicEndpoint != "http://minio:9000" {
		t.Fatalf("public endpoint = %q, internal = %q", cfg.StorageS3PublicEndpoint, cfg.StorageS3Endpoint)
	}

	t.Setenv("STORAGE_S3_PUBLIC_ENDPOINT", "http://localhost:9000")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageS3PublicEndpoint != "http://localhost:9000" {
		t.Fatalf("explicit public endpoint = %q", cfg.StorageS3PublicEndpoint)
	}
}

func TestStorageRejectsUnsafePublicEndpoint(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("STORAGE_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("STORAGE_S3_REGION", "af-south-1")
	t.Setenv("STORAGE_S3_BUCKET", "gamics-evidence")
	t.Setenv("STORAGE_S3_ACCESS_KEY", "access")
	t.Setenv("STORAGE_S3_SECRET_KEY", "secret")
	for _, endpoint := range []string{
		"http://user:password@localhost:9000",
		"http://localhost:9000/storage",
		"http://localhost:9000?secret=value",
	} {
		t.Setenv("STORAGE_S3_PUBLIC_ENDPOINT", endpoint)
		if _, err := Load(); err == nil {
			t.Fatalf("unsafe public endpoint accepted: %s", endpoint)
		}
	}
}

func TestProductionStorageRequiresHTTPS(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("GOOGLE_CLIENT_IDS", "android-client.apps.googleusercontent.com")
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
	t.Setenv("STORAGE_S3_PUBLIC_ENDPOINT", "http://objects.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted a plaintext public presign origin")
	}
}

func TestPushIsDisabledWithoutFCMCredentials(t *testing.T) {
	t.Setenv("FCM_CREDENTIALS_JSON", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushEnabled() {
		t.Fatal("push delivery must stay disabled until a Firebase service account is configured")
	}
	if cfg.NotificationPushBatch != 100 || cfg.FCMConcurrency != 8 || cfg.FCMTimeout != 10*time.Second ||
		cfg.FCMAPIURL != "https://fcm.googleapis.com" {
		t.Fatalf("push defaults = batch %d concurrency %d timeout %s url %s", cfg.NotificationPushBatch,
			cfg.FCMConcurrency, cfg.FCMTimeout, cfg.FCMAPIURL)
	}
}

// testFCMServiceAccount returns a service account JSON file with a fresh key.
func testFCMServiceAccount(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	file := map[string]any{
		"type": "service_account", "project_id": "ozone-test", "private_key_id": "key-1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "firebase-adminsdk@ozone-test.iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	}
	if mutate != nil {
		mutate(file)
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestFCMCredentialsAreReadAsJSONOrBase64(t *testing.T) {
	account := testFCMServiceAccount(t, nil)
	for name, raw := range map[string]string{"json": account, "base64": base64.StdEncoding.EncodeToString([]byte(account))} {
		t.Setenv("FCM_CREDENTIALS_JSON", raw)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s credentials refused: %v", name, err)
		}
		if !cfg.PushEnabled() || cfg.FCM.ProjectID != "ozone-test" || cfg.FCM.PrivateKey == nil ||
			cfg.FCM.ClientEmail != "firebase-adminsdk@ozone-test.iam.gserviceaccount.com" {
			t.Fatalf("%s credentials = %+v", name, cfg.FCM)
		}
	}
	t.Setenv("FCM_PROJECT_ID", "ozone-other")
	if cfg, err := Load(); err != nil || cfg.FCM.ProjectID != "ozone-other" {
		t.Fatalf("FCM_PROJECT_ID override = %v, %v", cfg.FCM, err)
	}
	t.Setenv("FCM_PROJECT_ID", "")
	for name, raw := range map[string]string{
		"not json":     "{not json",
		"not base64":   "%%%",
		"user account": testFCMServiceAccount(t, func(file map[string]any) { file["type"] = "authorized_user" }),
		"no key":       testFCMServiceAccount(t, func(file map[string]any) { file["private_key"] = "" }),
		"http token":   testFCMServiceAccount(t, func(file map[string]any) { file["token_uri"] = "http://oauth.example.com/token" }),
		"no project":   testFCMServiceAccount(t, func(file map[string]any) { file["project_id"] = "" }),
	} {
		t.Setenv("FCM_CREDENTIALS_JSON", raw)
		_, err := Load()
		if err == nil {
			t.Fatalf("%s credentials accepted", name)
		}
		if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Fatalf("%s error quotes the key: %v", name, err)
		}
	}
}

func TestNotificationWorkerRejectsUnsafeConfiguration(t *testing.T) {
	t.Setenv("NOTIFICATION_PUSH_BATCH", "101")
	if _, err := Load(); err == nil {
		t.Fatal("push batches above 100 must be rejected")
	}
	t.Setenv("NOTIFICATION_PUSH_BATCH", "100")
	for _, concurrency := range []string{"0", "65"} {
		t.Setenv("FCM_CONCURRENCY", concurrency)
		if _, err := Load(); err == nil {
			t.Fatalf("FCM_CONCURRENCY=%s accepted", concurrency)
		}
	}
	t.Setenv("FCM_CONCURRENCY", "8")
	t.Setenv("FCM_CREDENTIALS_JSON", testFCMServiceAccount(t, nil))
	t.Setenv("FCM_API_URL", "http://fcm.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("enabled FCM delivery must reject plaintext HTTP")
	}
}

func TestStrikeBanThresholdIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  int
		valid bool
	}{
		{name: "default", raw: "", want: 3, valid: true},
		{name: "zero disables the ban", raw: "0", want: 0, valid: true},
		{name: "upper bound", raw: "20", want: 20, valid: true},
		{name: "surrounding space", raw: " 5 ", want: 5, valid: true},
		{name: "negative", raw: "-1"},
		{name: "above the bound", raw: "21"},
		{name: "not a number", raw: "three"},
		{name: "fraction", raw: "2.5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("RESULT_STRIKE_BAN_THRESHOLD", test.raw)
			cfg, err := Load()
			if !test.valid {
				if err == nil || !strings.Contains(err.Error(), "RESULT_STRIKE_BAN_THRESHOLD") {
					t.Fatalf("RESULT_STRIKE_BAN_THRESHOLD=%q was not rejected by name: %v", test.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.StrikeBanThreshold != test.want {
				t.Fatalf("StrikeBanThreshold = %d, want %d", cfg.StrikeBanThreshold, test.want)
			}
		})
	}
}

func TestAvatarUploadBudgetIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		raw       string
		wantLimit int
		wantBytes int64
		valid     bool
	}{
		{name: "defaults", key: "AVATAR_UPLOAD_LIMIT", raw: "", wantLimit: 10, wantBytes: 64 << 20, valid: true},
		{name: "lowest limit", key: "AVATAR_UPLOAD_LIMIT", raw: "1", wantLimit: 1, wantBytes: 64 << 20, valid: true},
		{name: "highest limit", key: "AVATAR_UPLOAD_LIMIT", raw: "100", wantLimit: 100, wantBytes: 64 << 20, valid: true},
		{name: "zero limit", key: "AVATAR_UPLOAD_LIMIT", raw: "0"},
		{name: "limit above the bound", key: "AVATAR_UPLOAD_LIMIT", raw: "101"},
		{name: "limit not a number", key: "AVATAR_UPLOAD_LIMIT", raw: "ten"},
		{name: "one maximum-size avatar", key: "AVATAR_DAILY_BYTES", raw: "10485760", wantLimit: 10, wantBytes: 10 << 20, valid: true},
		{name: "highest byte budget", key: "AVATAR_DAILY_BYTES", raw: "1073741824", wantLimit: 10, wantBytes: 1 << 30, valid: true},
		{name: "below one avatar", key: "AVATAR_DAILY_BYTES", raw: "10485759"},
		{name: "above the byte bound", key: "AVATAR_DAILY_BYTES", raw: "1073741825"},
		{name: "byte unit suffix", key: "AVATAR_DAILY_BYTES", raw: "64MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AVATAR_UPLOAD_LIMIT", "")
			t.Setenv("AVATAR_DAILY_BYTES", "")
			t.Setenv(test.key, test.raw)
			cfg, err := Load()
			if !test.valid {
				if err == nil || !strings.Contains(err.Error(), test.key) {
					t.Fatalf("%s=%q was not rejected by name: %v", test.key, test.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AvatarUploadLimit != test.wantLimit || cfg.AvatarDailyBytes != test.wantBytes {
				t.Fatalf("avatar budget = %d intents, %d bytes; want %d, %d",
					cfg.AvatarUploadLimit, cfg.AvatarDailyBytes, test.wantLimit, test.wantBytes)
			}
		})
	}
}

func TestVisionSettingsRejectUnsafeConfiguration(t *testing.T) {
	const token = "reader-token-0123456789-0123456789"
	const training = "training-token-0123456789-0123456789"
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"internal service over http", map[string]string{"VISION_URL": "http://vision:8000", "VISION_TOKEN": token}, true},
		{"private address over http", map[string]string{"VISION_URL": "http://10.0.3.7:8000", "VISION_TOKEN": token}, true},
		{"public host over https", map[string]string{"VISION_URL": "https://reader.example.com", "VISION_TOKEN": token}, true},
		{"public host over http", map[string]string{"VISION_URL": "http://reader.example.com", "VISION_TOKEN": token}, false},
		{"missing token", map[string]string{"VISION_URL": "http://vision:8000"}, false},
		{"short token", map[string]string{"VISION_URL": "http://vision:8000", "VISION_TOKEN": "short-token"}, false},
		{"training token reused", map[string]string{"VISION_TOKEN": token, "VISION_TRAINING_TOKEN": token}, false},
		{"short training token", map[string]string{"VISION_TRAINING_TOKEN": "too-short"}, false},
		{"bad training network", map[string]string{"VISION_TRAINING_TOKEN": training, "VISION_TRAINING_CIDRS": "10.0.0.0/33"}, false},
		{"training feed alone", map[string]string{"VISION_TRAINING_TOKEN": training, "VISION_TRAINING_CIDRS": "10.0.0.0/8"}, true},
		{"confidence too low", map[string]string{"VISION_AUTO_MIN_CONFIDENCE": "0.3"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			for _, key := range []string{"VISION_URL", "VISION_TOKEN", "VISION_TRAINING_TOKEN", "VISION_TRAINING_CIDRS",
				"VISION_AUTO_MIN_CONFIDENCE"} {
				t.Setenv(key, "")
			}
			for key, value := range testCase.env {
				t.Setenv(key, value)
			}
			cfg, err := Load()
			if (err == nil) != testCase.ok {
				t.Fatalf("Load() error = %v, want ok=%v", err, testCase.ok)
			}
			if err == nil && !cfg.VisionWorker {
				t.Fatal("the reading worker should be on by default")
			}
		})
	}
}

func TestProductionSignUpIsGoogleOrAppleOnly(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_WRITE_URL", "postgres://writer.example/gamics")
	t.Setenv("DATABASE_READ_URL", "postgres://reader.example/gamics")
	t.Setenv("AUTH_TOKEN_SECRET", "12345678901234567890123456789012")
	t.Setenv("OTP_HASH_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("EMAIL_MODE", "smtp")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "login@example.com")
	t.Setenv("MPESA_ENVIRONMENT", "disabled")
	t.Setenv("GOOGLE_CLIENT_IDS", "")
	t.Setenv("APPLE_CLIENT_IDS", "")
	if _, err := Load(); err == nil {
		t.Fatal("production started with no Google or Apple client IDs")
	}
	t.Setenv("APPLE_CLIENT_IDS", "io.tonits.app")
	config, err := Load()
	if err != nil || config.PasswordRegistration || len(config.AppleClientIDs) != 1 {
		t.Fatalf("production with Apple only = %+v, %v", config.AppleClientIDs, err)
	}
	t.Setenv("PASSWORD_REGISTRATION", "true")
	if _, err := Load(); err == nil {
		t.Fatal("production allowed password sign-up")
	}
}
