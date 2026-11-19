package viz

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// This subprocess fixture exercises the real stdio transport on every platform.
func TestViewerHelper(t *testing.T) {
	if os.Getenv("DDVIZ_VERSION") != "bits-test-fixture" {
		return
	}
	if os.Getenv("DDVIZ_IPC") != "stdio" || os.Getenv("DDVIZ_OPEN") != "on-demand" || os.Getenv("DDVIZ_PARENT_PID") == "" {
		os.Exit(2)
	}
	fmt.Println(`{"jsonrpc":"2.0","id":"flags","method":"tools/call","params":{"name":"_dd_gff","arguments":{}}}`)
	// The frame sees the presented app as notifications, in order.
	var presented []string
	lookupFailed := false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg wireMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		if string(msg.ID) == `"flags"` {
			if msg.Error != nil {
				os.Exit(4)
			}
			fmt.Println(`{"jsonrpc":"2.0","method":"ui/notifications/initialized"}`)
		}
		if msg.ID == nil && msg.Method != "" {
			presented = append(presented, msg.Method+" "+string(msg.Params))
		}
		if msg.Method == "ui/notifications/tool-result" && string(msg.Params) == `{"pending":true}` {
			fmt.Println(`{"jsonrpc":"2.0","id":"pending","method":"tools/call","params":{"name":"pending","arguments":{}}}`)
		}
		if msg.Method == "ui/notifications/tool-result" && os.Getenv("DDVIZ_HEADLESS") == "1" {
			fmt.Println(`{"jsonrpc":"2.0","id":"lookup","method":"tools/call","params":{"name":"lookup","arguments":{}}}`)
		}
		if string(msg.ID) == `"lookup"` {
			lookupFailed = msg.Error == nil && string(msg.Result) == lookupError
		}
		if msg.Method == "snapshot" {
			reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]string{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(imageBytes())}}
			if !lookupFailed {
				reply = map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32000, "message": "app did not receive the failed lookup"}}
			} else if !slices.Equal(presented, presentedNotifications) {
				reply = map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32000, "message": fmt.Sprint(presented)}}
			}
			data, _ := json.Marshal(reply)
			fmt.Println(string(data))
		}
	}
	os.Exit(0)
}

const lookupError = `{"isError":true,"content":[{"type":"text","text":"lookup failed"}]}`

func fixtureToolResult(name string) json.RawMessage {
	if name == "lookup" {
		return json.RawMessage(lookupError)
	}
	return json.RawMessage(`{}`)
}

// presentedApp is what the fixture expects to see, as presentedNotifications.
var presentedApp = assistant.AppPayload{
	ToolCallID: "call", ToolName: "show_ui", ToolInput: json.RawMessage(`{"q":1}`),
	ResourceURI: DatavizURI, Result: json.RawMessage(`{"structuredContent":{}}`),
}

var presentedNotifications = []string{
	`ui/notifications/host-context-changed {"toolInfo":{"id":"call","tool":{"name":"show_ui","inputSchema":{"type":"object"}}}}`,
	`ui/notifications/tool-input {"arguments":{"q":1}}`,
	`ui/notifications/tool-result {"structuredContent":{}}`,
}

func TestStdioEndToEnd(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	config := Config{Version: "bits-test-fixture"}
	c := testClient(func(lifetime, startup context.Context, headless bool, tools toolCaller) (viewer, error) {
		return launchArgs(lifetime, startup, executable, []string{"-test.run=^TestViewerHelper$"}, config, headless, tools)
	})
	// The fixture calls a tool before it initializes, as the frame does.
	c.api = fakeAPI{call: func(_ context.Context, handle, name string, _ json.RawMessage) (json.RawMessage, error) {
		if handle == "h" && name == "_dd_gff" {
			callbacks.Add(1)
		}
		return fixtureToolResult(name), nil
	}}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := Request{App: presentedApp}
	r.App.RelayHandle = "h"
	result := c.Render(ctx, r)
	if result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	if result.Image.Width() != 2 || result.Image.Height() != 3 || callbacks.Load() != 1 {
		t.Fatalf("image=%+v callbacks=%d", result.Image, callbacks.Load())
	}
}

func TestSwitchingAppsCancelsPendingViewerCallbacks(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var launches int
	c := testClient(func(lifetime, startup context.Context, headless bool, tools toolCaller) (viewer, error) {
		launches++
		return launchArgs(lifetime, startup, executable, []string{"-test.run=^TestViewerHelper$"}, Config{Version: "bits-test-fixture"}, headless, tools)
	})
	defer func() { _ = c.Close() }()
	calls := make(chan string, 3)
	canceled := make(chan struct{})
	c.api = fakeAPI{call: func(ctx context.Context, handle, name string, _ json.RawMessage) (json.RawMessage, error) {
		calls <- handle + ":" + name
		if name == "pending" {
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		return json.RawMessage(`{}`), nil
	}}
	app := Request{App: presentedApp}
	app.App.RelayHandle = "first"
	app.App.Result = json.RawMessage(`{"pending":true}`)
	if result := c.Open(ctx, app); result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	for _, want := range []string{"first:_dd_gff", "first:pending"} {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("callback = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	app.App.RelayHandle = "second"
	app.App.Result = presentedApp.Result
	if result := c.Open(ctx, app); result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	if launches != 2 {
		t.Fatal("reused another app's viewer")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("old viewer's callback survived the app switch")
	}
	select {
	case got := <-calls:
		if got != "second:_dd_gff" {
			t.Fatalf("new viewer's callback = %q", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestExecLaunchFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		create  bool
		outcome Phase
		retry   RetryAdvice
	}{
		{"missing", false, Unavailable, RetryNotUseful},
		{"not-executable", true, Failed, RetryNotUseful},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.create && runtime.GOOS == "windows" {
				t.Skip("Unix executable permissions")
			}
			binary := filepath.Join(t.TempDir(), "ddviz")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			if tc.create {
				if err := os.WriteFile(binary, []byte("not an executable"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := launch(context.Background(), context.Background(), binary, Config{}, true, nil)
			if err == nil {
				t.Fatal("launch unexpectedly succeeded")
			}
			got := failure(err)
			if got.Phase != tc.outcome || got.Retry != tc.retry {
				t.Fatalf("status=%+v err=%v", got, err)
			}
		})
	}
}

func TestViewerOutlivesStartupContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tools := toolCaller(func(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
		return fixtureToolResult(name), nil
	})
	lifetime, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	startup, stopStartup := context.WithCancel(lifetime)
	defer stopStartup()

	viewer, err := launchArgs(lifetime, startup, executable, []string{"-test.run=^TestViewerHelper$"}, Config{Version: "bits-test-fixture"}, true, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.close()
	stopStartup()

	if err := viewer.present(presentedApp); err != nil {
		t.Fatal(err)
	}
	if _, err := viewer.snapshot(lifetime); err != nil {
		t.Fatalf("startup cancellation stopped initialized viewer: %v", err)
	}
}
