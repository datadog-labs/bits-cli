package viz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/datadogmcp"
	"github.com/datadog-labs/bits-cli/internal/version"
)

// fakeAPI restores apps with restore and relays tool calls to call.
type fakeAPI struct {
	restore func(conversationID, messageID string) (assistant.AppPayload, error)
	call    func(ctx context.Context, handle, name string, arguments json.RawMessage) (json.RawMessage, error)
}

func (f fakeAPI) RestoreApp(_ context.Context, conversationID, messageID string) (assistant.AppPayload, error) {
	if f.restore == nil {
		return assistant.AppPayload{}, errors.New("unexpected restore")
	}
	return f.restore(conversationID, messageID)
}

func (f fakeAPI) CallAppTool(ctx context.Context, handle, name string, arguments json.RawMessage) (json.RawMessage, error) {
	if f.call == nil {
		return json.RawMessage(`{}`), nil
	}
	return f.call(ctx, handle, name, arguments)
}

type fakeViewer struct {
	closes     atomic.Int32
	dead       atomic.Bool
	snapshotFn func(context.Context) ([]byte, error)
	presented  assistant.AppPayload
	tools      toolCaller
}

func (f *fakeViewer) present(app assistant.AppPayload) error {
	f.presented = app
	return nil
}
func (f *fakeViewer) show() error { return nil }
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
	c.api = fakeAPI{}
	return c
}

