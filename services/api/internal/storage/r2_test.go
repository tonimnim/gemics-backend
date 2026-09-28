package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestR2PresignsUsePrivateAPIEndpointAndRetainUploadConstraints(t *testing.T) {
	t.Setenv("R2_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_BUCKET", "gamics-evidence")
	t.Setenv("R2_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("R2_SECRET_ACCESS_KEY", "test-secret-never-exposed")
	t.Setenv("R2_JURISDICTION", "")
	cfg, err := config.LoadR2Storage()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewS3(S3Config{Endpoint: cfg.StorageS3Endpoint, PublicEndpoint: cfg.StorageS3PublicEndpoint, Region: cfg.StorageS3Region, Bucket: cfg.StorageS3Bucket, AccessKey: cfg.StorageS3AccessKey, SecretKey: cfg.StorageS3SecretKey, ForcePathStyle: cfg.StorageS3PathStyle})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("image"))
	intent, err := s.PresignPut(context.Background(), "evidence/test.png", "image/png", base64.StdEncoding.EncodeToString(digest[:]), 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(intent.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com" || u.Path != "/gamics-evidence/evidence/test.png" {
		t.Fatal("incorrect private R2 endpoint")
	}
	if !strings.Contains(u.Query().Get("X-Amz-Credential"), "/auto/s3/aws4_request") {
		t.Fatal("wrong R2 signing region")
	}
	if u.Query().Get("X-Amz-SignedHeaders") != "content-length;content-type;host;if-none-match;x-amz-checksum-sha256" {
		t.Fatal("upload constraints weakened for R2")
	}
	if strings.Contains(intent.URL, cfg.StorageS3SecretKey) {
		t.Fatal("R2 secret exposed")
	}
}
