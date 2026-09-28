package evidence

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"runtime"
	"testing"
	"time"
)

// markerSegment encodes one length-prefixed JPEG marker segment.
func markerSegment(marker byte, payload ...byte) []byte {
	length := len(payload) + 2
	return append([]byte{0xff, marker, byte(length >> 8), byte(length)}, payload...)
}

// frameHeader is an SOFn payload with 1x1 sampling for every component.
func frameHeader(precision byte, width, height int, ids ...byte) []byte {
	out := []byte{precision, byte(height >> 8), byte(height), byte(width >> 8), byte(width), byte(len(ids))}
	for _, id := range ids {
		out = append(out, id, 0x11, 0)
	}
	return out
}

// scanHeader is an SOS payload using table 0 for every component.
func scanHeader(spectralStart, spectralEnd byte, ids ...byte) []byte {
	out := []byte{byte(len(ids))}
	for _, id := range ids {
		out = append(out, id, 0)
	}
	return append(out, spectralStart, spectralEnd, 0)
}

// huffmanTable defines a table with a single one-bit code, "0", for symbol.
func huffmanTable(classAndID, symbol byte) []byte {
	counts := make([]byte, 16)
	counts[0] = 1
	return append(append([]byte{classAndID}, counts...), symbol)
}

func quantizationTable() []byte {
	return append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)
}

// bitWriter packs entropy-coded bits MSB first and stuffs 0xff bytes.
type bitWriter struct {
	out  []byte
	acc  byte
	bits uint
}

func (b *bitWriter) write(value uint32, count uint) {
	for i := count; i > 0; i-- {
		b.acc = b.acc<<1 | byte(value>>(i-1)&1)
		if b.bits++; b.bits == 8 {
			b.out = append(b.out, b.acc)
			if b.acc == 0xff {
				b.out = append(b.out, 0)
			}
			b.acc, b.bits = 0, 0
		}
	}
}

func (b *bitWriter) flush() []byte {
	for b.bits != 0 {
		b.write(1, 1)
	}
	return b.out
}

// flatGrayJPEG is a baseline 8-bit gray image of blocks 8x8 flat blocks: each
// block is the one-bit DC category 0 code followed by the one-bit EOB code.
// With restart set, a DRI of one MCU places RST markers inside the
// entropy-coded segment. scans repeats the scan (and its data) that many times.
func flatGrayJPEG(blocks, scans int, restart bool) []byte {
	out := []byte{0xff, 0xd8}
	out = append(out, markerSegment(jpegDQT, quantizationTable()...)...)
	out = append(out, markerSegment(jpegSOF0, frameHeader(8, 8*blocks, 8, 1)...)...)
	out = append(out, markerSegment(jpegDHT, huffmanTable(0x00, 0x00)...)...)
	out = append(out, markerSegment(jpegDHT, huffmanTable(0x10, 0x00)...)...)
	if restart {
		out = append(out, markerSegment(jpegDRI, 0, 1)...)
	}
	for range scans {
		out = append(out, markerSegment(jpegSOS, scanHeader(0, 63, 1)...)...)
		out = append(out, flatBlocks(blocks, restart)...)
	}
	return append(out, 0xff, jpegEOI)
}

func flatBlocks(blocks int, restart bool) []byte {
	if !restart {
		var bits bitWriter
		bits.write(0, uint(2*blocks))
		return bits.flush()
	}
	var out []byte
	for i := range blocks {
		if i > 0 {
			out = append(out, 0xff, jpegRST0+byte((i-1)%8))
		}
		var bits bitWriter
		bits.write(0, 2)
		out = append(out, bits.flush()...)
	}
	return out
}

