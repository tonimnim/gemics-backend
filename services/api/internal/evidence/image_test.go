package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

func fixture(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 64, 32))
	var err error
	if format == "png" {
		err = png.Encode(&buf, picture)
	} else {
		err = jpeg.Encode(&buf, picture, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func rejection(t *testing.T, err error) invalidImage {
	t.Helper()
	var invalid invalidImage
	if !errors.As(err, &invalid) {
		t.Fatalf("expected a permanent rejection, got %v", err)
	}
	return invalid
}

func TestValidateScreenshots(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			body := fixture(t, format)
			digest := sha256.Sum256(body)
			if err := Validate(body, "image/"+format, digest[:]); err != nil {
				t.Fatal(err)
			}
			if err := Validate(body, "image/gif", digest[:]); rejection(t, err).Code != codeMediaTypeMismatch {
				t.Fatalf("type spoof accepted: %v", err)
			}
			other := map[string]string{"png": "image/jpeg", "jpeg": "image/png"}[format]
			if err := Validate(body, other, digest[:]); rejection(t, err) != (invalidImage{Code: codeMediaTypeMismatch, Reason: "magic_bytes"}) {
				t.Fatalf("cross-format spoof accepted: %v", err)
			}
			digest[0] ^= 1
			if err := Validate(body, "image/"+format, digest[:]); rejection(t, err).Code != codeChecksumMismatch {
				t.Fatalf("wrong checksum accepted: %v", err)
			}
		})
	}
}

func TestRejectMalformedAndDecompressionBomb(t *testing.T) {
	tests := map[string][]byte{"html": []byte("<html>not a screenshot</html>"), "truncated": fixture(t, "png")[:45]}
	bomb := append([]byte(nil), fixture(t, "png")...)
	binary.BigEndian.PutUint32(bomb[16:20], 100_000)
	binary.BigEndian.PutUint32(bomb[20:24], 100_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	tests["dimensions"] = bomb
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			digest := sha256.Sum256(body)
			invalid := rejection(t, Validate(body, "image/png", digest[:]))
			if name == "dimensions" && invalid.Code != codeDimensionsExceeded {
				t.Fatalf("unbounded image reached decoder: %v", invalid)
			}
		})
	}
}

type fakeStore struct {
	body  []byte
	info  storage.ObjectInfo
	err   error
	stats int
	reads int
}

func (s *fakeStore) Stat(context.Context, string) (storage.ObjectInfo, error) {
	s.stats++
	return s.info, s.err
}

func (s *fakeStore) Read(_ context.Context, _ string, limit int64) ([]byte, error) {
	s.reads++
	if int64(len(s.body)) > limit {
		return nil, storage.ErrObjectTooLarge
	}
	return s.body, s.err
}

func storedObject(t *testing.T, body []byte, mediaType string) (Job, *fakeStore) {
	t.Helper()
	digest := sha256.Sum256(body)
	job := Job{ObjectKey: "evidence/test", MediaType: mediaType, ByteSize: int64(len(body)), Checksum: digest[:]}
	info := storage.ObjectInfo{Size: job.ByteSize, MediaType: mediaType, ChecksumSHA256: base64.StdEncoding.EncodeToString(digest[:]), ETag: "etag"}
	return job, &fakeStore{body: body, info: info}
}

func TestVerifyProviderAndActualBytes(t *testing.T) {
	job, store := storedObject(t, fixture(t, "png"), "image/png")
	if etag, err := verify(context.Background(), store, nil, job); err != nil || etag != "etag" {
		t.Fatalf("valid verification: %s %v", etag, err)
	}
	store.body = append([]byte(nil), store.body...)
	store.body[10] ^= 1
	if _, err := verify(context.Background(), store, nil, job); rejection(t, err) != (invalidImage{Code: codeChecksumMismatch, Reason: "body_checksum"}) {
		t.Fatalf("trusted provider without checking bytes: %v", err)
	}
	store.reads = 0
	store.info.Size++
	if _, err := verify(context.Background(), store, nil, job); rejection(t, err) != (invalidImage{Code: codeSizeMismatch, Reason: "provider_size"}) || store.reads != 0 {
		t.Fatalf("downloaded mismatched object: %v", err)
	}
}

func TestVerifyRejectsBeforeStorageIO(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want invalidImage
	}{
		{"empty", Job{MediaType: "image/png", ByteSize: 0}, invalidImage{Code: codeSizeExceeded, Reason: "declared_size"}},
		{"oversized", Job{MediaType: "image/png", ByteSize: maxEncodedBytes + 1}, invalidImage{Code: codeSizeExceeded, Reason: "declared_size"}},
		{"video", Job{MediaType: "video/mp4", ByteSize: 10}, invalidImage{Code: codeUnsupportedImageType, Reason: "declared_media_type"}},
		{"heic", Job{MediaType: "image/heic", ByteSize: 10}, invalidImage{Code: codeUnsupportedImageType, Reason: "declared_media_type"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := verify(context.Background(), store, nil, test.job)
			if rejection(t, err) != test.want || store.stats != 0 || store.reads != 0 {
				t.Fatalf("got %v after %d stats and %d reads", err, store.stats, store.reads)
			}
		})
	}
}

func TestVerifyReturnsStorageErrorsUnwrapped(t *testing.T) {
	job, store := storedObject(t, fixture(t, "png"), "image/png")
	store.err = &storage.StatusError{Op: "head", StatusCode: 503}
	_, err := verify(context.Background(), store, nil, job)
	var status *storage.StatusError
	if !errors.As(err, &status) || classify(err) != classStorageUnavailable {
		t.Fatalf("storage status lost: %v", err)
	}
}
