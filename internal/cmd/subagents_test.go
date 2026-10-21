package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubagentEnvironmentSelectsEndpoint(t *testing.T) {
	clearStagingEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("BITS_FAKE_BACKEND", "")
	t.Setenv("BITS_CUSTOM_HEADERS", "")
	t.Setenv("DD_API_KEY", "test-api-key")
	t.Setenv("DD_APP_KEY", "test-app-key")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, test := range []struct {
		name  string
		value string
		path  string
	}{
		{name: "default", path: "/api/v2/assistant"},
		{name: "enabled", value: "1", path: "/api/unstable/assistant/multi-agent/runs"},
		{name: "explicitly disabled", value: "0", path: "/api/v2/assistant"},
		{name: "other value", value: "true", path: "/api/v2/assistant"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BITS_USE_EXPERIMENTAL_SUBAGENTS", test.value)
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != test.path {
					t.Errorf("endpoint=%s, want %s", r.URL.Path, test.path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if test.path == "/api/v2/assistant" {
					_, _ = fmt.Fprintln(w, `{"data":{"type":"assistant-response","attributes":{"conversation_id":"11111111-1111-4111-8111-111111111111","structured_message":{"role":"assistant","message_id":"answer","content":{"type":"markdown_fragment","content":"answer"}}}}}`)
				} else {
					w.Header().Set("X-Assistant-Conversation-ID", "11111111-1111-4111-8111-111111111111")
					w.Header().Set("X-Assistant-Multi-Agent-Run-ID", "run")
					w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "root")
					_, _ = fmt.Fprintln(w, `data: {"conversation_id":"11111111-1111-4111-8111-111111111111","run_id":"run","agent_id":"root","sequence":1,"type":"content.fragment_received","event":{"fragment":"answer"}}`)
					_, _ = fmt.Fprintln(w, `data: {"conversation_id":"11111111-1111-4111-8111-111111111111","run_id":"run","agent_id":"root","sequence":2,"type":"run.terminal","event":{"outcome":"completed"}}`)
				}
			}))
			defer server.Close()
			// Trust only the fixture certificate and route the validated API host locally.
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "example.com"
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			http.DefaultTransport = transport
			args := []string{"run", "--prompt", "question", "--delivery", "adeep", "--auth", "api-key", "--site", "api.datadoghq.com"}
			out, _, err := executeForTest(t, args)
			if err != nil || requests != 1 || !strings.Contains(out, `"response":"answer"`) {
				t.Fatalf("err=%v requests=%d output=%s", err, requests, out)
			}
		})
	}
}
