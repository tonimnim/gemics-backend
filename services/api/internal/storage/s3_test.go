package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestS3PresignPutRequiresProviderVerifiedChecksum(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	client, err := NewS3(S3Config{
		Endpoint: "https://objects.example.com", Region: "af-south-1", Bucket: "gamics-evidence",
		AccessKey: "access", SecretKey: "secret", ForcePathStyle: true, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("result image"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	intent, err := client.PresignPut(context.Background(), "evidence/2026/08/object.bin", "image/jpeg", checksum, 12, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(intent.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("X-Amz-Signature") == "" || parsed.Query().Get("X-Amz-Expires") != "600" {
		t.Fatalf("missing presign fields: %s", intent.URL)
	}
	if !strings.Contains(parsed.Query().Get("X-Amz-SignedHeaders"), "x-amz-checksum-sha256") {
		t.Fatalf("checksum header is not signed: %s", parsed.Query().Get("X-Amz-SignedHeaders"))
	}
	if intent.RequiredHeaders["x-amz-checksum-sha256"] != checksum || intent.RequiredHeaders["Content-Type"] != "image/jpeg" {
		t.Fatalf("unexpected required headers: %#v", intent.RequiredHeaders)
	}
}

func TestS3StatReadsProviderChecksum(t *testing.T) {
	digest := sha256.Sum256([]byte("result image"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/gamics-evidence/evidence/object.bin" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("x-amz-checksum-mode") != "ENABLED" {
			t.Fatalf("request was not signed for checksum retrieval: %#v", r.Header)
		}
		w.Header().Set("Content-Length", "1234")
		w.Header().Set("x-amz-checksum-sha256", checksum)
		w.Header().Set("ETag", `"etag-value"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewS3(S3Config{
		Endpoint: server.URL, Region: "af-south-1", Bucket: "gamics-evidence", AccessKey: "access", SecretKey: "secret",
		ForcePathStyle: true, HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Stat(context.Background(), "evidence/object.bin")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 1234 || !EqualChecksum(info.ChecksumSHA256, checksum) || info.ETag != "etag-value" {
		t.Fatalf("unexpected object info: %#v", info)
	}
}

func TestS3UsesPublicHostForPresignsAndInternalHostForStat(t *testing.T) {
	digest := sha256.Sum256([]byte("result image"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	var internalHost string
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.Host != internalHost || r.URL.Path != "/gamics-evidence/evidence/object.jpg" {
			t.Fatalf("unexpected internal request: method=%s host=%s", r.Method, r.Host)
		}
		w.Header().Set("Content-Length", "12")
		w.Header().Set("x-amz-checksum-sha256", checksum)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()
	internalHost = strings.TrimPrefix(internal.URL, "http://")

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client, err := NewS3(S3Config{
		Endpoint: internal.URL, PublicEndpoint: "http://192.0.2.25:9000", Region: "af-south-1",
		Bucket: "gamics-evidence", AccessKey: "local-access", SecretKey: "do-not-leak-this-secret",
		ForcePathStyle: true, HTTPClient: internal.Client(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	put, err := client.PresignPut(context.Background(), "evidence/object.jpg", "image/jpeg", checksum, 12, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	get, err := client.PresignGet(context.Background(), "evidence/object.jpg", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{put.URL, get.URL} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if parsed.Host != "192.0.2.25:9000" || parsed.Path != "/gamics-evidence/evidence/object.jpg" {
			t.Fatalf("presign was not routed to the public origin: %s", raw)
		}
		if strings.Contains(raw, "do-not-leak-this-secret") {
			t.Fatal("secret key leaked into a presigned URL")
		}
	}
	if _, err = client.Stat(context.Background(), "evidence/object.jpg"); err != nil {
		t.Fatal(err)
	}
}

func TestS3PublicEndpointDefaultsToInternalAndChangesHostBoundSignature(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("result image"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	base := S3Config{
		Endpoint: "http://minio:9000", Region: "af-south-1", Bucket: "gamics-evidence",
		AccessKey: "access", SecretKey: "secret", ForcePathStyle: true, Now: func() time.Time { return now },
	}
	internalOnly, err := NewS3(base)
	if err != nil {
		t.Fatal(err)
	}
	internalIntent, err := internalOnly.PresignPut(context.Background(), "evidence/object.jpg", "image/jpeg", checksum, 12, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	internalURL, _ := url.Parse(internalIntent.URL)
	if internalURL.Host != "minio:9000" {
		t.Fatalf("blank public endpoint did not default to internal: %s", internalIntent.URL)
	}

	base.PublicEndpoint = "http://localhost:9000"
	publicClient, err := NewS3(base)
	if err != nil {
		t.Fatal(err)
	}
	publicIntent, err := publicClient.PresignPut(context.Background(), "evidence/object.jpg", "image/jpeg", checksum, 12, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	publicURL, _ := url.Parse(publicIntent.URL)
	if publicURL.Host != "localhost:9000" {
		t.Fatalf("public host was ignored: %s", publicIntent.URL)
	}
	if internalURL.Query().Get("X-Amz-Signature") == publicURL.Query().Get("X-Amz-Signature") {
		t.Fatal("changing the signed Host did not change the signature")
	}
}

func TestS3RejectsUnsafePublicEndpoints(t *testing.T) {
	base := S3Config{
		Endpoint: "https://objects.internal", Region: "af-south-1", Bucket: "gamics-evidence",
		AccessKey: "access", SecretKey: "secret", ForcePathStyle: true,
	}
	for _, endpoint := range []string{
		"http://user:password@localhost:9000",
		"http://localhost:9000/storage",
		"http://localhost:9000?secret=value",
		"ftp://localhost:9000",
	} {
		base.PublicEndpoint = endpoint
		if _, err := NewS3(base); err == nil {
			t.Fatalf("unsafe public endpoint accepted: %s", endpoint)
		}
	}
}

func TestS3PresignGetUsesShortLivedHostOnlySignature(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	client, err := NewS3(S3Config{
		Endpoint: "https://objects.example.com", Region: "af-south-1", Bucket: "gamics-evidence",
		AccessKey: "access", SecretKey: "secret", ForcePathStyle: true, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := client.PresignGet(context.Background(), "evidence/2026/08/result.jpg", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(intent.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("X-Amz-Signature") == "" || parsed.Query().Get("X-Amz-Expires") != "300" ||
		parsed.Query().Get("X-Amz-SignedHeaders") != "host" || !intent.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("unexpected download intent: %#v", intent)
	}
}

func TestHexToBase64SHA256RejectsWrongLength(t *testing.T) {
	if _, err := HexToBase64SHA256("abcd"); err == nil {
		t.Fatal("short checksum accepted")
	}
}
