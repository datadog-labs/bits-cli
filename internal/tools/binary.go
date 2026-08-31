package tools

import (
	"bytes"
	"context"
	"io"
)

const binaryProbeSize = 8192

// isBinary reports whether data is binary by scanning the first binaryProbeSize
// bytes for null bytes. Files beyond the probe limit are treated as text.
func isBinary(data []byte) bool {
	probe := data
	if len(probe) > binaryProbeSize {
		probe = probe[:binaryProbeSize]
	}
	return bytes.IndexByte(probe, 0) >= 0
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
