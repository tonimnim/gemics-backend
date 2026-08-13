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
	intent, err := client.PresignPut(context.Background(), "evidence/2026/08/object.bin", "image/jpeg", checksum, 10*time.Minute)
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