// progressiveBomb is the decoder CPU exploit behind structure.go: a 4000x4000
// progressive gray frame with one DC scan and 200 AC scans whose EOB runs of
// 32,767 blocks each make image/jpeg revisit all 250,000 blocks per scan. It
// is about 37 KB and costs the stdlib decoder seconds of CPU.
func progressiveBomb() []byte {
	const side, blocks, acScans = 4000, 250_000, 200
	out := []byte{0xff, 0xd8}
	out = append(out, markerSegment(jpegDQT, quantizationTable()...)...)
	out = append(out, markerSegment(jpegSOF2, frameHeader(8, side, side, 1)...)...)
	out = append(out, markerSegment(jpegDHT, huffmanTable(0x00, 0x00)...)...)
	out = append(out, markerSegment(jpegDHT, huffmanTable(0x10, 0xe0)...)...)
	out = append(out, markerSegment(jpegSOS, scanHeader(0, 0, 1)...)...)
	var dc bitWriter
	for range blocks {
		dc.write(0, 1)
	}
	out = append(out, dc.flush()...)
	for range acScans {
		out = append(out, markerSegment(jpegSOS, scanHeader(1, 63, 1)...)...)
		var ac bitWriter
		for covered := 0; covered < blocks; covered += 1<<15 - 1 {
			ac.write(0, 1)
			ac.write(1<<14-1, 14)
		}
		out = append(out, ac.flush()...)
	}
	return append(out, 0xff, jpegEOI)
}

func encodeJPEG(t *testing.T, picture image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, picture image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, picture); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// patternGray and patternRGBA fill deterministic noise, which makes the JPEG
// encoder emit stuffed 0xff00 bytes in its entropy-coded data.
func patternGray(width, height int) *image.Gray {
	picture := image.NewGray(image.Rect(0, 0, width, height))
	fillNoise(picture.Pix)
	return picture
}

func patternRGBA(width, height int) *image.RGBA {
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	fillNoise(picture.Pix)
	return picture
}

func fillNoise(pix []byte) {
	source := rand.New(rand.NewPCG(17, 29))
	for i := range pix {
		pix[i] = byte(source.Uint32())
	}
}

// withAdobeTransform inserts an APP14 Adobe segment after SOI; transform 0
// makes image/jpeg treat three components as RGB and allocate an RGBA copy.
func withAdobeTransform(body []byte, transform byte) []byte {
	app14 := markerSegment(jpegAPP14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, transform)
	return splice(body, 2, app14...)
}

func splice(body []byte, at int, insert ...byte) []byte {
	out := append([]byte(nil), body[:at]...)
	out = append(out, insert...)
	return append(out, body[at:]...)
}

// markerOffset returns the offset of the first "ff marker" pair.
func markerOffset(t *testing.T, body []byte, marker byte) int {
	t.Helper()
	offset := bytes.Index(body, []byte{0xff, marker})
	if offset < 0 {
		t.Fatalf("marker %x not found", marker)
	}
	return offset
}

func validateBody(body []byte, mediaType string) error {
	digest := sha256.Sum256(body)
	return Validate(body, mediaType, digest[:])
}

