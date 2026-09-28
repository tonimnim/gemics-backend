package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
)

func TestErrorCodesNeverIncludeSignedURL(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{syscall.Errno(10055), "socket_10055"},
		{&net.DNSError{Err: "secret", Name: "secret"}, "dns_error"},
		{context.DeadlineExceeded, "timeout"},
		{io.EOF, "connection_eof"},
		{errors.New("signed URL secret"), "transport_error"},
	} {
		wrapped := &url.Error{Op: "Put", URL: "https://example.test/?signature=secret", Err: tc.cause}
		if got := errorCode(0, wrapped); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// Read-only diagnostic to separate the local gateway path from R2 bandwidth.
// Explicit opt-in; cannot target a remote or production service.
func TestLiveLocalGatewayBurst(t *testing.T) {
	if os.Getenv("GAMICS_TEST_LOCAL_GATEWAY") != "1" {
		t.Skip("local gateway diagnostic not enabled")
	}
	counts := probeGateway("http://127.0.0.1:8080")
	t.Logf("1000 concurrent read-only gateway probes: %v", counts)
	if counts["http_200"] != 1000 {
		t.Fatal("local gateway did not serve every first-attempt request")
	}
}

func TestAllowedAPINeverTargetsRemoteServices(t *testing.T) {
	for _, tc := range []struct {
		raw             string
		docker, allowed bool
	}{
		{"http://127.0.0.1:8080", false, true},
		{"http://gateway:8080", true, true},
		{"http://gateway:8080", false, false},
		{"http://production:8080", true, false},
		{"http://127.0.0.1:8080", true, false},
		{"http://user:secret@gateway:8080", true, false},
		{"http://gateway:8080?x=1", true, false},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if allowedAPI(u, tc.docker) != tc.allowed {
			t.Errorf("unexpected allowance for %s", tc.raw)
		}
	}
}
