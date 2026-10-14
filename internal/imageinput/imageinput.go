// Package imageinput prepares PNG, JPEG, GIF, and static WebP images for Assistant.
// Content must match its declared media type or the backend silently drops it.
package imageinput

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
	"golang.org/x/image/riff"
	"golang.org/x/image/webp"
)

type MediaType string

const (
	PNG  MediaType = "image/png"
	JPEG MediaType = "image/jpeg"
	GIF  MediaType = "image/gif"
	WebP MediaType = "image/webp"
)

type Limits struct {
	MaxSourceBytes  int64
	MaxDecodePixels int // checked against the header, before decoding
	MaxDimension    int
	MaxPixels       int
	MaxEncodedBytes int // before base64
}

// MaxImages bounds the images attached to one message.
const MaxImages = 4

// DefaultLimits follows Anthropic's vision guidance.
var DefaultLimits = Limits{
	MaxSourceBytes:  30 << 20,
	MaxDecodePixels: 50_000_000,
	MaxDimension:    1568,
	MaxPixels:       1_150_000,
	MaxEncodedBytes: 3_750_000, // ≈5 MB once base64-encoded
}

// Data always sniffs as MediaType.
type Image struct {
	Name      string
	MediaType MediaType
	Data      []byte
	Width     int
	Height    int
	Resized   bool

	SourceWidth  int
	SourceHeight int
}

var (
	ErrNotImage        = errors.New("not a supported image file")
	ErrSourceTooLarge  = errors.New("image file too large")
	ErrDecode          = errors.New("image could not be decoded")
	ErrEncodedTooLarge = errors.New("encoded image too large")
	ErrTooManyImages   = fmt.Errorf("at most %d images can be attached", MaxImages)
)

// Error is an image that cannot be attached, named as the user knows it.
type Error struct {
	Name string
	Err  error
}

func (e *Error) Error() string {
	cause := e.Err
	if pathErr, ok := errors.AsType[*fs.PathError](cause); ok {
		cause = pathErr.Err // the name already says which file
	}
	return fmt.Sprintf("could not attach %q: %v", e.Name, cause)
}

func (e *Error) Unwrap() error { return e.Err }

// Sniff follows the WHATWG MIME Sniffing image patterns.
func Sniff(data []byte) (MediaType, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\xFF\xD8\xFF")):
		return JPEG, true
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1A\n")):
		return PNG, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return GIF, true
	case len(data) >= 14 && string(data[:4]) == "RIFF" && string(data[8:14]) == "WEBPVP":
		return WebP, true
	}
	return "", false
}

