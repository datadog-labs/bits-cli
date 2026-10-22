package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestMultiAgentUsesRegularAPIForUnsupportedTurns(t *testing.T) {
	// Noise produces a valid screenshot larger than the run API's context cap.
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(random.Uint32())
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	imageContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{assistant.ImageEntity("image/png", encoded.Bytes(), "screenshot")}}
	for _, test := range []struct {
		name    string
		config  Config
		context *assistant.AssistantContext
	}{
		{name: "reasoning mode", config: Config{InferenceMode: "deep"}},
		{name: "large screenshot", context: imageContext},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/api/v2/assistant" {
					t.Errorf("path=%s, want regular API", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				if r.Header.Get("test-drive-ds-rl") != "1" {
					t.Error("routing header missing")
				}
				var body assistant.Request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Data.Attributes.InferenceMode != test.config.InferenceMode {
					t.Error("reasoning mode changed")
				}
				if test.context != nil && !reflect.DeepEqual(body.Data.Attributes.Context, test.context) {
					t.Error("image context changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintln(w, `{"data":{"type":"assistant-response","attributes":{"conversation_id":"conversation","structured_message":{"role":"assistant","message_id":"answer","content":{"type":"markdown_fragment","content":"answer"}}}}}`)
			}))
			defer server.Close()
			client := assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client(), CustomHeaders: http.Header{"Test-Drive-Ds-Rl": []string{"1"}}})
			engine := New(client, test.config)
			tools, err := NewToolSet()
			if err != nil {
				t.Fatal(err)
			}
			events := collect(turnEvents(engine, context.Background(), TurnInput{Message: "question", Context: test.context, Tools: tools}))
			terminal := events[len(events)-1]
			if terminal.Err != nil || terminal.Kind != EventDone || requests != 1 {
				t.Fatalf("terminal=%+v requests=%d", terminal, requests)
			}
		})
	}
}

type imageDuplexBackend struct {
	*duplexBackend
	opts assistant.SendOptions
}

func (b *imageDuplexBackend) Stream(ctx context.Context, _ string, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) error {
	b.opts = opts
	return b.stream(ctx, emit)
}

func TestDuplexUsesPreparedImageAndResizeNotice(t *testing.T) {
	file, sent := screenshotFile(t)
	backend := &imageDuplexBackend{duplexBackend: &duplexBackend{stream: func(context.Context, func(assistant.AssistantResponse) error) error { return nil }}}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	tools, err := NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	events := collect(turnEvents(engine, context.Background(), TurnInput{Message: "describe", Tools: tools, Context: &assistant.AssistantContext{Entities: []assistant.ContextEntity{file}}}))
	terminal := events[len(events)-1]
	if terminal.Err != nil || terminal.Kind != EventDone {
		t.Fatalf("terminal=%+v", terminal)
	}
	if backend.opts.Context == nil || !reflect.DeepEqual(backend.opts.Context.Entities, []assistant.ContextEntity{sent}) || !strings.Contains(backend.opts.CustomUserContext, "resized from 3200x10 to 1568x4") {
		t.Fatalf("prepared image or resize notice missing: %+v", backend.opts)
	}
}
