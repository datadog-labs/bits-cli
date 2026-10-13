package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

func TestUserBlocksListTheContextTheyWereSentWith(t *testing.T) {
	engine := New(&scriptBackend{}, Config{})
	drain(engine.StartTurn(context.Background(), TurnInput{
		Message: "compare",
		Context: &assistant.AssistantContext{Entities: []assistant.ContextEntity{
			{Type: assistant.EntityDashboard, ID: "dash-1", Label: "Checkout overview"},
			{Type: assistant.EntityService, ID: "checkout-api"},
		}},
		Images: []imageinput.Image{shrunkScreenshot(t)},
	}))

	want := []ContextRef{
		{Type: assistant.EntityDashboard, Label: "Checkout overview"},
		{Type: assistant.EntityService, Label: "checkout-api"},
		{Type: assistant.EntityImageURL, Label: "shot.png"},
	}
	if got := engine.Snapshot()[0].Context; !reflect.DeepEqual(got, want) {
		t.Fatalf("user block context = %+v, want %+v", got, want)
	}
}

func TestRestoredUserBlocksListTheirContextWithoutImageData(t *testing.T) {
	user := assistant.Message{
		Role: "user", MessageID: "m1", Content: assistant.TextContent("compare"),
		ContextEntities: []byte(`[{"type":"service","id":"checkout-api"},{"type":"image_url","id":"data:image/png;base64,AAAA"}]`),
	}
	block, _ := NewTranscript().AppendMessage(user)

	want := []ContextRef{{Type: assistant.EntityService, Label: "checkout-api"}, {Type: assistant.EntityImageURL}}
	if !reflect.DeepEqual(block.Context, want) {
		t.Fatalf("restored context = %+v, want %+v", block.Context, want)
	}
}