// Prepare validates images and downscales as needed, preserving EXIF.
// GIF first frames and resized WebP become PNG; animated WebP is unsupported.
// Other images within limits are returned byte for byte.
// EXIF dimensions/thumbnails may stay stale: encoded dimensions are authoritative,
// and unchanged orientation remains valid. Copying avoids a TIFF metadata editor.
func Prepare(name string, raw []byte, limits Limits) (Image, error) {
	mediaType, ok := Sniff(raw)
	if !ok {
		return Image{}, ErrNotImage
	}
	decodeConfig, decode := codec(mediaType)

	cfg, err := decodeConfig(bytes.NewReader(raw))
	if err != nil {
		return Image{}, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	if mediaType == WebP {
		header, err := webpChunk(raw, "VP8X")
		if err != nil && !errors.Is(err, io.EOF) {
			return Image{}, fmt.Errorf("%w: %w", ErrDecode, err)
		}
		if len(header) > 0 && header[0]&2 != 0 {
			return Image{}, fmt.Errorf("%w: animated WebP is unsupported", ErrNotImage)
		}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Image{}, ErrDecode
	}
	if int64(cfg.Width)*int64(cfg.Height) > int64(limits.MaxDecodePixels) {
		return Image{}, ErrSourceTooLarge
	}
	// DecodeConfig alone accepts complete headers with missing pixel data.
	src, err := decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	if mediaType != GIF && fits(limits, cfg.Width, cfg.Height) {
		if len(raw) > limits.MaxEncodedBytes {
			return Image{}, ErrEncodedTooLarge
		}
		return Image{
			Name:         name,
			MediaType:    mediaType,
			Data:         raw,
			Width:        cfg.Width,
			Height:       cfg.Height,
			SourceWidth:  cfg.Width,
			SourceHeight: cfg.Height,
		}, nil
	}

	if mediaType == GIF {
		// gif.Decode returns a frame rectangle; restore its offset on the full canvas.
		canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		opaque := true
		for _, c := range src.(*image.Paletted).Palette {
			if _, _, _, alpha := c.RGBA(); alpha == 0 {
				opaque = false
				break
			}
		}
		// Transparent GIFs keep a clear canvas, even if the transparent index is unused.
		// Byte 11 is the validated logical-screen background index, in the global palette.
		palette := cfg.ColorModel.(color.Palette)
		if opaque && int(raw[11]) < len(palette) {
			stddraw.Draw(canvas, canvas.Bounds(), image.NewUniform(palette[raw[11]]), image.Point{}, stddraw.Src)
		}
		stddraw.Draw(canvas, src.Bounds(), src, src.Bounds().Min, stddraw.Src)
		src = canvas
	}
	width, height := scaledSize(limits, src.Bounds().Dx(), src.Bounds().Dy())
	scaled := scale(src, width, height)
	data, outType, err := encode(scaled, mediaType)
	if err != nil {
		return Image{}, err
	}
	switch mediaType {
	case JPEG:
		data = preserveJPEGEXIF(data, raw)
	case PNG:
		data = appendPNGEXIF(data, pngEXIF(raw))
	case WebP:
		exif, _ := webpChunk(raw, "EXIF")
		// Strip any JPEG EXIF signature: PNG stores only the TIFF payload.
		data = appendPNGEXIF(data, bytes.TrimPrefix(exif, []byte("Exif\x00\x00")))
	case GIF:
		// No EXIF metadata.
	}
	if len(data) > limits.MaxEncodedBytes {
		return Image{}, ErrEncodedTooLarge
	}
	return Image{
		Name:         name,
		MediaType:    outType,
		Data:         data,
		Width:        width,
		Height:       height,
		Resized:      width != cfg.Width || height != cfg.Height,
		SourceWidth:  cfg.Width,
		SourceHeight: cfg.Height,
	}, nil
}

// Load caps both file size and bytes read, including files that grow during reading.
func Load(ctx context.Context, path string, limits Limits) (_ Image, err error) {
	defer func() {
		if err != nil {
			err = &Error{Name: filepath.Base(path), Err: err}
		}
	}()
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Image{}, err
	}
	if !info.Mode().IsRegular() {
		return Image{}, ErrNotImage
	}
	if info.Size() > limits.MaxSourceBytes {
		return Image{}, ErrSourceTooLarge
	}

	f, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limits.MaxSourceBytes+1))
	if err != nil {
		return Image{}, err
	}
	if int64(len(raw)) > limits.MaxSourceBytes {
		return Image{}, ErrSourceTooLarge
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	return Prepare(filepath.Base(path), raw, limits)
}

func fits(l Limits, width, height int) bool {
	return width <= l.MaxDimension && height <= l.MaxDimension &&
		int64(width)*int64(height) <= int64(l.MaxPixels)
}

// Enforce the pixel budget after clamping thin images to at least one pixel wide/tall.
func scaledSize(l Limits, width, height int) (int, int) {
	w, h := float64(width), float64(height)
	maxDim := float64(l.MaxDimension)
	factor := min(1, maxDim/w, maxDim/h, math.Sqrt(float64(l.MaxPixels)/(w*h)))
	width, height = max(1, int(w*factor)), max(1, int(h*factor))
	if int64(width)*int64(height) > int64(l.MaxPixels) {
		if width >= height {
			width = max(1, l.MaxPixels/height)
		} else {
			height = max(1, l.MaxPixels/width)
		}
	}
	return width, height
}

