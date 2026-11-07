package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

const testView = "/api/v2/snapshot/view/authenticated/30d/org/1-id.png"

func snapshotServer(t *testing.T, viewURL string, view http.HandlerFunc) (*Client, *int) {
	posts := new(int)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/snapshot" {
			*posts++
			raw, _ := io.ReadAll(r.Body)
			var body snapshotRequest
			a := &body.Data.Attributes
			if json.Unmarshal(raw, &body) != nil || body.Data.Type != "create_snapshot" || a.Start != 1000 || a.Config.Theme != "dark" {
				t.Errorf("envelope = %s", raw)
			}
			_, _ = io.WriteString(w, `{"data":{"attributes":{"url":"`+viewURL+`"}}}`)
			return
		}
		view(w, r)
	})
	c.MaxRetries = 2
	return c, posts
}

var testSnapshot = SnapshotOptions{Definition: WidgetDefinition{"type": "timeseries"}, Start: time.UnixMilli(1000), End: time.UnixMilli(2000), Dark: true}

// The view is fetched from the API host, whatever host the response names,
// through the pending document to a PNG.
func TestRenderSnapshot(t *testing.T) {
	gets := 0
	c, posts := snapshotServer(t, "https://elsewhere.example"+testView, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testView || r.Header.Get("DD-API-KEY") != "api" {
			t.Errorf("GET %s", r.URL)
		}
		if gets++; gets == 1 {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			_, _ = io.WriteString(w, `{"data":{"attributes":{"status":"pending"}}}`)
			return
		}
		w.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = io.WriteString(w, "png")
	})
	if data, err := c.RenderSnapshot(context.Background(), testSnapshot); err != nil || string(data) != "png" || *posts != 1 {
		t.Fatalf("RenderSnapshot = %q, %v after %d posts", data, err, *posts)
	}
}

// Creation is never retried; a failed view is not mistaken for missing access.
func TestRenderSnapshotErrors(t *testing.T) {
	c, posts := snapshotServer(t, testView, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if _, err := c.RenderSnapshot(context.Background(), testSnapshot); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("view 404 = %v", err)
	}
	busy := testClient(t, func(w http.ResponseWriter, _ *http.Request) { *posts++; w.WriteHeader(http.StatusServiceUnavailable) })
	busy.MaxRetries = 2
	*posts = 0
	if _, err := busy.RenderSnapshot(context.Background(), testSnapshot); !errors.Is(err, ErrServer) || *posts != 1 {
		t.Fatalf("create 503 = %v after %d posts", err, *posts)
	}
}

func TestSnapshotViewURLs(t *testing.T) {
	for _, u := range []string{"", "https://x/api/v2/other.png", "https://x/api/v2/snapshot/view/../../v1/validate", "https://x/api/v2/snapshot/view/%2e%2e/%2e%2e/v1/x.png"} {
		c, _ := snapshotServer(t, u, func(http.ResponseWriter, *http.Request) { t.Errorf("fetched %q", u) })
		if _, err := c.RenderSnapshot(context.Background(), testSnapshot); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
}
