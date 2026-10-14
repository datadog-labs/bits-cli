package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestUserBlocksKeepTheContextTheyWereSentWith(t *testing.T) {
	engine := New(&scriptBackend{}, Config{})
	dashboard := assistant.ContextEntity{Type: assistant.EntityDashboard, ID: "dash-1", Label: "Checkout overview"}
	file, sent := screenshotFile(t)
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{dashboard, file}}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "compare", Context: turnContext}))

	want := []assistant.ContextEntity{dashboard, sent}
	if got := engine.Snapshot()[0].Context; !reflect.DeepEqual(got, want) {
		t.Fatalf("user block context = %+v, want %+v", got, want)
	}
}

func TestRestoredUserBlocksKeepTheirContext(t *testing.T) {
	user := assistant.Message{
		Role: "user", MessageID: "m1", Content: assistant.TextContent("compare"),
		ContextEntities: []byte(`[{"type":"service","id":"checkout-api"},{"type":"image_url","id":"data:image/png;base64,AAAA"}]`),
	}
	block, _ := NewTranscript().AppendMessage(user)

	want := []assistant.ContextEntity{
		{Type: assistant.EntityService, ID: "checkout-api"},
		{Type: assistant.EntityImageURL, ID: "data:image/png;base64,AAAA"},
	}
	if !reflect.DeepEqual(block.Context, want) {
		t.Fatalf("restored context = %+v, want %+v", block.Context, want)
	}
}
