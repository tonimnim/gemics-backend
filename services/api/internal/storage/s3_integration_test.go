package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// Opt in against a dedicated, private test bucket. Creates one small object and
// leaves it for the test bucket's retention policy; no delete permission needed.
func TestS3LiveUploadConformance(t *testing.T) {
	if os.Getenv("GAMICS_TEST_STORAGE") != "1" {
		t.Skip("GAMICS_TEST_STORAGE=1 not configured")
	}
	settings := S3Config{Endpoint: os.Getenv("STORAGE_S3_ENDPOINT"), PublicEndpoint: os.Getenv("STORAGE_S3_PUBLIC_ENDPOINT"), Region: os.Getenv("STORAGE_S3_REGION"), Bucket: os.Getenv("STORAGE_S3_BUCKET"), AccessKey: os.Getenv("STORAGE_S3_ACCESS_KEY"), SecretKey: os.Getenv("STORAGE_S3_SECRET_KEY"), SessionToken: os.Getenv("STORAGE_S3_SESSION_TOKEN"), ForcePathStyle: os.Getenv("STORAGE_S3_PATH_STYLE") != "false"}
	if os.Getenv("STORAGE_MODE") == "r2" {
		cfg, err := config.LoadR2Storage()
		if err != nil {
			t.Fatal(err)
		}
		settings = S3Config{Endpoint: cfg.StorageS3Endpoint, PublicEndpoint: cfg.StorageS3PublicEndpoint, Region: cfg.StorageS3Region, Bucket: cfg.StorageS3Bucket, AccessKey: cfg.StorageS3AccessKey, SecretKey: cfg.StorageS3SecretKey, ForcePathStyle: cfg.StorageS3PathStyle}
	}
	client, err := NewS3(settings)
	if err != nil {
		t.Fatal("invalid test storage configuration")
	}
	body := []byte("gamics storage conformance " + rand.Text())
	digest := sha256.Sum256(body)
	key := "conformance/" + rand.Text() + ".bin"
	intent, err := client.PresignPut(context.Background(), key, "application/octet-stream", base64.StdEncoding.EncodeToString(digest[:]), int64(len(body)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	put := func(payload []byte) int {
		req, err := http.NewRequest("PUT", intent.URL, bytes.NewReader(payload))
		if err != nil {
			t.Fatal("build PUT")
		}
		for name, value := range intent.RequiredHeaders {
			req.Header.Set(name, value)
		}
		// Go derives Content-Length from the body; a changed size must invalidate
		// the signature rather than merely being detected later by the API.
		req.Header.Del("Content-Length")
		response, err := client.httpClient.Do(req)
		if err != nil {
			t.Fatal("storage PUT transport failed")
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return response.StatusCode
	}
	if status := put(append(append([]byte(nil), body...), 0)); status >= 200 && status < 300 {
		t.Fatal("provider ignored signed Content-Length")
	}
	bad := append([]byte(nil), body...)
	bad[0] ^= 1
	if status := put(bad); status >= 200 && status < 300 {
		t.Fatal("provider ignored SHA256")
	}
	if status := put(body); status < 200 || status >= 300 {
		t.Fatalf("valid PUT failed: HTTP %d", status)
	}
	if status := put(body); status != 412 {
		t.Fatalf("provider did not reject overwrite with 412: HTTP %d", status)
	}
	info, err := client.Stat(context.Background(), key)
	if err != nil {
		t.Fatal("provider must expose verified SHA256 on HEAD")
	}
	if info.Size != int64(len(body)) || !EqualChecksum(info.ChecksumSHA256, base64.StdEncoding.EncodeToString(digest[:])) {
		t.Fatal("provider metadata mismatch")
	}
	read, err := client.Read(context.Background(), key, int64(len(body)))
	if err != nil || !bytes.Equal(read, body) {
		t.Fatal("signed private read failed")
	}
	response, err := client.httpClient.Get(client.publicObjectURL(key).String())
	if err != nil {
		t.Fatal("anonymous-read check failed")
	}
	response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatal("test bucket allows anonymous reads")
	}

	settings.Now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	expiredClient, err := NewS3(settings)
	if err != nil {
		t.Fatal("build expired URL test client")
	}
	expired, err := expiredClient.PresignGet(context.Background(), key, time.Minute)
	if err != nil {
		t.Fatal("sign expired URL")
	}
	response, err = client.httpClient.Get(expired.URL)
	if err != nil {
		t.Fatal("expiry check transport failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expired signed URL must return 403, got %d", response.StatusCode)
	}

	if os.Getenv("STORAGE_MODE") == "r2" {
		settings.Now = nil
		settings.AccessKey = os.Getenv("R2_WORKER_ACCESS_KEY_ID")
		settings.SecretKey = os.Getenv("R2_WORKER_SECRET_ACCESS_KEY")
		worker, err := NewS3(settings)
		if err != nil {
			t.Fatal("separate R2 worker read-only credentials are required for this test")
		}
		if _, err = worker.Stat(context.Background(), key); err != nil {
			t.Fatal("worker cannot verify R2 object metadata")
		}
		got, err := worker.Read(context.Background(), key, int64(len(body)))
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("worker cannot read R2 object")
		}
		workerIntent, err := worker.PresignPut(context.Background(), "conformance/"+rand.Text()+"-worker-denied.bin", "application/octet-stream", base64.StdEncoding.EncodeToString(digest[:]), int64(len(body)), time.Minute)
		if err != nil {
			t.Fatal("cannot create permission check")
		}
		req, err := http.NewRequest("PUT", workerIntent.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal("permission check request failed")
		}
		for name, value := range workerIntent.RequiredHeaders {
			req.Header.Set(name, value)
		}
		req.Header.Del("Content-Length")
		response, err = worker.httpClient.Do(req)
		if err != nil {
			t.Fatal("worker permission check transport failed")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("worker credentials must deny PUT with 403, got %d", response.StatusCode)
		}
	}
}
