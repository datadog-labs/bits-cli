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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		if msg.Method == "snapshot" {
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]string{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(imageBytes())}})
			fmt.Println(string(data))
		}
	}
	os.Exit(0)
}

func TestStdioEndToEnd(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	tools := toolFunc(func(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
		if name == "_dd_gff" {
			callbacks.Add(1)
			return json.RawMessage(`{}`), nil
		}
		return json.RawMessage(`{"structuredContent":{}}`), nil
	})
	config := Config{MCP: tools, Version: "bits-test-fixture"}
	c := testClient(func(lifetime, startup context.Context, headless bool) (viewer, error) {
		return launchArgs(lifetime, startup, executable, []string{"-test.run=^TestViewerHelper$"}, config, headless)
	})
	c.mcp = tools
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := c.Render(ctx, request(t))
	if result.Status.Phase != Succeeded {
		t.Fatal(result.Status)
	}
	if result.Image.Width() != 2 || result.Image.Height() != 3 || callbacks.Load() != 1 {
		t.Fatalf("image=%+v callbacks=%d", result.Image, callbacks.Load())
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
			_, err := launch(context.Background(), context.Background(), binary, Config{}, true)
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
	tools := toolFunc(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	lifetime, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	startup, stopStartup := context.WithCancel(lifetime)
	defer stopStartup()

	viewer, err := launchArgs(lifetime, startup, executable, []string{"-test.run=^TestViewerHelper$"}, Config{MCP: tools, Version: "bits-test-fixture"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.close()
	stopStartup()

	if err := viewer.present(&mcp.CallToolResult{StructuredContent: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := viewer.snapshot(lifetime); err != nil {
		t.Fatalf("startup cancellation stopped initialized viewer: %v", err)
	}
}
