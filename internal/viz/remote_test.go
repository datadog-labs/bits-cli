package viz

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type fakeSnapshots struct {
	fakeAPI
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
	fakeAPI
	started  chan struct{}
	returned atomic.Bool
}

func (b *blockingSnapshots) RenderSnapshot(ctx context.Context, _ assistant.SnapshotOptions) ([]byte, error) {
	close(b.started)
	<-ctx.Done()
	b.returned.Store(true)
	return nil, ctx.Err()
}

var timeseries = chartApp(`{"type":"timeseries"}`, 0, 0)

// Charts render over their result's window, other views have no preview, and
// errors keep the server's reason where it is the widget's fault.
func TestRemoteRender(t *testing.T) {
	api := &fakeSnapshots{}
	c := NewRemote(api)
	app := chartApp(`{"type":"timeseries","title":"CPU"}`, 1, 5)
	if r := c.Render(context.Background(), Request{App: app, Dark: true}); r.Status.Phase != Succeeded || r.Image.Interactive().Available {
		t.Fatalf("result = %+v", r)
	}
	if api.got.Start.Unix() != 1 || api.got.End.Unix() != 5 || !api.got.Dark || api.got.Definition.Title != "CPU" {
		t.Fatalf("options = %+v", api.got)
	}
	if r := c.Render(context.Background(), Request{App: timeseries}); r.Status.Phase != Succeeded || api.got.End.Sub(api.got.Start) != time.Hour {
		t.Fatalf("no window: result = %+v, options = %+v", r, api.got)
	}
	incident := assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{"_meta":{"datadog/show_ui":{"view_spec":{"view":{"name":"incident_detail"}}}}}`)}
	if s := c.Render(context.Background(), Request{App: incident}).Status; s.Phase != Failed || s.Retry != RetryNotUseful {
		t.Fatalf("incident view = %+v", s)
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
		if s := c.Render(context.Background(), Request{App: app}).Status; s.Phase != tc.phase || s.Retry != tc.retry {
			t.Errorf("%v: status = %+v", tc.err, s)
		}
	}
}

// Close cancels and joins running work, and nothing starts after it.
func TestRemoteClose(t *testing.T) {
	api := &blockingSnapshots{started: make(chan struct{})}
	c := NewRemote(api)
	done := make(chan Status, 1)
	go func() { done <- c.Render(context.Background(), Request{App: timeseries}).Status }()
	<-api.started
	_ = c.Close()
	if !api.returned.Load() {
		t.Fatal("Close returned before its render")
	}
	if s := <-done; s.Phase != Canceled {
		t.Fatalf("running render = %+v", s)
	}
	if s := c.Render(context.Background(), Request{App: timeseries}).Status; s.Phase != Canceled {
		t.Fatalf("render after Close = %+v", s)
	}
}

// Without a panel, Open restores a saved app for the caller's web fallback,
// which only charts have.
func TestRemoteOpenRestores(t *testing.T) {
	restored := chartApp(`{"type":"timeseries","title":"CPU"}`, 0, 0)
	c := NewRemote(&fakeSnapshots{fakeAPI: fakeAPI{restore: func(string, string) (assistant.AppPayload, error) {
		return restored, nil
	}}})
	defer func() { _ = c.Close() }()
	saved := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Restorable: true}, ConversationID: "conv", MessageID: "msg"}
	result := c.Open(context.Background(), saved)
	if result.Status.Phase != Unavailable || result.App == nil || string(result.App.Result) != string(restored.Result) {
		t.Fatalf("open = %+v", result)
	}
	// Only charts open on the web.
	if result := c.Open(context.Background(), request(t)); result.Status != notShown || result.App != nil {
		t.Fatalf("view open = %+v", result)
	}
}