// codec selects the sniffed format's decoder regardless of registered codecs.
func codec(mediaType MediaType) (func(io.Reader) (image.Config, error), func(io.Reader) (image.Image, error)) {
	switch mediaType {
	case PNG:
		return png.DecodeConfig, png.Decode
	case JPEG:
		return jpeg.DecodeConfig, jpeg.Decode
	case GIF:
		return gif.DecodeConfig, gif.Decode
	default:
		return webp.DecodeConfig, webp.Decode
	}
}

func scale(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Src, nil)
	return dst
}

func encode(img image.Image, source MediaType) ([]byte, MediaType, error) {
	var buf bytes.Buffer
	var err error
	switch source {
	case JPEG:
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	default:
		source = PNG
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), source, nil
}

// image/jpeg has no metadata API; copy EXIF APP1 segments from the validated JPEG.
func preserveJPEGEXIF(encoded, original []byte) []byte {
	var exif []byte
	for pos := 2; pos < len(original); {
		if original[pos] != 0xff {
			break
		}
		for pos < len(original) && original[pos] == 0xff {
			pos++
		}
		if pos == len(original) {
			break
		}
		marker := original[pos]
		pos++
		if marker == 0xda || marker == 0xd9 { // scan data or end of image
			break
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd8 {
			continue // standalone markers have no length
		}
		if len(original)-pos < 2 {
			break
		}
		size := int(binary.BigEndian.Uint16(original[pos:]))
		if size < 2 || size > len(original)-pos {
			break
		}
		if marker == 0xe1 && bytes.HasPrefix(original[pos+2:pos+size], []byte("Exif\x00\x00")) {
			exif = append(exif, 0xff, marker)
			exif = append(exif, original[pos:pos+size]...)
		}
		pos += size
	}
	if len(exif) == 0 {
		return encoded
	}
	result := make([]byte, 0, len(encoded)+len(exif))
	result = append(result, encoded[:2]...) // SOI
	result = append(result, exif...)
	return append(result, encoded[2:]...)
}

// pngEXIF reads metadata from a PNG already validated by png.Decode.
func pngEXIF(raw []byte) []byte {
	for pos := 8; len(raw)-pos >= 12; {
		size := uint64(binary.BigEndian.Uint32(raw[pos:]))
		if size > uint64(len(raw)-pos-12) {
			break
		}
		end := pos + 8 + int(size)
		if string(raw[pos+4:pos+8]) == "eXIf" {
			return raw[pos+8 : end]
		}
		if string(raw[pos+4:pos+8]) == "IEND" {
			break
		}
		pos = end + 4
	}
	return nil
}

func appendPNGEXIF(encoded, exif []byte) []byte {
	if len(exif) == 0 {
		return encoded
	}
	// Place EXIF before pixels so streaming readers see orientation first.
	end := 8 + 12 + 13 // signature, chunk framing, IHDR payload
	result := make([]byte, 0, len(encoded)+12+len(exif))
	result = append(result, encoded[:end]...)
	result = binary.BigEndian.AppendUint32(result, uint32(len(exif)))
	result = append(result, "eXIf"...)
	result = append(result, exif...)
	result = binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[end+4:]))
	return append(result, encoded[end:]...)
}

// webpChunk delegates chunk framing and padding to the existing RIFF reader.
func webpChunk(raw []byte, kind string) ([]byte, error) {
	form, chunks, err := riff.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if string(form[:]) != "WEBP" {
		return nil, ErrDecode
	}
	for {
		id, _, data, err := chunks.Next()
		if err != nil {
			return nil, err
		}
		if string(id[:]) == kind {
			return io.ReadAll(data)
		}
	}
}
