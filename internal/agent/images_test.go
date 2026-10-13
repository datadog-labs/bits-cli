package agent

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

func shrunkScreenshot(t *testing.T) imageinput.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 2))); err != nil {
		t.Fatal(err)
	}
	return imageinput.Image{
		Name: "shot.png", MediaType: imageinput.PNG, Data: buf.Bytes(),
		Width: 4, Height: 2, SourceWidth: 40, SourceHeight: 20,
	}
}

func TestImagesAreSentOnlyWithTheUserMessage(t *testing.T) {
	backend := &contextRecordingBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := assistant.ContextEntity{Type: assistant.EntityDashboard, ID: "dash-1"}
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{dashboard}}
	img := shrunkScreenshot(t)
	engine := New(backend, Config{})

	drain(engine.StartTurn(context.Background(), TurnInput{
		Message: "what is wrong?", Tools: tools, Context: turnContext, Images: []imageinput.Image{img},
	}))

	want := []assistant.ContextEntity{dashboard, assistant.ImageEntity("image/png", img.Data, "shot.png")}
	if len(backend.contexts) != 2 || backend.contexts[0] == nil || !reflect.DeepEqual(backend.contexts[0].Entities, want) {
		t.Fatalf("user message context = %+v, want %+v", backend.contexts, want)
	}
	if backend.contexts[1] != nil {
		t.Fatalf("tool answers carried context %+v", backend.contexts[1])
	}
	if len(turnContext.Entities) != 1 {
		t.Fatalf("caller context was mutated: %+v", turnContext.Entities)
	}
	if !strings.Contains(backend.userContexts[0], "resized from 40x20 to 4x2 pixels") || backend.userContexts[1] != "" {
		t.Fatalf("resize notice = %q", backend.userContexts)
	}
}

func TestAnImageTheBackendWouldDropFailsTheTurnBeforeSending(t *testing.T) {
	backend := &contextRecordingBackend{}
	engine := New(backend, Config{})
	img := imageinput.Image{Name: "fake.png", MediaType: imageinput.PNG, Data: []byte("not a png")}

	events := drain(engine.StartTurn(context.Background(), TurnInput{Message: "look", Images: []imageinput.Image{img}}))

	if last := events[len(events)-1]; !errors.Is(last.Err, ErrInvalidImage) {
		t.Fatalf("last event = %+v, want ErrInvalidImage", last)
	}
	if backend.calls != 0 {
		t.Fatalf("backend received %d requests", backend.calls)
	}
	if blocks := engine.Snapshot(); len(blocks) != 1 || blocks[0].Role != assistant.RoleUser {
		t.Fatalf("user block was not kept: %+v", blocks)
	}
}
