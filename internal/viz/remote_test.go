package viz

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync/atomic"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type fakeSnapshots struct {
	got assistant.SnapshotOptions
	err error
}

func (f *fakeSnapshots) RenderSnapshot(ctx context.Context, o assistant.SnapshotOptions) ([]byte, error) {
	f.got = o
	if f.err != nil {
		return nil, f.err
	}
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 22)))
	return b.Bytes(), nil
}

// blockingSnapshots renders until canceled.
type blockingSnapshots struct {
	started  chan struct{}
	returned atomic.Bool
}

func (b *blockingSnapshots) RenderSnapshot(ctx context.Context, _ assistant.SnapshotOptions) ([]byte, error) {
	close(b.started)
	<-ctx.Done()
	b.returned.Store(true)
	return nil, ctx.Err()
}

var timeseriesDef = assistant.WidgetDefinition{"type": "timeseries"}

func TestRemoteRender(t *testing.T) {
	api := &fakeSnapshots{}
	c := NewRemote(api)
	def := assistant.WidgetDefinition{"type": "timeseries", "time": map[string]any{"from": 1000.0, "to": 5000.0}}
	if r := c.Render(context.Background(), Request{Definition: def, Dark: true}); r.Status.Phase != Succeeded || r.Image.Interactive().Available {
		t.Fatalf("result = %+v", r)
	}
	if api.got.Start.UnixMilli() != 1000 || api.got.End.UnixMilli() != 5000 || !api.got.Dark {
		t.Fatalf("options = %+v", api.got)
	}
	for _, tc := range []struct {
		err   error
		phase Phase
		retry RetryAdvice
	}{
		{&assistant.APIError{StatusCode: 400, Detail: "Unknown widget type: x"}, Failed, RetryNotUseful},
		{&assistant.APIError{StatusCode: 403}, Unavailable, RetryNotUseful},
		{&assistant.APIError{StatusCode: 503}, Failed, RetryAllowed},
	} {
		api.err = tc.err
		if s := c.Render(context.Background(), Request{Definition: def}).Status; s.Phase != tc.phase || s.Retry != tc.retry {
			t.Errorf("%v: status = %+v", tc.err, s)
		}
	}
}

// Close cancels and joins running work, and nothing starts after it.
func TestRemoteClose(t *testing.T) {
	api := &blockingSnapshots{started: make(chan struct{})}
	c := NewRemote(api)
	done := make(chan Status, 1)
	go func() { done <- c.Render(context.Background(), Request{Definition: timeseriesDef}).Status }()
	<-api.started
	_ = c.Close()
	if !api.returned.Load() {
		t.Fatal("Close returned before its render")
	}
	if s := <-done; s.Phase != Canceled {
		t.Fatalf("running render = %+v", s)
	}
	if s := c.Render(context.Background(), Request{Definition: timeseriesDef}).Status; s.Phase != Canceled {
		t.Fatalf("render after Close = %+v", s)
	}
}
