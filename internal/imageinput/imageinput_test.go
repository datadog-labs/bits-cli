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
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gradient(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	return img
}

func noise(width, height int) *image.NRGBA {
	rng := rand.New(rand.NewPCG(1, 2))
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Uint32())
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pngHeader has valid dimensions but no pixel data.
func pngHeader(width, height uint32) []byte {
	ihdr := make([]byte, 0, 17)
	ihdr = append(ihdr, "IHDR"...)
	ihdr = binary.BigEndian.AppendUint32(ihdr, width)
	ihdr = binary.BigEndian.AppendUint32(ihdr, height)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // 8-bit truecolor
	out := []byte("\x89PNG\r\n\x1A\n")
	out = binary.BigEndian.AppendUint32(out, 13)
	out = append(out, ihdr...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(ihdr))
}

func TestContentDecidesTheFormatNotTheFileName(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		want    MediaType
		wantErr error
	}{
		{name: "a PNG named .jpg is a PNG", raw: encodePNG(t, gradient(8, 8)), want: PNG},
		{name: "text is not an image", raw: []byte("not an image at all"), wantErr: ErrNotImage},
		{name: "an empty file is not an image", raw: nil, wantErr: ErrNotImage},
		{name: "a PNG signature alone cannot be decoded", raw: []byte("\x89PNG\r\n\x1A\n"), wantErr: ErrDecode},
		{name: "an in-bounds PNG header without pixels is rejected", raw: pngHeader(1, 1), wantErr: ErrDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := Prepare(tt.name, tt.raw, DefaultLimits)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Prepare error = %v, want %v", err, tt.wantErr)
			}
			if img.MediaType != tt.want {
				t.Fatalf("MediaType = %q, want %q", img.MediaType, tt.want)
			}
		})
	}
}

func TestAnImageThatFitsIsSentUnchanged(t *testing.T) {
	raw := encodePNG(t, gradient(64, 32))
	img, err := Prepare("in-bounds.png", raw, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if img.Resized || img.MediaType != PNG || !bytes.Equal(img.Data, raw) || img.Width != 64 || img.Height != 32 {
		t.Fatalf("got Resized=%v MediaType=%q %dx%d identical=%v, want untouched 64x32 PNG",
			img.Resized, img.MediaType, img.Width, img.Height, bytes.Equal(img.Data, raw))
	}
}

func TestLargeImagesShrinkToFit(t *testing.T) {
	tests := []struct {
		name          string
		raw           []byte
		width, height int
		want          MediaType
		maxPixels     int
	}{
		{name: "a wide PNG is limited by its width", raw: encodePNG(t, gradient(4000, 1000)), width: 4000, height: 1000, want: PNG},
		{name: "a tall JPEG is limited by its height", raw: encodeJPEG(t, gradient(1000, 4000)), width: 1000, height: 4000, want: JPEG},
		{name: "a big square GIF becomes PNG and is limited by its pixel count", raw: encodeGIF(t, gradient(1500, 1500)), width: 1500, height: 1500, want: PNG},

		{name: "thin landscape respects custom pixel cap", raw: encodePNG(t, gradient(4000, 1)), width: 4000, height: 1, want: PNG, maxPixels: 100},
		{name: "thin portrait respects custom pixel cap", raw: encodePNG(t, gradient(1, 4000)), width: 1, height: 4000, want: PNG, maxPixels: 100},
		{name: "static WebP becomes PNG", raw: readFixture(t, "static.webp"), width: 80, height: 40, want: PNG, maxPixels: 800},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits
			if tt.maxPixels != 0 {
				limits.MaxPixels = tt.maxPixels
			}
			img, err := Prepare(tt.name, tt.raw, limits)
			if err != nil {
				t.Fatal(err)
			}
			if !img.Resized || img.MediaType != tt.want {
				t.Fatalf("got Resized=%v MediaType=%q, want resized %q", img.Resized, img.MediaType, tt.want)
			}
			if img.SourceWidth != tt.width || img.SourceHeight != tt.height {
				t.Fatalf("source size = %dx%d, want %dx%d", img.SourceWidth, img.SourceHeight, tt.width, tt.height)
			}
			if img.Width > limits.MaxDimension || img.Height > limits.MaxDimension || img.Width*img.Height > limits.MaxPixels {
				t.Fatalf("%dx%d exceeds limits", img.Width, img.Height)
			}
			if tt.width >= tt.height {
				if want := max(1, img.Width*tt.height/tt.width); img.Height < want-1 || img.Height > want+1 {
					t.Fatalf("height %d does not preserve aspect ratio (want ~%d)", img.Height, want)
				}
			} else {
				if want := max(1, img.Height*tt.width/tt.height); img.Width < want-1 || img.Width > want+1 {
					t.Fatalf("width %d does not preserve aspect ratio (want ~%d)", img.Width, want)
				}
			}
		})
	}
}

