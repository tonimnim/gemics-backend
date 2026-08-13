package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("storage object not found")
	ErrChecksumAbsent = errors.New("storage provider did not return a verified checksum")
)

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
	ChecksumSHA256 string
	ETag           string
}

// Provider exposes the minimum object-storage surface used by evidence uploads.
// Implementations must make the provider validate the body checksum during PUT;
// trusting client-provided metadata alone is not sufficient.
type Provider interface {
	PresignPut(ctx context.Context, objectKey, mediaType, checksumSHA256 string, expires time.Duration) (UploadIntent, error)
	PresignGet(ctx context.Context, objectKey string, expires time.Duration) (DownloadIntent, error)
	Stat(ctx context.Context, objectKey string) (ObjectInfo, error)
}
