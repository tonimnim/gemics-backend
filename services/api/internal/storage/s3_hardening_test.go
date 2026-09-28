package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadSignatureBindsSizeAndPreventsOverwrite(t *testing.T) {
	client, err := NewS3(S3Config{Endpoint: "https://s3.example.test", Region: "af-south-1", Bucket: "evidence-test", AccessKey: "key", SecretKey: "secret", Now: func() time.Time { return time.Unix(1000000000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("body"))
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	one, err := client.PresignPut(context.Background(), "test.png", "image/png", checksum, 4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	two, err := client.PresignPut(context.Background(), "test.png", "image/png", checksum, 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	oneURL, _ := url.Parse(one.URL)
	twoURL, _ := url.Parse(two.URL)
	headers := oneURL.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(headers, "content-length;") || !strings.Contains(headers, "if-none-match;") {
		t.Fatal("constraints are not signed")
	}
	if one.RequiredHeaders["Content-Length"] != "4" || one.RequiredHeaders["If-None-Match"] != "*" {
		t.Fatal("required upload constraints missing")
	}
	if oneURL.Query().Get("X-Amz-Signature") == twoURL.Query().Get("X-Amz-Signature") {
		t.Fatal("changing size did not change signature")
	}
	if _, err = client.PresignPut(context.Background(), "test.png", "image/png", checksum, 0, time.Minute); err == nil {
		t.Fatal("unbounded upload accepted")
	}
}

func TestReadUsesInternalEndpointAndBoundsChunkedBody(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/evidence-test/test.png" || r.URL.Query().Get("X-Amz-Signature") == "" {
			t.Error("invalid signed read")
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("12345"))
	}))
	defer origin.Close()
	client, err := NewS3(S3Config{Endpoint: origin.URL, PublicEndpoint: "https://unreachable.example.test", Region: "af-south-1", Bucket: "evidence-test", AccessKey: "key", SecretKey: "secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Read(context.Background(), "test.png", 4); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("oversized body accepted: %v", err)
	}
	body, err := client.Read(context.Background(), "test.png", 5)
	if err != nil || string(body) != "12345" {
		t.Fatalf("internal read failed: %q %v", body, err)
	}
}

func TestStorageClientRefusesRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed credential-bearing redirect")
		w.WriteHeader(200)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	response, err := NewHTTPClient(time.Second).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 302 {
		t.Fatalf("unexpected response %d", response.StatusCode)
	}
}

func testS3(t *testing.T, endpoint string, client *http.Client) *S3 {
	t.Helper()
	s3, err := NewS3(S3Config{Endpoint: endpoint, Region: "af-south-1", Bucket: "evidence-test", AccessKey: "key", SecretKey: "secret", ForcePathStyle: true, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	return s3
}

// Provider error documents echo the bucket and key; none of it may surface.
func assertSanitized(t *testing.T, err error, host string) {
	t.Helper()
	for _, leaked := range []string{"evidence-test", "private-object", "AccessDenied", "X-Amz-Signature", host} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("storage error leaked %q: %v", leaked, err)
		}
	}
}

func TestStatReturnsTypedStatusWithoutBody(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code><Key>evidence/private-object.png</Key></Error>"))
	}))
	defer origin.Close()
	_, err := testS3(t, origin.URL, origin.Client()).Stat(context.Background(), "evidence/private-object.png")
	var status *StatusError
	if !errors.As(err, &status) || status.Op != "head" || status.StatusCode != http.StatusForbidden {
		t.Fatalf("untyped status error: %#v", err)
	}
	assertSanitized(t, err, strings.TrimPrefix(origin.URL, "http://"))
}

func TestReadTypedStatus(t *testing.T) {
	var statusCode atomic.Int64
	statusCode.Store(http.StatusServiceUnavailable)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(statusCode.Load()))
		_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code><Key>evidence/private-object.png</Key></Error>"))
	}))
	defer origin.Close()
	client := testS3(t, origin.URL, origin.Client())
	_, err := client.Read(context.Background(), "evidence/private-object.png", 1024)
	var status *StatusError
	if !errors.As(err, &status) || status.Op != "get" || status.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("untyped status error: %#v", err)
	}
	assertSanitized(t, err, strings.TrimPrefix(origin.URL, "http://"))
	statusCode.Store(http.StatusNotFound)
	if _, err = client.Read(context.Background(), "evidence/private-object.png", 1024); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object not reported as ErrNotFound: %v", err)
	}
}

func TestTransportErrorOmitsURL(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	client := testS3(t, closedURL, NewHTTPClient(time.Second))
	_, statErr := client.Stat(context.Background(), "evidence/private-object.png")
	_, readErr := client.Read(context.Background(), "evidence/private-object.png", 1024)
	for op, err := range map[string]error{"head": statErr, "get": readErr} {
		var transport *TransportError
		if !errors.As(err, &transport) || transport.Op != op || transport.Timeout {
			t.Fatalf("%s: untyped transport error: %#v", op, err)
		}
		assertSanitized(t, err, strings.TrimPrefix(closedURL, "http://"))
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := testS3(t, slow.URL, slow.Client()).Stat(ctx, "evidence/private-object.png")
	var transport *TransportError
	if !errors.As(err, &transport) || !transport.Timeout {
		t.Fatalf("deadline not reported as a transport timeout: %#v", err)
	}
	assertSanitized(t, err, strings.TrimPrefix(slow.URL, "http://"))
	_, err = testS3(t, slow.URL, NewHTTPClient(50*time.Millisecond)).Read(context.Background(), "evidence/private-object.png", 1024)
	if !errors.As(err, &transport) || !transport.Timeout || transport.Op != "get" {
		t.Fatalf("client timeout not reported as a transport timeout: %#v", err)
	}
}