func TestGIFConversionPreservesCanvasPlacementAndTransparency(t *testing.T) {
	red, blue := color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}
	green, yellow := color.NRGBA{G: 255, A: 255}, color.NRGBA{R: 255, G: 255, A: 255}
	clear := color.NRGBA{}
	tests := []struct {
		name              string
		global, local     color.Palette
		backgroundIndex   byte
		unusedTransparent bool
		background, left  color.NRGBA
	}{
		{name: "opaque shared palette", global: color.Palette{blue, red}, local: color.Palette{blue, red}, background: blue, left: blue},
		{name: "opaque local palette", global: color.Palette{green, blue}, local: color.Palette{yellow, red}, backgroundIndex: 1, background: blue, left: yellow},
		{name: "transparent shared palette", global: color.Palette{clear, red}, local: color.Palette{clear, red}, backgroundIndex: 1, background: clear, left: clear},
		{name: "transparent local palette", global: color.Palette{green, blue}, local: color.Palette{clear, red}, backgroundIndex: 1, background: clear, left: clear},
		{name: "unused transparent index", global: color.Palette{green, blue}, local: color.Palette{clear, red}, backgroundIndex: 1, unusedTransparent: true, background: clear, left: red},
	}
	for _, tt := range tests {
		frame := image.NewPaletted(image.Rect(20, 10, 60, 30), tt.local)
		for y := 10; y < 30; y++ {
			for x := 20; x < 60; x++ {
				if x >= 40 || tt.unusedTransparent {
					frame.SetColorIndex(x, y, 1)
				}
			}
		}
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, &gif.GIF{
			Image:           []*image.Paletted{frame, image.NewPaletted(frame.Bounds(), tt.local)},
			Delay:           []int{10, 10},
			Config:          image.Config{Width: 100, Height: 40, ColorModel: tt.global},
			BackgroundIndex: tt.backgroundIndex,
		}); err != nil {
			t.Fatal(err)
		}
		for _, dimension := range []int{100, 50} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, dimension), func(t *testing.T) {
				limits := DefaultLimits
				limits.MaxDimension = dimension
				out, err := Prepare("animation.gif", buf.Bytes(), limits)
				if err != nil {
					t.Fatal(err)
				}
				if out.MediaType != PNG || out.Width != dimension || out.Height != dimension*40/100 || out.Resized != (dimension < 100) || out.SourceWidth != 100 || out.SourceHeight != 40 {
					t.Fatalf("unexpected conversion: type=%s size=%dx%d source=%dx%d resized=%v", out.MediaType, out.Width, out.Height, out.SourceWidth, out.SourceHeight, out.Resized)
				}
				img, err := png.Decode(bytes.NewReader(out.Data))
				if err != nil {
					t.Fatal(err)
				}
				for _, pixel := range []struct {
					p    image.Point
					want color.NRGBA
				}{
					{image.Pt(0, 0), tt.background},
					{image.Pt(30*dimension/100, 20*dimension/100), tt.left},
					{image.Pt(50*dimension/100, 20*dimension/100), red},
				} {
					if got := color.NRGBAModel.Convert(img.At(pixel.p.X, pixel.p.Y)); got != pixel.want {
						t.Fatalf("pixel %v = %v, want %v", pixel.p, got, pixel.want)
					}
				}
				limits.MaxEncodedBytes = len(out.Data) - 1
				if _, err := Prepare("animation.gif", buf.Bytes(), limits); !errors.Is(err, ErrEncodedTooLarge) {
					t.Fatalf("converted PNG did not enforce byte limit: %v", err)
				}
			})
		}
	}
}

