// Package evidence verifies private screenshots outside the API process.
package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

const MaxPixels = 16_000_000
const MaxDimension = 8192

// maxEncodedBytes is the largest screenshot the worker downloads.
const maxEncodedBytes int64 = 25 << 20

// Client-visible rejection codes, stored in evidence_uploads.processing_error_code.
const (
	codeChecksumMismatch     = "checksum_mismatch"
	codeSizeMismatch         = "size_mismatch"
	codeSizeExceeded         = "size_exceeded"
	codeMediaTypeMismatch    = "media_type_mismatch"
	codeUnsupportedImageType = "unsupported_image_type"
	codeInvalidImage         = "invalid_image"
	codeUnsupportedEncoding  = "unsupported_image_encoding"
	codeDimensionsExceeded   = "image_dimensions_exceeded"
)

type Store interface {
	Stat(context.Context, string) (storage.ObjectInfo, error)
	Read(context.Context, string, int64) ([]byte, error)
}

// invalidImage is a permanent rejection. Code is shown to the uploader; Reason
// is a bounded internal detail recorded as the job's error class.
type invalidImage struct{ Code, Reason string }

func (e invalidImage) Error() string { return "invalid image: " + e.Code + "/" + e.Reason }

// errDecodeCapacity means the process-wide decode budget stayed exhausted for
// the whole job timeout. It is transient: the job is retried.
var errDecodeCapacity = errors.New("evidence decode budget unavailable")

type imageCodec struct {
	config func(io.Reader) (image.Config, error)
	decode func(io.Reader) (image.Image, error)
}

var codecs = map[string]imageCodec{
	"jpeg": {config: jpeg.DecodeConfig, decode: jpeg.Decode},
	"png":  {config: png.DecodeConfig, decode: png.Decode},
}

// Validate inspects the actual encoded image and fully decodes it to catch
// truncated/corrupt payloads. The byte structure, dimensions and decoder
// memory are proven before any pixel allocation. This establishes media
// integrity, not the truth of a claimed game result.
func Validate(body []byte, mediaType string, expectedChecksum []byte) error {
	return validate(context.Background(), body, mediaType, expectedChecksum, nil)
}

// validate reserves the image's decoder memory from budget, once and whole,
// between the structural checks and the decode. A nil budget is unbounded.
func validate(ctx context.Context, body []byte, mediaType string, expectedChecksum []byte, budget *decodeBudget) error {
	actual := sha256.Sum256(body)
	if len(expectedChecksum) != sha256.Size || subtle.ConstantTimeCompare(actual[:], expectedChecksum) != 1 {
		return invalidImage{Code: codeChecksumMismatch, Reason: "body_checksum"}
	}
	format := sniffFormat(body)
	if format == "" {
		return invalidImage{Code: codeInvalidImage, Reason: "unknown_format"}
	}
	if mediaType != "image/"+format {
		return invalidImage{Code: codeMediaTypeMismatch, Reason: "magic_bytes"}
	}
	info, err := inspectEncodedImage(body)
	if err != nil {
		return err
	}
	codec := codecs[format]
	cfg, err := codec.config(bytes.NewReader(body))
	if err != nil {
		return invalidImage{Code: codeInvalidImage, Reason: "decode_failed"}
	}
	if cfg.Width != info.Width || cfg.Height != info.Height {
		return invalidImage{Code: codeInvalidImage, Reason: "parser_mismatch"}
	}
	release, err := budget.acquire(ctx, info.DecodedBytes+int64(len(body)))
	if err != nil {
		return err
	}
	defer release()
	decoded, err := codec.decode(bytes.NewReader(body))
	if err != nil || decoded.Bounds().Dx() != info.Width || decoded.Bounds().Dy() != info.Height {
		return invalidImage{Code: codeInvalidImage, Reason: "decode_failed"}
	}
	return nil
}

// verify returns storage and context errors unwrapped so classify can map
// them to bounded classes.
func verify(ctx context.Context, store Store, budget *decodeBudget, job Job) (string, error) {
	if job.ByteSize < 1 || job.ByteSize > maxEncodedBytes {
		return "", invalidImage{Code: codeSizeExceeded, Reason: "declared_size"}
	}
	if job.MediaType != "image/jpeg" && job.MediaType != "image/png" {
		return "", invalidImage{Code: codeUnsupportedImageType, Reason: "declared_media_type"}
	}
	info, err := store.Stat(ctx, job.ObjectKey)
	if err != nil {
		return "", err
	}
	if info.Size != job.ByteSize {
		return "", invalidImage{Code: codeSizeMismatch, Reason: "provider_size"}
	}
	if info.MediaType != job.MediaType {
		return "", invalidImage{Code: codeMediaTypeMismatch, Reason: "provider_media_type"}
	}
	if !storage.EqualChecksum(info.ChecksumSHA256, base64.StdEncoding.EncodeToString(job.Checksum)) {
		return "", invalidImage{Code: codeChecksumMismatch, Reason: "provider_checksum"}
	}
	body, err := store.Read(ctx, job.ObjectKey, job.ByteSize)
	if errors.Is(err, storage.ErrObjectTooLarge) {
		return "", invalidImage{Code: codeSizeMismatch, Reason: "body_size"}
	}
	if err != nil {
		return "", err
	}
	if int64(len(body)) != job.ByteSize {
		return "", invalidImage{Code: codeSizeMismatch, Reason: "body_size"}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validate(ctx, body, job.MediaType, job.Checksum, budget); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return info.ETag, nil
}
