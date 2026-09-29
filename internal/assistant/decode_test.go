package assistant

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeJSONBodyAroundTheCap(t *testing.T) {
	pad := func(limit int64) string {
		prefix := `{"title":"`
		suffix := `"}`
		return prefix + strings.Repeat("a", int(limit)-len(prefix)-len(suffix)) + suffix
	}
	t.Run("under the cap decodes", func(t *testing.T) {
		var out struct {
			Title string `json:"title"`
		}
		if err := decodeJSONBody(&out, strings.NewReader(pad(1023)), 1024); err != nil {
			t.Fatalf("body just under the cap failed to decode: %v", err)
		}
	})
	t.Run("exactly at the cap decodes", func(t *testing.T) {
		var out struct {
			Title string `json:"title"`
		}
		if err := decodeJSONBody(&out, strings.NewReader(pad(1024)), 1024); err != nil {
			t.Fatalf("body exactly at the cap failed to decode: %v", err)
		}
	})
	t.Run("over the cap errors descriptively", func(t *testing.T) {
		var out struct {
			Title string `json:"title"`
		}
		err := decodeJSONBody(&out, strings.NewReader(pad(1025)), 1024)
		if err == nil || !strings.Contains(err.Error(), "response body exceeds 1024 bytes") {
			t.Fatalf("error = %v, want a descriptive size-cap error", err)
		}
		if !errors.Is(err, errResponseBodyTooLarge) {
			t.Fatalf("error = %v, want it to match errResponseBodyTooLarge", err)
		}
	})
}

func TestHistoryCapCoversStreamLines(t *testing.T) {
	if maxHistoryBodyBytes < defaultMaxLineBytes {
		t.Fatalf("history cap %d must not be smaller than the %d stream line cap",
			maxHistoryBodyBytes, defaultMaxLineBytes)
	}
}
