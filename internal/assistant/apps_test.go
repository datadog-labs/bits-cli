package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestRestoreApp(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		reply   string
		want    string // relay handle
		wantErr error
	}{
		{name: "restored", status: http.StatusOK, reply: `{"type":"mcp_app","tool_call_id":"c1","tool_name":"show_ui","tool_input":{},"resource_uri":"ui://apps/dataviz","relay_handle":"new","tool_result":{"content":[]},"restoration":{"type":"mcp_app_saved"}}`, want: "new"},
		{name: "not an app", status: http.StatusOK, reply: `{"type":"markdown_fragment","content":"x"}`},
		{name: "no longer available", status: http.StatusForbidden, reply: `{"errors":[{"status":"403","title":"Application producer is no longer available"}]}`, wantErr: ErrForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != "/api/unstable/assistant/mcp-apps/restore" || string(raw) != `{"conversation_id":"conv","message_id":"msg"}` {
					t.Errorf("%s %s %s", r.Method, r.URL.Path, raw)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.reply)
			})
			app, err := c.RestoreApp(context.Background(), "conv", "msg")
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.want == "":
				if err == nil {
					t.Fatalf("app = %+v, want an error", app)
				}
			case err != nil || app.RelayHandle != tt.want || !app.Restorable || string(app.Result) != `{"content":[]}`:
				t.Fatalf("app = %+v, %v", app, err)
			}
		})
	}
}

func TestCallAppTool(t *testing.T) {
	for _, tt := range []struct {
		name      string
		arguments json.RawMessage
		sent      string
		status    int
		wantErr   error
	}{
		{name: "arguments", arguments: json.RawMessage(`{"q":1}`), sent: `{"relay_handle":"h","name":"_dd_wfd","arguments":{"q":1}}`, status: http.StatusOK},
		{name: "null arguments", arguments: json.RawMessage(`null`), sent: `{"relay_handle":"h","name":"_dd_wfd","arguments":{}}`, status: http.StatusOK},
		{name: "no arguments", sent: `{"relay_handle":"h","name":"_dd_wfd","arguments":{}}`, status: http.StatusOK},
		{name: "expired", sent: `{"relay_handle":"h","name":"_dd_wfd","arguments":{}}`, status: http.StatusGone, wantErr: ErrGone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/api/unstable/assistant/mcp-apps/call-tool" || string(raw) != tt.sent {
					t.Errorf("%s %s", r.URL.Path, raw)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
			})
			result, err := c.CallAppTool(context.Background(), "h", "_dd_wfd", tt.arguments)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || string(result) != `{"content":[{"type":"text","text":"ok"}]}` {
				t.Fatalf("result = %s, %v", result, err)
			}
		})
	}
}
