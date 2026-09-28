package evidence

import (
	"bytes"
	"encoding/binary"
)

// The parsers below run before any decoder call. image/jpeg and image/png
// cannot be cancelled, so the only CPU and memory bound on a hostile upload is
// what these byte walks prove about the stream: a single frame, no progressive
// coefficient buffers, no repeated scans and a decoded size within budget.

const (
	// MaxDecodedBytes caps the estimated decoder allocation for one image.
	MaxDecodedBytes int64 = 96 << 20
	// decoderOverhead covers decoder state, Huffman tables and zlib windows.
	decoderOverhead   int64 = 1 << 20
	maxJPEGSegments         = 1024
	maxPNGChunkLength       = 0x7fffffff
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// encodedImageInfo is what the structural walk proves before decoding.
// DecodedBytes is an upper bound on the decoder's allocations.
type encodedImageInfo struct {
	Width        int
	Height       int
	DecodedBytes int64
}

func sniffFormat(body []byte) string {
	switch {
	case len(body) >= 2 && body[0] == 0xff && body[1] == 0xd8:
		return "jpeg"
	case bytes.HasPrefix(body, pngSignature):
		return "png"
	default:
		return ""
	}
}

func inspectEncodedImage(body []byte) (encodedImageInfo, error) {
	var info encodedImageInfo
	var err error
	switch sniffFormat(body) {
	case "jpeg":
		info, err = inspectJPEG(body)
	case "png":
		info, err = inspectPNG(body)
	default:
		return encodedImageInfo{}, invalidImage{Code: codeInvalidImage, Reason: "unknown_format"}
	}
	if err != nil {
		return encodedImageInfo{}, err
	}
	return info, checkImageLimits(info)
}

// checkImageLimits tests dimensions first: DecodedBytes is only meaningful
// once both sides are known to be small enough that its products cannot wrap.
func checkImageLimits(info encodedImageInfo) error {
	if info.Width > MaxDimension || info.Height > MaxDimension {
		return invalidImage{Code: codeDimensionsExceeded, Reason: "dimensions"}
	}
	if int64(info.Width)*int64(info.Height) > MaxPixels {
		return invalidImage{Code: codeDimensionsExceeded, Reason: "pixels"}
	}
	if info.DecodedBytes > MaxDecodedBytes {
		return invalidImage{Code: codeDimensionsExceeded, Reason: "decode_budget"}
	}
	return nil
}

const (
	jpegSOF0  = 0xc0
	jpegSOF1  = 0xc1
	jpegSOF2  = 0xc2
	jpegDHT   = 0xc4
	jpegJPG   = 0xc8
	jpegDAC   = 0xcc
	jpegRST0  = 0xd0
	jpegRST7  = 0xd7
	jpegEOI   = 0xd9
	jpegSOS   = 0xda
	jpegDQT   = 0xdb
	jpegDRI   = 0xdd
	jpegAPP0  = 0xe0
	jpegAPP14 = 0xee
	jpegAPP15 = 0xef
	jpegCOM   = 0xfe
)

var (
	errJPEGStructure = invalidImage{Code: codeInvalidImage, Reason: "jpeg_structure"}
	errPNGStructure  = invalidImage{Code: codeInvalidImage, Reason: "png_structure"}
)

// jpegWalk accumulates what the segment walk has proven so far.
type jpegWalk struct {
	width, height  int
	components     []byte // Frame component identifiers in SOF order.
	scanned        [3]bool
	scans          int
	jfif           bool
	adobeValid     bool
	adobeTransform byte
}

// inspectJPEG mirrors image/jpeg's marker walk so that it can never miss a
// segment the decoder would act on: extraneous bytes, stuffed FF00 pairs and
// fill bytes are skipped, standalone RST markers are ignored, and the walk
// stops at the first EOI. It is stricter than the decoder everywhere else.
func inspectJPEG(body []byte) (encodedImageInfo, error) {
	if sniffFormat(body) != "jpeg" {
		return encodedImageInfo{}, errJPEGStructure
	}
	var walk jpegWalk
	pos, segments := 2, 0
	for {
		marker, next, ok := nextJPEGMarker(body, pos)
		if !ok {
			return encodedImageInfo{}, errJPEGStructure
		}
		if marker == jpegEOI {
			break
		}
		if segments++; segments > maxJPEGSegments {
			return encodedImageInfo{}, errJPEGStructure
		}
		pos = next
		if marker >= jpegRST0 && marker <= jpegRST7 {
			continue
		}
		payload, end, ok := jpegSegment(body, pos)
		if !ok {
			return encodedImageInfo{}, errJPEGStructure
		}
		pos = end
		if err := walk.apply(marker, payload); err != nil {
			return encodedImageInfo{}, err
		}
		if marker == jpegSOS {
			pos = skipEntropyCodedData(body, pos)
		}
	}
	if walk.components == nil || walk.scans == 0 {
		return encodedImageInfo{}, errJPEGStructure
	}
	return encodedImageInfo{Width: walk.width, Height: walk.height, DecodedBytes: walk.decodedBytes()}, nil
}

// nextJPEGMarker returns the next marker at or after pos and the offset just
// past it, following image/jpeg's decode loop byte for byte.
func nextJPEGMarker(body []byte, pos int) (marker byte, next int, ok bool) {
	for {
		for pos < len(body) && body[pos] != 0xff {
			pos++
		}
		if pos+1 >= len(body) {
			return 0, 0, false
		}
		marker, pos = body[pos+1], pos+2
		if marker == 0x00 {
			continue
		}
		for marker == 0xff {
			if pos >= len(body) {
				return 0, 0, false
			}
			marker, pos = body[pos], pos+1
		}
		return marker, pos, true
	}
}

// jpegSegment reads a length-prefixed segment payload starting at pos.
func jpegSegment(body []byte, pos int) (payload []byte, end int, ok bool) {
	if pos+2 > len(body) {
		return nil, 0, false
	}
	length := int(binary.BigEndian.Uint16(body[pos:]))
	if length < 2 || pos+length > len(body) {
		return nil, 0, false
	}
	return body[pos+2 : pos+length], pos + length, true
}

// skipEntropyCodedData returns the offset of the first marker after a scan.
// Stuffed FF00 pairs and RST markers belong to the entropy-coded segment; any
// other FF (including fill) starts the next marker, exactly where the decoder
// stops reading coefficients.
func skipEntropyCodedData(body []byte, pos int) int {
	for pos+1 < len(body) {
		if body[pos] == 0xff {
			next := body[pos+1]
			if next != 0x00 && (next < jpegRST0 || next > jpegRST7) {
				return pos
			}
			pos += 2
			continue
		}
		pos++
	}
	return len(body)
}

func (w *jpegWalk) apply(marker byte, payload []byte) error {
	switch {
	case marker == jpegSOF0 || marker == jpegSOF1:
		return w.frame(payload)
	case marker == jpegSOF2:
		return invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_progressive"}
	case marker > jpegSOF2 && marker <= 0xcf && marker != jpegDHT && marker != jpegJPG:
		return invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_sof_unsupported"}
	case marker == jpegSOS:
		return w.scan(payload)
	case marker == jpegAPP0:
		if len(payload) >= 5 {
			w.jfif = bytes.HasPrefix(payload, []byte("JFIF\x00"))
		}
		return nil
	case marker == jpegAPP14:
		if len(payload) >= 12 && bytes.HasPrefix(payload, []byte("Adobe")) {
			w.adobeValid, w.adobeTransform = true, payload[11]
		}
		return nil
	case marker == jpegDHT || marker == jpegDQT || marker == jpegDRI || marker == jpegCOM || (marker >= jpegAPP0 && marker <= jpegAPP15):
		return nil
	default:
		return errJPEGStructure
	}
}

func (w *jpegWalk) frame(payload []byte) error {
	if w.components != nil || len(payload) < 6 {
		return errJPEGStructure
	}
	if payload[0] != 8 {
		return invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_precision"}
	}
	count := int(payload[5])
	if count != 1 && count != 3 {
		return invalidImage{Code: codeUnsupportedEncoding, Reason: "jpeg_components"}
	}
	if len(payload) != 6+3*count {
		return errJPEGStructure
	}
	w.height = int(binary.BigEndian.Uint16(payload[1:]))
	w.width = int(binary.BigEndian.Uint16(payload[3:]))
	if w.width == 0 || w.height == 0 {
		return errJPEGStructure
	}
	w.components = make([]byte, count)
	for i := range count {
		id := payload[6+3*i]
		if bytes.IndexByte(w.components[:i], id) >= 0 {
			return errJPEGStructure
		}
		w.components[i] = id
	}
	return nil
}

// scan admits each frame component to at most one scan, so a sequential image
// is decoded in at most len(components) passes.
func (w *jpegWalk) scan(payload []byte) error {
	if w.components == nil || len(payload) < 1 {
		return errJPEGStructure
	}
	count := int(payload[0])
	if count < 1 || count > len(w.components) || len(payload) != 4+2*count {
		return errJPEGStructure
	}
	for i := range count {
		index := bytes.IndexByte(w.components, payload[1+2*i])
		if index < 0 {
			return errJPEGStructure
		}
		if w.scanned[index] {
			return invalidImage{Code: codeInvalidImage, Reason: "jpeg_scan_repeat"}
		}
		w.scanned[index] = true
	}
	w.scans++
	return nil
}

// decodedBytes bounds image/jpeg's allocations: MCU-padded planes (luma
// sampling factors reach 4x2), plus an RGBA copy when the decoder treats the
// three components as RGB rather than YCbCr.
func (w *jpegWalk) decodedBytes() int64 {
	width, height := int64(w.width), int64(w.height)
	if len(w.components) == 1 {
		return (width+7)*(height+7) + decoderOverhead
	}
	total := 3*(width+31)*(height+15) + decoderOverhead
	if w.rgb() {
		total += 4 * width * height
	}
	return total
}

func (w *jpegWalk) rgb() bool {
	if w.jfif {
		return false
	}
	return (w.adobeValid && w.adobeTransform == 0) || bytes.Equal(w.components, []byte("RGB"))
}

// inspectPNG walks the chunk list without inflating anything. Ancillary chunk
// size and count are not capped: the walk is linear in the body, which the
// worker has already bounded.
func inspectPNG(body []byte) (encodedImageInfo, error) {
	if sniffFormat(body) != "png" {
		return encodedImageInfo{}, errPNGStructure
	}
	first, pos, ok := pngChunk(body, len(pngSignature))
	if !ok || first.kind != "IHDR" {
		return encodedImageInfo{}, errPNGStructure
	}
	header, err := parseIHDR(first.data)
	if err != nil {
		return encodedImageInfo{}, err
	}
	transparency, seenIDAT := false, false
	for {
		chunk, next, ok := pngChunk(body, pos)
		if !ok {
			return encodedImageInfo{}, errPNGStructure
		}
		pos = next
		switch chunk.kind {
		case "IHDR":
			return encodedImageInfo{}, errPNGStructure
		case "tRNS":
			transparency = true
		case "IDAT":
			seenIDAT = true
		case "IEND":
			if !seenIDAT {
				return encodedImageInfo{}, errPNGStructure
			}
			return header.info(transparency), nil
		}
	}
}

type pngChunkView struct {
	kind string
	data []byte
}

// pngChunk returns the chunk at pos and the offset after its CRC. The CRC is
// left to the decoder; a corrupt chunk fails there before any pixel work.
func pngChunk(body []byte, pos int) (pngChunkView, int, bool) {
	if pos+8 > len(body) {
		return pngChunkView{}, 0, false
	}
	length := binary.BigEndian.Uint32(body[pos:])
	if length > maxPNGChunkLength || int64(pos)+12+int64(length) > int64(len(body)) {
		return pngChunkView{}, 0, false
	}
	start := pos + 8
	end := start + int(length)
	return pngChunkView{kind: string(body[pos+4 : start]), data: body[start:end]}, end + 4, true
}

type pngHeader struct {
	width, height int
	colorType     byte
	interlaced    bool
}

func parseIHDR(data []byte) (pngHeader, error) {
	if len(data) != 13 {
		return pngHeader{}, errPNGStructure
	}
	width := binary.BigEndian.Uint32(data[0:])
	height := binary.BigEndian.Uint32(data[4:])
	depth, colorType := data[8], data[9]
	if width == 0 || height == 0 || width > maxPNGChunkLength || height > maxPNGChunkLength {
		return pngHeader{}, errPNGStructure
	}
	if data[10] != 0 || data[11] != 0 || data[12] > 1 {
		return pngHeader{}, errPNGStructure
	}
	if depth == 16 && (colorType == 0 || colorType == 2 || colorType == 4 || colorType == 6) {
		return pngHeader{}, invalidImage{Code: codeUnsupportedEncoding, Reason: "png_bit_depth"}
	}
	if !validPNGDepth(depth, colorType) {
		return pngHeader{}, errPNGStructure
	}
	return pngHeader{width: int(width), height: int(height), colorType: colorType, interlaced: data[12] == 1}, nil
}

func validPNGDepth(depth, colorType byte) bool {
	switch colorType {
	case 0, 3:
		return depth == 1 || depth == 2 || depth == 4 || depth == 8
	case 2, 4, 6:
		return depth == 8
	default:
		return false
	}
}

// info bounds image/png's allocations. Palette and opaque gray images decode
// to one byte per pixel, everything else to four. Adam7 allocates every pass
// image beside the final image, doubling the pixel memory. Two row buffers of
// at most 4w+1 bytes are live while rows are unfiltered.
func (h pngHeader) info(transparency bool) encodedImageInfo {
	width, height := int64(h.width), int64(h.height)
	bytesPerPixel := int64(4)
	if h.colorType == 3 || (h.colorType == 0 && !transparency) {
		bytesPerPixel = 1
	}
	pixels := bytesPerPixel * width * height
	if h.interlaced {
		pixels *= 2
	}
	return encodedImageInfo{
		Width: h.width, Height: h.height,
		DecodedBytes: pixels + 2*(4*width+1) + decoderOverhead,
	}
}
