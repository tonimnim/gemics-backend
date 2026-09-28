package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"math"
	rand "math/rand/v2"
	"slices"

	"github.com/gamics-io/gamics/services/api/internal/evidence"
)

const imageBytes = 2_000_000 // Decimal 2 MB, not 2 MiB.

// A real decodable PNG, not random bytes renamed .png. Padding is a valid private
// ancillary PNG chunk before IEND. This is a synthetic fixture, not gameplay data.
func fixture() ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, 768, 768))
	rng := rand.New(rand.NewPCG(17, 29))
	for p := 0; p < len(img.Pix); p += 4 {
		v := rng.Uint32()
		img.Pix[p] = byte(v)
		img.Pix[p+1] = byte(v >> 8)
		img.Pix[p+2] = byte(v >> 16)
		img.Pix[p+3] = 255
	}
	var encoded bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&encoded, img); err != nil {
		return nil, err
	}
	raw := encoded.Bytes()
	padding := imageBytes - len(raw) - 12
	if padding < 0 {
		return nil, errors.New("PNG fixture too large")
	}
	result := make([]byte, 0, imageBytes)
	result = append(result, raw[:len(raw)-12]...)
	result = binary.BigEndian.AppendUint32(result, uint32(padding))
	crcStart := len(result)
	result = append(result, []byte("gaMi")...)
	for range padding {
		result = append(result, byte(rng.Uint32()))
	}
	result = binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[crcStart:]))
	result = append(result, raw[len(raw)-12:]...)
	sum := sha256.Sum256(result)
	if err := evidence.Validate(result, "image/png", sum[:]); err != nil {
		return nil, err
	}
	return result, nil
}

type latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

func summarize(values []float64) latency {
	if len(values) == 0 {
		return latency{}
	}
	values = slices.Clone(values)
	slices.Sort(values)
	percentile := func(p float64) float64 { return values[max(0, int(math.Ceil(p*float64(len(values))))-1)] }
	return latency{len(values), percentile(.5), percentile(.95), percentile(.99), values[len(values)-1]}
}
