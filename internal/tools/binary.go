package tools

import (
	"bytes"
	"context"
	"io"
	"unicode/utf8"
)

const binaryProbeSize = 8192

// isBinary reports whether data is binary because it contains a null byte or
// invalid UTF-8.
func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
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