func allocatedBytes(run func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestInspectJPEGAcceptsGoEncodedBaseline(t *testing.T) {
	for name, body := range map[string][]byte{
		"gray":          encodeJPEG(t, patternGray(97, 45)),
		"ycbcr":         encodeJPEG(t, patternRGBA(97, 45)),
		"adobe_rgb":     withAdobeTransform(encodeJPEG(t, patternRGBA(97, 45)), 0),
		"flat_gray":     flatGrayJPEG(3, 1, false),
		"flat_restarts": flatGrayJPEG(5, 1, true),
	} {
		t.Run(name, func(t *testing.T) {
			info, err := inspectEncodedImage(body)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(body))
			if err != nil || sniffFormat(body) != "jpeg" || info.Width != cfg.Width || info.Height != cfg.Height {
				t.Fatalf("parser disagrees with decoder: %+v %+v %v", info, cfg, err)
			}
			if err := validateBody(body, "image/jpeg"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectProgressiveJPEGBeforeDecode(t *testing.T) {
	bomb := progressiveBomb()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(bomb))
	if err != nil || cfg.Width*cfg.Height > MaxPixels {
		t.Fatalf("the bomb must pass the old dimension gate: %+v %v", cfg, err)
	}
	patched := encodeJPEG(t, patternGray(64, 64))
	patched[markerOffset(t, patched, jpegSOF0)+1] = jpegSOF2
	want := invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_progressive"}
	for name, body := range map[string][]byte{"bomb": bomb, "patched": patched} {
		t.Run(name, func(t *testing.T) {
			// The allocation bound proves the decoder never ran; a wall-clock
			// bound would only make the test flaky on a busy runner.
			var err error
			allocated := allocatedBytes(func() { err = validateBody(body, "image/jpeg") })
			if allocated > 1<<20 {
				t.Fatalf("rejection allocated %d bytes", allocated)
			}
			if rejection(t, err) != want {
				t.Fatalf("progressive JPEG not rejected: %v", err)
			}
		})
	}
}

func TestRejectUnsupportedSOFVariants(t *testing.T) {
	base := encodeJPEG(t, patternGray(16, 16))
	offset := markerOffset(t, base, jpegSOF0) + 1
	for _, marker := range []byte{0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, jpegDAC, 0xcd, 0xce, 0xcf} {
		body := append([]byte(nil), base...)
		body[offset] = marker
		if err := validateBody(body, "image/jpeg"); rejection(t, err) != (invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_sof_unsupported"}) {
			t.Fatalf("SOF %x: %v", marker, err)
		}
	}
	body := append([]byte(nil), base...)
	body[offset] = jpegSOF1
	if err := validateBody(body, "image/jpeg"); err != nil {
		t.Fatalf("extended sequential SOF1 rejected: %v", err)
	}
}

func TestRejectCMYKTwoComponentAnd12BitJPEG(t *testing.T) {
	frameOnly := func(precision byte, ids ...byte) []byte {
		out := []byte{0xff, 0xd8}
		out = append(out, markerSegment(jpegSOF0, frameHeader(precision, 16, 16, ids...)...)...)
		out = append(out, markerSegment(jpegSOS, scanHeader(0, 63, ids...)...)...)
		return append(out, 0x00, 0xff, jpegEOI)
	}
	cmyk := withAdobeTransform(frameOnly(8, 1, 2, 3, 4), 2)
	precision := encodeJPEG(t, patternGray(16, 16))
	precision[markerOffset(t, precision, jpegSOF0)+4] = 12
	tests := map[string]struct {
		body []byte
		want invalidImage
	}{
		"cmyk":          {cmyk, invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_components"}},
		"two_component": {frameOnly(8, 1, 2), invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_components"}},
		"twelve_bit":    {precision, invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_precision"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateBody(test.body, "image/jpeg"); rejection(t, err) != test.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRejectRepeatedSequentialScans(t *testing.T) {
	want := invalidImage{Code: codeInvalidImage, Reason: "jpeg_scan_repeat"}
	encoded := encodeJPEG(t, patternRGBA(48, 32))
	scan := encoded[markerOffset(t, encoded, jpegSOS) : len(encoded)-2]
	tests := map[string][]byte{
		"go_encoded_twice": splice(encoded, len(encoded)-2, scan...),
		"flat_twice":       flatGrayJPEG(2, 2, false),
		// Junk before the second scan is skipped by the decoder, so the walk
		// must still find it.
		"hidden_after_junk": splice(encoded, len(encoded)-2, append([]byte{0x12, 0x34, 0xff, 0x00, 0xff, jpegRST0}, scan...)...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateBody(body, "image/jpeg"); rejection(t, err) != want {
				t.Fatalf("repeated scan accepted: %v", err)
			}
		})
	}
}

func TestJPEGMarkerWalkMatchesDecoder(t *testing.T) {
	encoded := encodeJPEG(t, patternGray(40, 24))
	if !bytes.Contains(encoded[markerOffset(t, encoded, jpegSOS):], []byte{0xff, 0x00}) {
		t.Fatal("fixture must contain stuffed bytes in its entropy-coded data")
	}
	dqt := markerOffset(t, encoded, jpegDQT)
	accepted := map[string][]byte{
		"stuffed_entropy_data": encoded,
		"fill_bytes":           splice(encoded, dqt, 0xff, 0xff, 0xff),
		"extraneous_bytes":     splice(encoded, dqt, 0x00, 0x42, 0x13),
		"extraneous_ff00":      splice(encoded, dqt, 0xff, 0x00),
		"standalone_rst":       splice(encoded, dqt, 0xff, jpegRST0+3),
		"restart_markers":      flatGrayJPEG(10, 1, true),
		"trailing_after_eoi":   append(append([]byte(nil), encoded...), 0xff, 0xd8, 0xff, jpegSOS, 1, 2, 3),
		"comment_and_app":      splice(encoded, dqt, append(markerSegment(jpegCOM, 'h', 'i'), markerSegment(0xe1, 'E', 'x', 'i', 'f', 0, 0)...)...),
	}
	for name, body := range accepted {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectEncodedImage(body); err != nil {
				t.Fatalf("walk rejected a stream the decoder accepts: %v", err)
			}
			if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
				t.Fatalf("fixture is not decodable: %v", err)
			}
		})
	}
	rejected := map[string][]byte{
		"soi_inside_stream": splice(encoded, dqt, 0xff, 0xd8),
		"missing_eoi":       encoded[:len(encoded)-2],
		"truncated_segment": encoded[:dqt+10],
		"fill_then_zero":    splice(encoded, dqt, 0xff, 0xff, 0x00, 0x02),
		"unknown_marker":    splice(encoded, dqt, markerSegment(0xf0, 0)...),
		"short_length":      splice(encoded, dqt, 0xff, jpegCOM, 0x00, 0x01),
		"no_scan":           append(append([]byte(nil), encoded[:markerOffset(t, encoded, jpegSOS)]...), 0xff, jpegEOI),
		"scan_before_frame": append([]byte{0xff, 0xd8}, append(markerSegment(jpegSOS, scanHeader(0, 63, 1)...), 0xff, jpegEOI)...),
		"second_frame":      splice(encoded, dqt, markerSegment(jpegSOF0, frameHeader(8, 8, 8, 1)...)...),
		"zero_height":       append([]byte{0xff, 0xd8}, append(markerSegment(jpegSOF0, frameHeader(8, 8, 0, 1)...), 0xff, jpegEOI)...),
		"unknown_component": append(append([]byte(nil), encoded[:markerOffset(t, encoded, jpegSOS)]...), append(markerSegment(jpegSOS, scanHeader(0, 63, 9)...), 0xff, jpegEOI)...),
	}
	for name, body := range rejected {
		t.Run(name, func(t *testing.T) {
			if rejection(t, validateBody(body, "image/jpeg")).Code != codeInvalidImage {
				t.Fatal("malformed marker stream was not invalid_image")
			}
		})
	}
}

func TestJPEGSegmentCap(t *testing.T) {
	encoded := encodeJPEG(t, patternGray(8, 8))
	dqt := markerOffset(t, encoded, jpegDQT)
	comments := bytes.Repeat(markerSegment(jpegCOM), maxJPEGSegments)
	if _, err := inspectEncodedImage(splice(encoded, dqt, comments...)); rejection(t, err) != errJPEGStructure {
		t.Fatalf("segment cap not enforced: %v", err)
	}
	if _, err := inspectEncodedImage(splice(encoded, dqt, comments[:len(comments)-40]...)); err != nil {
		t.Fatalf("segments below the cap rejected: %v", err)
	}
}

func pngChunkBytes(kind string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, kind...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[4:]))
}

func ihdrChunk(width, height uint32, depth, colorType, interlace byte) []byte {
	data := binary.BigEndian.AppendUint32(nil, width)
	data = binary.BigEndian.AppendUint32(data, height)
	return pngChunkBytes("IHDR", append(data, depth, colorType, 0, 0, interlace))
}

func buildPNG(chunks ...[]byte) []byte {
	out := append([]byte(nil), pngSignature...)
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}

func zlibBytes(t testing.TB, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

// grayPNG encodes an 8-bit gray image, optionally with Adam7 interlacing and
// a tRNS chunk; image/png's encoder writes neither.
func grayPNG(t testing.TB, width, height int, interlaced, transparent bool) []byte {
	t.Helper()
	passes, interlace := [][4]int{{0, 0, 1, 1}}, byte(0)
	if interlaced {
		passes, interlace = [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}, 1
	}
	var raw []byte
	for _, pass := range passes {
		passWidth := (width - pass[0] + pass[2] - 1) / pass[2]
		passHeight := (height - pass[1] + pass[3] - 1) / pass[3]
		if passWidth == 0 || passHeight == 0 {
			continue
		}
		for y := range passHeight {
			raw = append(raw, 0)
			for x := range passWidth {
				raw = append(raw, byte(x*3+y))
			}
		}
	}
	chunks := [][]byte{ihdrChunk(uint32(width), uint32(height), 8, 0, interlace)}
	if transparent {
		chunks = append(chunks, pngChunkBytes("tRNS", []byte{0, 7}))
	}
	chunks = append(chunks, pngChunkBytes("IDAT", zlibBytes(t, raw)), pngChunkBytes("IEND", nil))
	return buildPNG(chunks...)
}

// headerOnlyPNG carries a valid chunk list around an arbitrary IHDR; it must
// be rejected or accepted from the header alone, never decoded.
func headerOnlyPNG(width, height uint32, depth, colorType, interlace byte) []byte {
	return buildPNG(ihdrChunk(width, height, depth, colorType, interlace), pngChunkBytes("IDAT", []byte{0x78, 0x9c}), pngChunkBytes("IEND", nil))
}

func TestRejectSixteenBitPNG(t *testing.T) {
	for name, picture := range map[string]image.Image{
		"rgba64": image.NewNRGBA64(image.Rect(0, 0, 32, 16)),
		"gray16": image.NewGray16(image.Rect(0, 0, 32, 16)),
	} {
		t.Run(name, func(t *testing.T) {
			body := encodePNG(t, picture)
			if err := validateBody(body, "image/png"); rejection(t, err) != (invalidImage{Code: codeUnsupportedEncoding, Reason: "png_bit_depth"}) {
				t.Fatalf("16-bit PNG accepted: %v", err)
			}
		})
	}
}

func TestInterlacedPNGBudgetedNotBanned(t *testing.T) {
	small := grayPNG(t, 37, 21, true, false)
	if err := validateBody(small, "image/png"); err != nil {
		t.Fatalf("small interlaced PNG rejected: %v", err)
	}
	info, err := inspectEncodedImage(small)
	if err != nil || info.DecodedBytes != 2*37*21+2*(4*37+1)+decoderOverhead {
		t.Fatalf("interlace not costed twice: %+v %v", info, err)
	}
	// 14 MP of RGBA fits the budget progressively but not interlaced.
	if _, err := inspectEncodedImage(headerOnlyPNG(4000, 3500, 8, 6, 0)); err != nil {
		t.Fatalf("non-interlaced 14 MP RGBA rejected: %v", err)
	}
	var rejected error
	allocated := allocatedBytes(func() { rejected = validateBody(headerOnlyPNG(4000, 3500, 8, 6, 1), "image/png") })
	if rejection(t, rejected) != (invalidImage{Code: codeDimensionsExceeded, Reason: "decode_budget"}) || allocated > 1<<20 {
		t.Fatalf("interlaced 14 MP RGBA reached the decoder: %v after %d bytes", rejected, allocated)
	}
}

func TestPNGStructureRules(t *testing.T) {
	valid := grayPNG(t, 8, 8, false, false)
	ihdr := ihdrChunk(8, 8, 8, 0, 0)
	idat := pngChunkBytes("IDAT", zlibBytes(t, make([]byte, 9*8)))
	iend := pngChunkBytes("IEND", nil)
	if err := validateBody(valid, "image/png"); err != nil {
		t.Fatal(err)
	}
	private := pngChunkBytes("gaMi", bytes.Repeat([]byte{7}, 230_000))
	if err := validateBody(buildPNG(ihdr, idat, private, iend), "image/png"); err != nil {
		t.Fatalf("large private ancillary chunk rejected: %v", err)
	}
	oversizedLength := append(binary.BigEndian.AppendUint32(nil, 0x80000000), "tEXt"...)
	tests := map[string]struct {
		body []byte
		want invalidImage
	}{
		"idat_first":         {buildPNG(idat, ihdr, iend), errPNGStructure},
		"short_ihdr":         {buildPNG(pngChunkBytes("IHDR", make([]byte, 12)), idat, iend), errPNGStructure},
		"duplicate_ihdr":     {buildPNG(ihdr, ihdr, idat, iend), errPNGStructure},
		"missing_idat":       {buildPNG(ihdr, iend), errPNGStructure},
		"missing_iend":       {buildPNG(ihdr, idat), errPNGStructure},
		"truncated_chunk":    {buildPNG(ihdr, idat[:len(idat)-2]), errPNGStructure},
		"chunk_length":       {append(buildPNG(ihdr), oversizedLength...), errPNGStructure},
		"zero_width":         {buildPNG(ihdrChunk(0, 8, 8, 0, 0), idat, iend), errPNGStructure},
		"negative_width":     {buildPNG(ihdrChunk(0x80000000, 8, 8, 0, 0), idat, iend), errPNGStructure},
		"compression_method": {buildPNG(pngChunkBytes("IHDR", []byte{0, 0, 0, 8, 0, 0, 0, 8, 8, 0, 1, 0, 0}), idat, iend), errPNGStructure},
		"filter_method":      {buildPNG(pngChunkBytes("IHDR", []byte{0, 0, 0, 8, 0, 0, 0, 8, 8, 0, 0, 1, 0}), idat, iend), errPNGStructure},
		"interlace_method":   {buildPNG(ihdrChunk(8, 8, 8, 0, 2), idat, iend), errPNGStructure},
		"rgb_depth_4":        {buildPNG(ihdrChunk(8, 8, 4, 2, 0), idat, iend), errPNGStructure},
		"palette_depth_16":   {buildPNG(ihdrChunk(8, 8, 16, 3, 0), idat, iend), errPNGStructure},
		"color_type_5":       {buildPNG(ihdrChunk(8, 8, 8, 5, 0), idat, iend), errPNGStructure},
		"oversized_side":     {headerOnlyPNG(MaxDimension+1, 8, 8, 0, 0), invalidImage{Code: codeDimensionsExceeded, Reason: "dimensions"}},
		"oversized_pixels":   {headerOnlyPNG(4001, 4000, 8, 0, 0), invalidImage{Code: codeDimensionsExceeded, Reason: "pixels"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateBody(test.body, "image/png"); rejection(t, err) != test.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestDecodedBytesEstimateIsUpperBound(t *testing.T) {
	palette := image.NewPaletted(image.Rect(0, 0, 1600, 1200), color.Palette{color.Gray{0}, color.Gray{128}, color.Gray{255}})
	for i := range palette.Pix {
		palette.Pix[i] = byte(i % 3)
	}
	opaque := patternRGBA(1600, 1200)
	for i := 3; i < len(opaque.Pix); i += 4 {
		opaque.Pix[i] = 0xff
	}
	translucent := image.NewNRGBA(image.Rect(0, 0, 1600, 1200))
	copy(translucent.Pix, patternRGBA(1600, 1200).Pix)
	tests := map[string]struct {
		body   []byte
		decode func([]byte) (image.Image, error)
	}{
		"jpeg_gray":      {encodeJPEG(t, patternGray(1600, 1200)), decodeJPEG},
		"jpeg_ycbcr":     {encodeJPEG(t, patternRGBA(1600, 1200)), decodeJPEG},
		"jpeg_adobe_rgb": {withAdobeTransform(encodeJPEG(t, patternRGBA(1600, 1200)), 0), decodeJPEG},
		"png_gray":       {encodePNG(t, patternGray(1600, 1200)), decodePNG},
		"png_gray_trns":  {grayPNG(t, 1600, 1200, false, true), decodePNG},
		"png_paletted":   {encodePNG(t, palette), decodePNG},
		"png_rgb":        {encodePNG(t, opaque), decodePNG},
		"png_nrgba":      {encodePNG(t, translucent), decodePNG},
		"png_adam7_gray": {grayPNG(t, 2000, 1500, true, false), decodePNG},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			info, err := inspectEncodedImage(test.body)
			if err != nil {
				t.Fatal(err)
			}
			var decodeErr error
			allocated := allocatedBytes(func() { _, decodeErr = test.decode(test.body) })
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if int64(allocated) > info.DecodedBytes {
				t.Fatalf("decoder allocated %d bytes, estimate %d", allocated, info.DecodedBytes)
			}
		})
	}
}

func decodeJPEG(body []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(body)) }
func decodePNG(body []byte) (image.Image, error)  { return png.Decode(bytes.NewReader(body)) }

func threeComponentFrame(width, height int, adobeRGB bool) []byte {
	out := []byte{0xff, 0xd8}
	out = append(out, markerSegment(jpegSOF0, frameHeader(8, width, height, 1, 2, 3)...)...)
	out = append(out, markerSegment(jpegSOS, scanHeader(0, 63, 1, 2, 3)...)...)
	out = append(out, 0x00, 0xff, jpegEOI)
	if adobeRGB {
		return withAdobeTransform(out, 0)
	}
	return out
}

func TestPhoneScreenshotSizesAccepted(t *testing.T) {
	for _, size := range [][2]int{{1440, 3200}, {1290, 2796}, {1644, 3840}, {3840, 2160}} {
		width, height := size[0], size[1]
		for name, body := range map[string][]byte{
			"png_rgba":       headerOnlyPNG(uint32(width), uint32(height), 8, 6, 0),
			"png_rgb_adam7":  headerOnlyPNG(uint32(width), uint32(height), 8, 2, 1),
			"jpeg_ycbcr":     threeComponentFrame(width, height, false),
			"jpeg_adobe_rgb": threeComponentFrame(width, height, true),
		} {
			info, err := inspectEncodedImage(body)
			if err != nil || info.Width != width || info.Height != height || info.DecodedBytes > MaxDecodedBytes {
				t.Fatalf("%dx%d %s rejected: %+v %v", width, height, name, info, err)
			}
		}
	}
	body := encodeJPEG(t, patternRGBA(1290, 2796))
	if err := validateBody(body, "image/jpeg"); err != nil {
		t.Fatalf("full-size phone screenshot rejected: %v", err)
	}
}

func TestDecodeBudgetWaitIsTransient(t *testing.T) {
	budget := newDecodeBudget(MinDecodeBudgetBytes)
	hold, err := budget.acquire(context.Background(), MinDecodeBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	body := fixture(t, "png")
	digest := sha256.Sum256(body)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = validate(ctx, body, "image/png", digest[:], budget)
	if !errors.Is(err, errDecodeCapacity) || budget.waits.Load() != 1 {
		t.Fatalf("exhausted budget did not time out transiently: %v", err)
	}
	w := newWorker(nil, &fakeStore{}, discardLogger(), DefaultOptions())
	queued := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	if result := w.decide(err, Job{Attempts: 1, QueuedAt: queued, UploadExpiresAt: queued}, queued.Add(time.Second)); result.status != statusQueued || result.class != classDecodeCapacity {
		t.Fatalf("decode capacity was not retried: %+v", result)
	}
	hold()
	if err := validate(context.Background(), body, "image/png", digest[:], budget); err != nil {
		t.Fatal(err)
	}
	if budget.inUse.Load() != 0 {
		t.Fatalf("decode budget leaked %d bytes", budget.inUse.Load())
	}
	if _, err := budget.acquire(context.Background(), MinDecodeBudgetBytes+1); !errors.Is(err, errDecodeCapacity) {
		t.Fatalf("unsatisfiable reservation waited: %v", err)
	}
}

func FuzzValidate(f *testing.F) {
	for _, seed := range [][]byte{
		flatGrayJPEG(2, 1, false), flatGrayJPEG(3, 1, true), flatGrayJPEG(1, 2, false),
		progressiveBomb()[:512], threeComponentFrame(16, 16, true),
		grayPNG(f, 9, 7, true, true), headerOnlyPNG(16, 16, 8, 6, 0),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		info, inspectErr := inspectEncodedImage(body)
		if inspectErr == nil && (info.Width < 1 || info.Height < 1 || info.DecodedBytes > MaxDecodedBytes) {
			t.Fatalf("inspection admitted %+v", info)
		}
		for _, mediaType := range []string{"image/jpeg", "image/png"} {
			if err := validateBody(body, mediaType); err == nil && inspectErr != nil {
				t.Fatalf("decoded an image the structural walk rejected: %v", inspectErr)
			}
		}
	})
}
