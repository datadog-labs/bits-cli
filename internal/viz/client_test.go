package viz

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/datadogmcp"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolFunc func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func (f toolFunc) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	args, err := json.Marshal(params.Arguments)
	if err != nil {
		return nil, err
	}
	raw, err := f(ctx, params.Name, args)
	if err != nil {
		return nil, err
	}
	var result mcp.CallToolResult
	err = json.Unmarshal(raw, &result)
	return &result, err
}

type fakeViewer struct {
	closes     atomic.Int32
	dead       atomic.Bool
	snapshotFn func(context.Context) ([]byte, error)
}

func (f *fakeViewer) present(*mcp.CallToolResult) error { return nil }
func (f *fakeViewer) show() error                       { return nil }
func (f *fakeViewer) snapshot(ctx context.Context) ([]byte, error) {
	if f.snapshotFn != nil {
		return f.snapshotFn(ctx)
	}
	return imageBytes(), nil
}
func (f *fakeViewer) close()       { f.closes.Add(1) }
func (f *fakeViewer) exited() bool { return f.dead.Load() }
func testClient(start launchFunc) *localClient {
	return testClientConcurrency(start, 1)
}

func testClientConcurrency(start launchFunc, concurrency int) *localClient {
	c := newLocal(start, concurrency)
	c.mcp = toolFunc(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"structuredContent":{"widget":{}}}`), nil
	})
	return c
}

func TestClientReuseAndSeparatePanel(t *testing.T) {
	var modes []bool
	var launched []*fakeViewer
	c := testClient(func(_, _ context.Context, headless bool) (viewer, error) {
		modes = append(modes, headless)
		v := &fakeViewer{}
		launched = append(launched, v)
		return v, nil
	})
	defer func() { _ = c.Close() }()
	w := request(t)
	for range 2 {
		r := c.Render(context.Background(), w)
		if r.Status.Phase != Succeeded || r.Image.Width() != 2 || r.Image.Height() != 3 {
			t.Fatalf("%+v", r)
		}
	}
	if r := c.Open(context.Background(), w); r.Phase != Succeeded {
		t.Fatal(r)
	}
	if len(modes) != 2 || !modes[0] || modes[1] {
		t.Fatal(modes)
	}
	launched[1].dead.Store(true) // The user quit the interactive helper.
	if r := c.Open(context.Background(), w); r.Phase != Succeeded {
		t.Fatal(r)
	}
	if len(modes) != 3 || modes[2] || launched[1].closes.Load() != 1 {
		t.Fatalf("exited interactive viewer was reused: modes=%v closes=%d", modes, launched[1].closes.Load())
	}
}

func TestCancellationRetiresViewerAndQueueIsCancellable(t *testing.T) {
	entered := make(chan struct{})
	first := &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	var launches int
	c := testClient(func(context.Context, context.Context, bool) (viewer, error) {
		launches++
		if launches == 1 {
			return first, nil
		}
		return &fakeViewer{}, nil
	})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan RenderResult, 1)
	w := request(t)
	go func() { result <- c.Render(ctx, w) }()
	<-entered
	queued, stop := context.WithCancel(context.Background())
	stop()
	if r := c.Render(queued, w); r.Status.Phase != Canceled {
		t.Fatal(r)
	}
	cancel()
	if r := <-result; r.Status.Phase != Canceled {
		t.Fatal(r)
	}
	if r := c.Render(context.Background(), w); r.Status.Phase != Succeeded {
		t.Fatal(r)
	}
	if launches != 2 || first.closes.Load() != 1 {
		t.Fatalf("launches=%d closes=%d", launches, first.closes.Load())
	}
}

func TestCloseCancelsActiveWork(t *testing.T) {
	entered := make(chan struct{})
	c := testClient(func(context.Context, context.Context, bool) (viewer, error) {
		return &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}, nil
	})
	var wg sync.WaitGroup
	wg.Add(1)
	w := request(t)
	go func() {
		defer wg.Done()
		c.Render(context.Background(), w)
	}()
	<-entered
	_ = c.Close()
	wg.Wait()
	if r := c.Render(context.Background(), w); r.Status.Phase != Canceled {
		t.Fatal(r)
	}
}

func TestUnavailableAndInvalidWidget(t *testing.T) {
	c := testClient(func(context.Context, context.Context, bool) (viewer, error) {
		t.Fatal("unexpected launch")
		return nil, nil
	})
	defer func() { _ = c.Close() }()
	for err, want := range map[error]Status{
		os.ErrNotExist:               {Phase: Unavailable},
		syscall.ENOEXEC:              {Phase: Unavailable}, // built for another system
		os.ErrPermission:             {Phase: Failed, Retry: RetryNotUseful},
		syscall.EAGAIN:               {Phase: Failed, Retry: RetryAllowed},   // transient
		errors.New("broken install"): {Phase: Failed, Retry: RetryNotUseful}, // no known cause
	} {
		c.launch = func(context.Context, context.Context, bool) (viewer, error) {
			return nil, &startError{err: err}
		}
		for _, got := range []Status{c.Render(context.Background(), request(t)).Status, c.Open(context.Background(), request(t))} {
			if got.Phase != want.Phase || got.Retry != want.Retry {
				t.Fatal(err, got)
			}
		}
	}

	if r := c.Render(context.Background(), Request{}); r.Status.Phase != Failed || r.Status.Retry != RetryNotUseful {
		t.Fatal(r)
	}
}

func TestRendererPoolConcurrencyAndInteractiveIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var renders, opens atomic.Int32
	client := testClientConcurrency(func(_, _ context.Context, headless bool) (viewer, error) {
		if !headless {
			opens.Add(1)
			return &fakeViewer{}, nil
		}
		renders.Add(1)
		return &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return imageBytes(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}, nil
	}, 0)
	defer func() { _ = client.Close() }()
	widget := request(t)
	results := make(chan RenderResult, 3)
	for range 2 {
		go func() { results <- client.Render(ctx, widget) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("renderers did not run concurrently")
		}
	}

	queued, cancelQueued := context.WithCancel(ctx)
	go func() { results <- client.Render(queued, widget) }()
	cancelQueued()
	if result := <-results; result.Status.Phase != Canceled {
		t.Fatalf("queued request: %+v", result.Status)
	}
	// The interactive viewer has its own slot even when all renderers are busy.
	if status := client.Open(ctx, widget); status.Phase != Succeeded {
		t.Fatal(status)
	}
	if renders.Load() != 2 || opens.Load() != 1 {
		t.Fatalf("started %d hidden and %d interactive viewers", renders.Load(), opens.Load())
	}

	close(release)
	for range 2 {
		if result := <-results; result.Status.Phase != Succeeded {
			t.Fatal(result.Status)
		}
	}
	if result := client.Render(ctx, widget); result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	if status := client.Open(ctx, widget); status.Phase != Succeeded {
		t.Fatal(status)
	}
	if renders.Load() != 2 || opens.Load() != 1 {
		t.Fatal("successful viewers were not reused")
	}
}

func TestCloseDrainsRendererPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	viewers := make(chan *fakeViewer, 2)
	client := testClientConcurrency(func(_, _ context.Context, _ bool) (viewer, error) {
		viewer := &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		viewers <- viewer
		return viewer, nil
	}, 2)
	defer func() { _ = client.Close() }()
	widget := request(t)
	results := make(chan RenderResult, 3)
	for range 2 {
		go func() { results <- client.Render(ctx, widget) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("renderers did not start")
		}
	}
	go func() { results <- client.Render(ctx, widget) }()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if result := <-results; result.Status.Phase != Canceled {
			t.Fatal(result.Status)
		}
	}
	for range 2 {
		if viewer := <-viewers; viewer.closes.Load() != 1 {
			t.Fatalf("viewer closed %d times", viewer.closes.Load())
		}
	}
}

func TestCancelRetiresOnlyItsRenderer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	viewers := make(chan *fakeViewer, 3)
	var launches atomic.Int32
	client := testClientConcurrency(func(_, _ context.Context, _ bool) (viewer, error) {
		launches.Add(1)
		viewer := &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return imageBytes(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
		viewers <- viewer
		return viewer, nil
	}, 2)
	defer func() { _ = client.Close() }()
	widget := request(t)
	firstContext, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	results := make(chan RenderResult, 2)
	go func() { results <- client.Render(firstContext, widget) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first renderer did not start")
	}
	first := <-viewers
	go func() { results <- client.Render(ctx, widget) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("second renderer did not start")
	}
	second := <-viewers
	cancelFirst()
	if result := <-results; result.Status.Phase != Canceled {
		t.Fatal(result.Status)
	}
	if first.closes.Load() != 1 || second.closes.Load() != 0 {
		t.Fatal("cancellation did not isolate the active renderer")
	}
	close(release)
	if result := <-results; result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	// The retired slot was returned first; its next user starts a replacement.
	if result := client.Render(ctx, widget); result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	if launches.Load() != 3 || second.closes.Load() != 0 {
		t.Fatal("failed renderer was not replaced independently")
	}
}

func TestLive(t *testing.T) {
	if os.Getenv("BITS_VIZ_E2E") != "1" {
		t.Skip("set BITS_VIZ_E2E=1 with a Bits login and built ddviz helper")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("ddviz requires macOS")
	}
	store := auth.DefaultStore()
	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	source, err := auth.NewSource(session, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	api, err := assistant.NewOAuthClient(source.Site(), source)
	if err != nil {
		t.Fatal(err)
	}
	client, err := datadogmcp.New(api)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	c := New(Config{MCP: client, FrameURL: api.BaseURL + "/api/unstable/dataviz-mcp-ui", Version: version.Build()})
	defer func() { _ = c.Close() }()
	definition := assistant.WidgetDefinition{
		"type":     "timeseries",
		"requests": []any{map[string]any{"q": "avg:system.cpu.user{*}", "display_type": "line"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result := c.Render(ctx, Request{Definition: definition})
	if result.Status.Phase != Succeeded {
		t.Fatalf("live render: %+v", result.Status)
	}
	if result.Image == nil || len(result.Image.PNG()) == 0 {
		t.Fatal("missing image")
	}
	t.Logf("captured %dx%d PNG (%d bytes) through authenticated MCP", result.Image.Width(), result.Image.Height(), len(result.Image.PNG()))
}

func TestFetchWidgetPreservesDefinition(t *testing.T) {
	timing := map[string]any{"type": "fixed", "from": 1700000000000, "to": 1700003600000, "hide_incomplete_cost_data": true}
	definition := assistant.WidgetDefinition{"type": "timeseries", "time": timing}
	request := Request{Definition: definition}
	client := testClient(nil)
	defer func() { _ = client.Close() }()
	client.mcp = toolFunc(func(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
		expected, err := json.Marshal(map[string]any{"widget_definition": definition})
		if err != nil {
			t.Fatal(err)
		}
		if name != "get_widget" || string(args) != string(expected) {
			t.Fatalf("unexpected call: %s %s", name, args)
		}
		return json.RawMessage(`{"structuredContent":{}}`), nil
	})
	if _, status := client.fetchWidget(context.Background(), request); status.Phase != Succeeded {
		t.Fatal(status)
	}
	if len(timing) != 4 || definition["time"].(map[string]any)["type"] != "fixed" {
		t.Fatal("fetch modified the borrowed definition")
	}
}
