package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

// screenshotFile writes a PNG too wide to send unchanged and returns its file
// URL entity and the entity it is sent as.
func screenshotFile(t *testing.T) (file, sent assistant.ContextEntity) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 3200, 10))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := imageinput.Load(context.Background(), path, imageinput.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	file = assistant.ContextEntity{Type: assistant.EntityImageURL, ID: imageinput.FileURL(path), Label: "shot.png"}
	return file, assistant.ImageEntity(string(img.MediaType), img.Data, "shot.png")
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
	file, sent := screenshotFile(t)
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{dashboard, file}}
	engine := New(backend, Config{})

	drain(engine.StartTurn(context.Background(), TurnInput{Message: "what is wrong?", Tools: tools, Context: turnContext}))

	want := []assistant.ContextEntity{dashboard, sent}
	if len(backend.contexts) != 2 || backend.contexts[0] == nil || !reflect.DeepEqual(backend.contexts[0].Entities, want) {
		t.Fatalf("user message context = %+v, want %+v", backend.contexts, want)
	}
	if backend.contexts[1] != nil {
		t.Fatalf("tool answers carried context %+v", backend.contexts[1])
	}
	if !reflect.DeepEqual(turnContext.Entities, []assistant.ContextEntity{dashboard, file}) {
		t.Fatalf("caller context was mutated: %+v", turnContext.Entities)
	}
	if !strings.Contains(backend.userContexts[0], "Image 1 of 1 attached to this message was resized from 3200x10 to 1568x4 pixels") || backend.userContexts[1] != "" {
		t.Fatalf("resize notice = %q", backend.userContexts)
	}
}

func TestAFileThatIsNotAnImageFailsTheTurnBeforeSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.png")
	if err := os.WriteFile(path, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := assistant.ContextEntity{Type: assistant.EntityImageURL, ID: imageinput.FileURL(path), Label: "fake.png"}
	backend := &contextRecordingBackend{}
	engine := New(backend, Config{})

	events := drain(engine.StartTurn(context.Background(), TurnInput{
		Message: "look", Context: &assistant.AssistantContext{Entities: []assistant.ContextEntity{file}},
	}))

	if last := events[len(events)-1]; !errors.Is(last.Err, imageinput.ErrNotImage) {
		t.Fatalf("last event = %+v, want ErrNotImage", last)
	}
	if backend.calls != 0 {
		t.Fatalf("backend received %d requests", backend.calls)
	}
	// The failed prompt keeps the file reference, so recalling it retries.
	if blocks := engine.Snapshot(); len(blocks) != 1 || !reflect.DeepEqual(blocks[0].Context, []assistant.ContextEntity{file}) {
		t.Fatalf("user block = %+v", blocks)
	}
}

func TestPrepareImageContextPreservesOrderAndDeduplicates(t *testing.T) {
	file, sent := screenshotFile(t)
	dashboard := assistant.ContextEntity{Type: assistant.EntityDashboard, ID: "dash-1"}
	recalled := assistant.ContextEntity{Type: assistant.EntityImageURL, ID: "data:image/png;base64,AAAA", Label: "previous"}
	input := &assistant.AssistantContext{Entities: []assistant.ContextEntity{recalled, dashboard, file, file, sent}}

	prepared, notices, err := prepareImageContext(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []assistant.ContextEntity{recalled, dashboard, sent}; !reflect.DeepEqual(prepared.Entities, want) {
		t.Fatalf("context = %+v, want %+v", prepared.Entities, want)
	}
	// A recalled image has no resize to report but keeps its position.
	if len(notices) != 2 || notices[0].Resized || !notices[1].Resized {
		t.Fatalf("notice images = %+v", notices)
	}
}

func TestTooManyImagesFailTheTurnBeforeSending(t *testing.T) {
	backend := &contextRecordingBackend{}
	engine := New(backend, Config{})
	var entities []assistant.ContextEntity
	for i := range imageinput.MaxImages + 1 {
		entities = append(entities, assistant.ContextEntity{Type: assistant.EntityImageURL, ID: fmt.Sprint("data:image/png;base64,", i)})
	}
	events := drain(engine.StartTurn(context.Background(), TurnInput{Message: "look", Context: &assistant.AssistantContext{Entities: entities}}))
	if backend.calls != 0 {
		t.Fatal("too many images reached the backend")
	}
	if last := events[len(events)-1]; !errors.Is(last.Err, imageinput.ErrTooManyImages) {
		t.Fatalf("last event = %+v", last)
	}
}
