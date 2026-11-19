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
	if api.got.Start.Unix() != 1 || api.got.End.Unix() != 5 || !api.got.Dark || string(api.got.Definition) != `{"type":"timeseries","title":"CPU"}` {
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