// WebP fixtures: libwebp lossless, 80x40; static red or animated red/blue.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestImagesPreserveEXIF(t *testing.T) {
	// TIFF orientation tags: little-endian rotate 90, big-endian mirror horizontally.
	for _, exif := range [][]byte{
		[]byte("II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00"),
		[]byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x02\x00\x00\x00\x00\x00\x00"),
	} {
		jpegRaw := encodeJPEG(t, gradient(80, 40))
		segment := []byte{0xff, 0xe1}
		segment = binary.BigEndian.AppendUint16(segment, uint16(len(exif)+8))
		segment = append(segment, "Exif\x00\x00"...)
		segment = append(segment, exif...)
		// An unrelated APP1 must not hide EXIF.
		metadata := append([]byte{0xff, 0xe1, 0, 5, 'x', 'm', 'p'}, segment...)
		jpegRaw = append(append(append([]byte{}, jpegRaw[:2]...), metadata...), jpegRaw[2:]...)
		pngRaw := encodePNG(t, gradient(80, 40))
		pngChunk := binary.BigEndian.AppendUint32(nil, uint32(len(exif)))
		pngChunk = append(pngChunk, "eXIf"...)
		pngChunk = append(pngChunk, exif...)
		pngChunk = binary.BigEndian.AppendUint32(pngChunk, crc32.ChecksumIEEE(pngChunk[4:]))
		pngBefore := append(append(append([]byte{}, pngRaw[:33]...), pngChunk...), pngRaw[33:]...)
		pngAfter := append(append(append([]byte{}, pngRaw[:len(pngRaw)-12]...), pngChunk...), pngRaw[len(pngRaw)-12:]...)
		makeWebP := func(payload []byte) []byte {
			raw := readFixture(t, "static.webp")
			// Add VP8X with EXIF and the 80x40 canvas before VP8L.
			out := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x08\x00\x00\x00\x4f\x00\x00\x27\x00\x00"), raw[12:]...)
			// Odd-length chunks require RIFF padding.
			out = append(out, "JUNK\x01\x00\x00\x00x\x00EXIF"...)
			out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
			out = append(out, payload...)
			if len(payload)%2 != 0 {
				out = append(out, 0)
			}
			binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
			return out
		}
		tests := []struct {
			name           string
			raw            []byte
			source, output MediaType
		}{
			{"JPEG", jpegRaw, JPEG, JPEG},
			{"PNG before IDAT", pngBefore, PNG, PNG},
			{"PNG after IDAT", pngAfter, PNG, PNG},
			{"WebP", makeWebP(exif), WebP, PNG},
			{"WebP with EXIF signature", makeWebP(append([]byte("Exif\x00\x00"), exif...)), WebP, PNG},
		}
		for _, tt := range tests {
			for _, dimension := range []int{80, 40} {
				t.Run(fmt.Sprintf("%s/%s/%d", exif[:2], tt.name, dimension), func(t *testing.T) {
					limits := DefaultLimits
					limits.MaxDimension = dimension
					out, err := Prepare(tt.name, tt.raw, limits)
					if err != nil {
						t.Fatal(err)
					}
					wantType := tt.source
					if dimension < 80 {
						wantType = tt.output
					}
					if out.MediaType != wantType || out.Width != dimension || out.Height != dimension/2 || out.SourceWidth != 80 || out.SourceHeight != 40 || out.Resized != (dimension < 80) {
						t.Fatalf("unexpected output: type=%s size=%dx%d source=%dx%d resized=%v", out.MediaType, out.Width, out.Height, out.SourceWidth, out.SourceHeight, out.Resized)
					}
					if dimension == 80 {
						if !bytes.Equal(out.Data, tt.raw) {
							t.Fatal("in-bounds image changed")
						}
					} else {
						expected := pngChunk
						if wantType == JPEG {
							expected = segment
						}
						if !bytes.Contains(out.Data, expected) {
							t.Fatal("EXIF payload or container framing changed")
						}
						if bytes.Contains(out.Data, []byte("xmp")) {
							t.Fatal("unrelated APP1 retained")
						}
					}
					_, decode := codec(wantType)
					decoded, err := decode(bytes.NewReader(out.Data))
					if err != nil {
						t.Fatal(err)
					}
					if decoded.Bounds().Dx() != dimension || decoded.Bounds().Dy() != dimension/2 {
						t.Fatalf("wrong encoded dimensions: %v", decoded.Bounds())
					}
					limits.MaxEncodedBytes = len(out.Data) - 1
					if _, err := Prepare(tt.name, tt.raw, limits); !errors.Is(err, ErrEncodedTooLarge) {
						t.Fatalf("metadata not counted toward byte limit: %v", err)
					}
				})
			}
		}
	}
}

func TestAnimatedWebPIsUnsupportedAtEverySize(t *testing.T) {
	raw := readFixture(t, "animated.webp")
	for _, dimension := range []int{80, 40} {
		t.Run(fmt.Sprint(dimension), func(t *testing.T) {
			limits := DefaultLimits
			limits.MaxDimension = dimension
			_, err := Prepare("animation.webp", raw, limits)
			if !errors.Is(err, ErrNotImage) || !strings.Contains(err.Error(), "animated WebP") {
				t.Fatalf("expected explicit unsupported animation error, got %v", err)
			}
		})
	}
}

func TestAnImageClaimingHugeDimensionsIsRejectedBeforeDecoding(t *testing.T) {
	_, err := Prepare("bomb.png", pngHeader(100_000, 100_000), DefaultLimits)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("Prepare error = %v, want %v", err, ErrSourceTooLarge)
	}
}

func TestAnImageTooHeavyToSendIsRejected(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "it fits but weighs too much", raw: encodePNG(t, noise(128, 128))},
		{name: "it is still too heavy once shrunk", raw: encodePNG(t, noise(2000, 200))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits
			limits.MaxEncodedBytes = 1000
			_, err := Prepare("noise.png", tt.raw, limits)
			if !errors.Is(err, ErrEncodedTooLarge) {
				t.Fatalf("Prepare error = %v, want %v", err, ErrEncodedTooLarge)
			}
		})
	}
}

func TestLoadRefusesFilesItShouldNotRead(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.png")
	if err := os.WriteFile(large, encodePNG(t, gradient(64, 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{name: "a file over the source limit", path: large, wantErr: ErrSourceTooLarge},
		{name: "a directory", path: dir, wantErr: ErrNotImage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits
			limits.MaxSourceBytes = 64
			_, err := Load(context.Background(), tt.path, limits)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Load error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
