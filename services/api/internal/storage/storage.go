package storage

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"
)

var (
	ErrNotFound       = errors.New("storage object not found")
	ErrChecksumAbsent = errors.New("storage provider did not return a verified checksum")
	ErrObjectTooLarge = errors.New("storage object exceeds byte limit")
)

// StatusError reports an unexpected provider status. It deliberately carries
// neither the response body nor the request URL: provider error documents echo
// bucket and object names, and signed URLs carry credentials.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	return "storage " + e.Op + " returned HTTP " + strconv.Itoa(e.StatusCode)
}

// TransportError reports a failed provider round trip. The wrapped *url.Error
// is dropped because it names the private endpoint, bucket and object key.
type TransportError struct {
	Op      string
	Timeout bool
}

func (e *TransportError) Error() string {
	if e.Timeout {
		return "storage " + e.Op + " timed out"
	}
	return "storage " + e.Op + " transport failure"
}

func newTransportError(op string, err error) *TransportError {
	var network net.Error
	timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout())
	return &TransportError{Op: op, Timeout: timeout}
}

type UploadIntent struct {
	URL             string
	RequiredHeaders map[string]string
	ExpiresAt       time.Time
}

type DownloadIntent struct {
	URL       string
	ExpiresAt time.Time
}

type ObjectInfo struct {
	Size           int64
	MediaType      string
	ChecksumSHA256 string
	ETag           string
}

// Provider exposes the minimum object-storage surface used by evidence uploads.
// Implementations must make the provider validate the body checksum during PUT;
// trusting client-provided metadata alone is not sufficient.
type Provider interface {
	PresignPut(ctx context.Context, objectKey, mediaType, checksumSHA256 string, byteSize int64, expires time.Duration) (UploadIntent, error)
	PresignGet(ctx context.Context, objectKey string, expires time.Duration) (DownloadIntent, error)
	Stat(ctx context.Context, objectKey string) (ObjectInfo, error)
}
