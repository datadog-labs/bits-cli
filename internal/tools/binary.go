package tools

import (
	"bytes"
	"context"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
)

const binaryProbeSize = 8192

// isBinary reports whether data is binary because it contains a null byte or
// invalid UTF-8.
func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

// errNotText explains which files the editor tools read as text.
const errNotText = "not UTF-8 or UTF-16 text (binary or a legacy code page)"

// decodeText returns data as UTF-8 text, a byte order mark kept as a leading
// U+FEFF, and the encoding that writes the text back to the same bytes: nil
// for UTF-8. ok is false for binary data. Only a BOM selects UTF-16: a
// BOM-less UTF-16 file or a legacy code page cannot be told from binary data.
func decodeText(data []byte) (text string, enc encoding.Encoding, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		enc = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		enc = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	default:
		if isBinary(data) {
			return "", nil, false
		}
		return string(data), nil, true
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil || isBinary(decoded) {
		return "", nil, false
	}
	// Decoding replaces malformed UTF-16 with U+FFFD, which an edit would then
	// write over bytes it did not change.
	if encoded, err := enc.NewEncoder().Bytes(decoded); err != nil || !bytes.Equal(encoded, data) {
		return "", nil, false
	}
	return string(decoded), enc, true
}

// encodeText encodes text for a file that decodeText read as enc.
func encodeText(text string, enc encoding.Encoding) ([]byte, error) {
	if enc == nil {
		return []byte(text), nil
	}
	return enc.NewEncoder().Bytes([]byte(text))
}

// probeBinary reads enough bytes to validate the first binaryProbeSize bytes
// without splitting a UTF-8 rune. It returns a reader that replays the probe
// before the unread remainder of r.
func probeBinary(ctx context.Context, r io.Reader) (io.Reader, bool, error) {
	probe := make([]byte, binaryProbeSize+utf8.UTFMax-1)
	n, err := io.ReadFull(contextReader{ctx: ctx, r: r}, probe)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, err
	}
	probe = probe[:n]

	end := min(len(probe), binaryProbeSize)
	for end < len(probe) && !utf8.RuneStart(probe[end]) {
		end++
	}
	if isBinary(probe[:end]) {
		return nil, true, nil
	}
	return io.MultiReader(bytes.NewReader(probe), r), false, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}