func TestClientReuseAndSeparatePanel(t *testing.T) {
	var modes []bool
	var launched []*fakeViewer
	c := testClient(func(_, _ context.Context, headless bool, _ toolCaller) (viewer, error) {
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
	if r := c.Open(context.Background(), w); r.Status.Phase != Succeeded {
		t.Fatal(r)
	}
	if len(modes) != 2 || !modes[0] || modes[1] {
		t.Fatal(modes)
	}
	launched[1].dead.Store(true) // The user quit the interactive helper.
	if r := c.Open(context.Background(), w); r.Status.Phase != Succeeded {
		t.Fatal(r)
	}
	if len(modes) != 3 || modes[2] || launched[1].closes.Load() != 1 {
		t.Fatalf("exited interactive viewer was reused: modes=%v closes=%d", modes, launched[1].closes.Load())
	}
	w.App.RelayHandle = "another-session"
	if r := c.Render(context.Background(), w); r.Status.Phase != Succeeded {
		t.Fatal(r)
	}
	if r := c.Open(context.Background(), w); r.Status.Phase != Succeeded {
		t.Fatal(r)
	}
	if len(modes) != 5 || !modes[3] || modes[4] || launched[0].closes.Load() != 1 || launched[2].closes.Load() != 1 {
		t.Fatalf("viewers survived a relay session change: modes=%v", modes)
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
	c := testClient(func(context.Context, context.Context, bool, toolCaller) (viewer, error) {
		launches++
		if launches == 1 {
			return first, nil
		}
		return &fakeViewer{}, nil
	})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Result, 1)
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
	c := testClient(func(context.Context, context.Context, bool, toolCaller) (viewer, error) {
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
	c := testClient(func(context.Context, context.Context, bool, toolCaller) (viewer, error) {
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
		c.launch = func(context.Context, context.Context, bool, toolCaller) (viewer, error) {
			return nil, &startError{err: err}
		}
		for _, got := range []Status{c.Render(context.Background(), request(t)).Status, c.Open(context.Background(), request(t)).Status} {
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
	client := testClientConcurrency(func(_, _ context.Context, headless bool, _ toolCaller) (viewer, error) {
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
	results := make(chan Result, 3)
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
	if status := client.Open(ctx, widget); status.Status.Phase != Succeeded {
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
	if status := client.Open(ctx, widget); status.Status.Phase != Succeeded {
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
	client := testClientConcurrency(func(_, _ context.Context, _ bool, _ toolCaller) (viewer, error) {
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
	results := make(chan Result, 3)
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
	client := testClientConcurrency(func(_, _ context.Context, _ bool, _ toolCaller) (viewer, error) {
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
	results := make(chan Result, 2)
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

func TestLegacyAndRelayViewersKeepTheirCallbackRoutes(t *testing.T) {
	const def = `{"type":"timeseries","title":"CPU","time":{"type":"fixed","from":1700000000123,"to":1700000030456}}`
	legacy := legacyWidgetRequest(t, def)
	var viewers []*fakeViewer
	c := testClient(func(_, _ context.Context, _ bool, tools toolCaller) (viewer, error) {
		v := &fakeViewer{tools: tools}
		viewers = append(viewers, v)
		return v, nil
	})
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	appCalls, legacyCalls := 0, 0
	c.api = fakeAPI{
		restore: func(string, string) (assistant.AppPayload, error) {
			return assistant.AppPayload{}, &assistant.APIError{StatusCode: 403, Detail: "MCP apps are not enabled"}
		},
		call: func(_ context.Context, handle, name string, _ json.RawMessage) (json.RawMessage, error) {
			if handle != "h" || name != "_dd_wfd" {
				t.Fatalf("relayed %q via %q", name, handle)
			}
			appCalls++
			return json.RawMessage(`{}`), nil
		},
	}
	c.callLegacyTool = func(_ context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		switch name {
		case "get_widget":
			want := `{"widget_definition":` + def + `,"from_ts":"1700000000","to_ts":"1700000031"}`
			if string(input) != want {
				t.Fatalf("get_widget input = %s, want %s", input, want)
			}
			return json.RawMessage(`{"structuredContent":{"widget_definition":` + def + `}}`), nil
		case "_dd_wfd":
			legacyCalls++
			return json.RawMessage(`{}`), nil
		default:
			return nil, errors.New("unexpected legacy call " + name)
		}
	}
	ctx := context.Background()
	app := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{}`), RelayHandle: "h"}}
	saved := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Restorable: true}, ConversationID: "conv", MessageID: "app"}
	for i, req := range []Request{legacy, legacy, app, saved, legacy} {
		result := c.Open(ctx, req)
		if req.CanRestore() {
			if status := result.Status; status.Phase != Failed || status.Retry != RetryNotUseful || status.Reason != "MCP apps are not enabled" {
				t.Fatalf("restore with app gate off: %+v", status)
			}
			continue
		}
		if result.Status.Phase != Succeeded {
			t.Fatalf("open %d: %+v", i, result.Status)
		}
		if _, err := viewers[len(viewers)-1].tools(ctx, "_dd_wfd", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(viewers) != 3 || viewers[0].closes.Load() != 1 || viewers[1].closes.Load() != 1 ||
		viewers[0].presented.ToolName != "get_widget" || appCalls != 1 || legacyCalls != 3 {
		t.Fatalf("viewers=%d, app callbacks=%d, legacy callbacks=%d", len(viewers), appCalls, legacyCalls)
	}
}

func legacyWidgetRequest(t *testing.T, definition string) Request {
	t.Helper()
	var content assistant.Content
	if err := json.Unmarshal([]byte(`{"type":"widget_def","widget_def":`+definition+`}`), &content); err != nil {
		t.Fatal(err)
	}
	return Request{LegacyWidget: content.Widget}
}

func TestLegacyWidgetLive(t *testing.T) {
	if os.Getenv("BITS_VIZ_E2E") != "1" || !Supported() {
		t.Skip("set BITS_VIZ_E2E=1 with a Bits login and built ddviz helper")
	}
	api := widgetE2EAPI(t)
	legacy, err := datadogmcp.New(api)
	if err != nil {
		t.Fatal(err)
	}
	c := New(Config{API: api, CallLegacyTool: legacy.CallToolJSON, FrameURL: api.BaseURL + "/api/unstable/dataviz-mcp-ui", Version: version.Build()})
	defer func() {
		if err := errors.Join(c.Close(), legacy.Close()); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request := legacyWidgetRequest(t, `{"type":"timeseries","title":"CPU","requests":[{"q":"avg:system.cpu.user{*}","display_type":"line"}],"time":{"type":"live","value":4,"unit":"hour"}}`)
	result := c.Render(ctx, request)
	if result.Status.Phase != Succeeded || result.Image == nil {
		t.Fatalf("legacy ddviz render: %+v", result.Status)
	}
	t.Logf("captured %dx%d PNG (%d bytes) through direct MCP", result.Image.Width(), result.Image.Height(), len(result.Image.PNG()))
}

// TestLive restores a saved app, named as conversation/message in
// BITS_VIZ_E2E_APP, and snapshots it through its relay session.
func TestLive(t *testing.T) {
	saved := os.Getenv("BITS_VIZ_E2E_APP")
	if os.Getenv("BITS_VIZ_E2E") != "1" || saved == "" {
		t.Skip("set BITS_VIZ_E2E=1 and BITS_VIZ_E2E_APP=conversation/message with a Bits login and built ddviz helper")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("ddviz requires macOS")
	}
	conversation, message, ok := strings.Cut(saved, "/")
	if !ok {
		t.Fatal("BITS_VIZ_E2E_APP must be conversation/message")
	}
	api := widgetE2EAPI(t)
	c := New(Config{API: api, FrameURL: api.BaseURL + "/api/unstable/dataviz-mcp-ui", Version: version.Build()})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Restorable: true}, ConversationID: conversation, MessageID: message}
	result := c.Render(ctx, request)
	if result.Status.Phase != Succeeded {
		t.Fatalf("live render: %+v", result.Status)
	}
	if result.Image == nil || len(result.Image.PNG()) == 0 || result.App == nil {
		t.Fatal("missing image or restored app")
	}
	t.Logf("captured %dx%d PNG (%d bytes) through the app relay", result.Image.Width(), result.Image.Height(), len(result.Image.PNG()))
}

func TestSnapshotLive(t *testing.T) {
	if os.Getenv("BITS_VIZ_E2E") != "1" {
		t.Skip("set BITS_VIZ_E2E=1 with a Bits login that has snapshot access")
	}
	api := widgetE2EAPI(t)
	c := NewRemote(api)
	defer func() { _ = c.Close() }()
	app := chartApp(`{"type":"timeseries","requests":[{"q":"avg:system.cpu.user{*}","display_type":"line"}]}`, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result := c.Render(ctx, Request{App: app})
	if result.Status.Phase != Succeeded {
		t.Fatalf("snapshot render: %+v", result.Status)
	}
	if result.Image == nil || len(result.Image.PNG()) == 0 {
		t.Fatal("missing snapshot image")
	}
	decoded, err := png.Decode(bytes.NewReader(result.Image.PNG()))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := decoded.At(0, 0).RGBA()
	dark := c.Render(ctx, Request{App: app, Dark: true})
	if dark.Status.Phase != Succeeded || dark.Image == nil {
		t.Fatalf("dark snapshot render: %+v", dark.Status)
	}
	darkImage, err := png.Decode(bytes.NewReader(dark.Image.PNG()))
	if err != nil {
		t.Fatal(err)
	}
	darkR, darkG, darkB, darkA := darkImage.At(0, 0).RGBA()
	if darkR >= r || darkG >= g || darkB >= b || darkA != a {
		t.Fatalf("dark theme did not darken background: light=%d,%d,%d,%d dark=%d,%d,%d,%d", r>>8, g>>8, b>>8, a>>8, darkR>>8, darkG>>8, darkB>>8, darkA>>8)
	}
	t.Logf("captured %dx%d PNG (%d bytes) through Datadog snapshots", result.Image.Width(), result.Image.Height(), len(result.Image.PNG()))
}

func widgetE2EAPI(t *testing.T) *assistant.Client {
	t.Helper()
	source, err := auth.NewSource(context.Background(), auth.DefaultStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	api, err := assistant.NewOAuthClient(source.Site(), source)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// A saved app is restored before it takes a viewer. Its frame calls tools
// through the restored relay session, from launch on, and the client returns
// the restored app for reuse.
func TestRestoresSavedAppAndRelaysToolCalls(t *testing.T) {
	restored := assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{"structuredContent":{}}`), RelayHandle: "fresh", Restorable: true}
	var viewers []*fakeViewer
	c := testClient(func(_, _ context.Context, _ bool, tools toolCaller) (viewer, error) {
		v := &fakeViewer{tools: tools}
		viewers = append(viewers, v)
		return v, nil
	})
	defer func() { _ = c.Close() }()
	var handles []string
	c.api = fakeAPI{
		restore: func(conversationID, messageID string) (assistant.AppPayload, error) {
			if conversationID != "conv" || messageID != "msg" {
				t.Fatalf("restored %s/%s", conversationID, messageID)
			}
			return restored, nil
		},
		call: func(_ context.Context, handle, name string, _ json.RawMessage) (json.RawMessage, error) {
			handles = append(handles, handle+":"+name)
			return json.RawMessage(`{}`), nil
		},
	}
	saved := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Restorable: true}, ConversationID: "conv", MessageID: "msg"}
	result := c.Render(context.Background(), saved)
	if result.Status.Phase != Succeeded || result.App == nil || result.App.RelayHandle != "fresh" {
		t.Fatalf("result = %+v", result)
	}
	if string(viewers[0].presented.Result) != string(restored.Result) {
		t.Fatalf("presented %s", viewers[0].presented.Result)
	}
	if _, err := viewers[0].tools(context.Background(), "_dd_gff", nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(handles, []string{"fresh:_dd_gff"}) {
		t.Fatalf("relayed %v", handles)
	}

	c.api = fakeAPI{restore: func(string, string) (assistant.AppPayload, error) {
		return assistant.AppPayload{}, &assistant.APIError{StatusCode: 410, Detail: "Original application call is unavailable"}
	}}
	if s := c.Render(context.Background(), saved).Status; s.Phase != Failed || s.Retry != RetryNotUseful || s.Reason != "Original application call is unavailable" {
		t.Fatalf("refused restore = %+v", s)
	}
}

// An expired relay session fails the panel's callbacks after Open succeeded,
// so the next Open restores the app for a new session, as the first does for
// a live app sent without one.
func TestExpiredRelayRestoresOnNextOpen(t *testing.T) {
	var viewers []*fakeViewer
	c := testClient(func(_, _ context.Context, _ bool, tools toolCaller) (viewer, error) {
		v := &fakeViewer{tools: tools}
		viewers = append(viewers, v)
		return v, nil
	})
	defer func() { _ = c.Close() }()
	fresh := assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{"structuredContent":{}}`), RelayHandle: "fresh", Restorable: true}
	restores := 0
	c.api = fakeAPI{
		restore: func(string, string) (assistant.AppPayload, error) {
			restores++
			return fresh, nil
		},
		call: func(_ context.Context, handle, _ string, _ json.RawMessage) (json.RawMessage, error) {
			if handle == "old" {
				return nil, assistant.ErrGone
			}
			return json.RawMessage(`{}`), nil
		},
	}
	live := Request{
		App:            assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{}`), RelayHandle: "old", Restorable: true},
		ConversationID: "conv", MessageID: "msg",
	}
	unrelayed := live
	unrelayed.App.RelayHandle = ""
	if result := c.Open(context.Background(), unrelayed); result.Status.Phase != Succeeded || result.App == nil || result.App.RelayHandle != "fresh" || restores != 1 {
		t.Fatalf("open without relay = %+v, restores = %d", result, restores)
	}
	if result := c.Open(context.Background(), live); result.Status.Phase != Succeeded || result.App != nil {
		t.Fatalf("first open = %+v", result)
	}
	if _, err := viewers[1].tools(context.Background(), "_dd_gff", nil); !errors.Is(err, assistant.ErrGone) {
		t.Fatalf("callback err = %v", err)
	}
	result := c.Open(context.Background(), live)
	if result.Status.Phase != Succeeded || result.App == nil || result.App.RelayHandle != "fresh" || restores != 2 {
		t.Fatalf("reopen = %+v, restores = %d", result, restores)
	}

	// A live view that was never saved cannot reopen with the same expired
	// handle. Its result still provides the chart definition for the browser.
	unsaved := live
	unsaved.App.Restorable = false
	result = c.Open(context.Background(), unsaved)
	if result.Status.Phase != Failed || result.Status.Retry != RetryNotUseful || result.App == nil ||
		result.App.RelayHandle != "" || !bytes.Equal(result.App.Result, unsaved.App.Result) || len(viewers) != 3 || restores != 2 {
		t.Fatalf("reopen unsaved view = %+v, viewers = %d, restores = %d", result, len(viewers), restores)
	}
}

func TestRelayExpiresWhileRendering(t *testing.T) {
	for _, tc := range []struct {
		name       string
		restorable bool
		retry      RetryAdvice
	}{
		{name: "saved chart can retry", restorable: true, retry: RetryAllowed},
		{name: "unsaved chart loses only its local relay", retry: RetryNotUseful},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := chartApp(`{"type":"timeseries","title":"CPU"}`, 10, 20)
			live.Restorable = tc.restorable
			fresh := live
			fresh.RelayHandle = "fresh"
			var callbacks []string
			var restores int
			c := testClient(func(_, _ context.Context, _ bool, tools toolCaller) (viewer, error) {
				return &fakeViewer{snapshotFn: func(ctx context.Context) ([]byte, error) {
					_, err := tools(ctx, "_dd_wfd", nil)
					if err != nil && !errors.Is(err, assistant.ErrGone) {
						t.Fatal(err)
					}
					// The helper captures an image even when the chart's data call fails.
					return imageBytes(), nil
				}}, nil
			})
			defer func() {
				if err := c.Close(); err != nil {
					t.Error(err)
				}
			}()
			c.api = fakeAPI{
				restore: func(conversationID, messageID string) (assistant.AppPayload, error) {
					if conversationID != "conv" || messageID != "msg" {
						t.Fatalf("restore identity = %s/%s", conversationID, messageID)
					}
					restores++
					return fresh, nil
				},
				call: func(_ context.Context, handle, _ string, _ json.RawMessage) (json.RawMessage, error) {
					callbacks = append(callbacks, handle)
					if handle == live.RelayHandle {
						return nil, assistant.ErrGone
					}
					return json.RawMessage(`{}`), nil
				},
			}

			ctx := context.Background()
			request := Request{App: live, ConversationID: "conv", MessageID: "msg"}
			result := c.Render(ctx, request)
			if result.Status.Phase != Failed || result.Status.Retry != tc.retry || result.Image != nil {
				t.Fatalf("preview with expired relay = %+v", result)
			}
			if !tc.restorable {
				if result.App == nil || result.App.RelayHandle != "" || !bytes.Equal(result.App.Result, live.Result) ||
					restores != 0 || !slices.Equal(callbacks, []string{live.RelayHandle}) {
					t.Fatalf("unsaved app = %+v, callbacks = %v, restores = %d", result.App, callbacks, restores)
				}
				return
			}
			result = c.Render(ctx, request)
			if result.Status.Phase != Succeeded || result.Image == nil || result.App == nil || result.App.RelayHandle != fresh.RelayHandle ||
				restores != 1 || !slices.Equal(callbacks, []string{live.RelayHandle, fresh.RelayHandle}) {
				t.Fatalf("preview after retry = %+v, callbacks = %v, restores = %d", result, callbacks, restores)
			}
		})
	}
}

// A live app with no relay session it could restore cannot load its data, so
// it fails without a retry and without taking a viewer.
func TestUnrelayedAppIsNotShown(t *testing.T) {
	c := testClient(func(_, _ context.Context, _ bool, _ toolCaller) (viewer, error) {
		t.Fatal("launched a viewer")
		return nil, nil
	})
	defer func() { _ = c.Close() }()
	app := Request{App: assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{}`)}}
	if r := c.Render(context.Background(), app); r.Status != notShown {
		t.Fatalf("render = %+v", r.Status)
	}
	if r := c.Open(context.Background(), app); r.Status != notShown {
		t.Fatalf("open = %+v", r.Status)
	}
}
