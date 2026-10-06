package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Non-streaming response bodies are decoded under a size cap. History gets a
// larger cap: it aggregates whole rounds, and a single stream line may
// legitimately reach defaultMaxLineBytes, so the history cap must not be
// smaller than the line cap.
const (
	maxHistoryBodyBytes  = 256 << 20 // 256 MiB
	maxResponseBodyBytes = 8 << 20   // 8 MiB
)

// errResponseBodyTooLarge is reported once a body exceeds its cap.
var errResponseBodyTooLarge = errors.New("response body exceeds size cap")

// cappedReader reads up to limit bytes, then reports errResponseBodyTooLarge.
type cappedReader struct {
	r         io.Reader
	remaining int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errResponseBodyTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func decodeJSONBody(dst any, body io.Reader, limit int64) error {
	err := json.NewDecoder(&cappedReader{r: body, remaining: limit}).Decode(dst)
	if errors.Is(err, errResponseBodyTooLarge) {
		return fmt.Errorf("response body exceeds %d bytes: %w", limit, err)
	}
	return err
}
