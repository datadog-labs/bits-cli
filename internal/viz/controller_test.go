package viz

import (
	"context"
	"reflect"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type controllerClient struct {
	render func(context.Context, Request) RenderResult
	open   func(context.Context, Request) Status
}

func (c controllerClient) Render(ctx context.Context, request Request) RenderResult {
	return c.render(ctx, request)
}

func (c controllerClient) Open(ctx context.Context, request Request) Status {
	return c.open(ctx, request)
}

func (controllerClient) Close() error { return nil }

func TestControllerIndependentOperationsAndRetry(t *testing.T) {
	definition := assistant.WidgetDefinition{"type": "timeseries", "reflect": map[string]any{"live_span": "1h"}}
	var requests []Request
	attempts := 0
	client := controllerClient{
		render: func(_ context.Context, request Request) RenderResult {
			requests = append(requests, request)
			attempts++
			if attempts == 1 {
				return RenderResult{Status: Status{Phase: Failed, Retry: RetryAllowed}}
			}
			image, err := NewSnapshot(imageBytes(), Availability{Available: true})
			if err != nil {
				t.Fatal(err)
			}
			return RenderResult{Status: Status{Phase: Succeeded}, Image: image}
		},
		open: func(ctx context.Context, request Request) Status {
			if err := ctx.Err(); err != nil {
				t.Fatalf("render canceled open: %v", err)
			}
			requests = append(requests, request)
			return Status{Phase: Succeeded}
		},
	}
	chart := NewController(client, Request{Definition: definition})
	defer chart.Close()
	open := chart.Open(context.Background()) // No preview is required.
	render := chart.Render(context.Background())
	if open == nil || render == nil || chart.Render(context.Background()) != nil || chart.Open(context.Background()) != nil {
		t.Fatal("pending operations must not be scheduled twice")
	}
	chart.Apply(render())
	retry := chart.Render(context.Background())
	if retry == nil || chart.State().Open.Phase != Loading {
		t.Fatal("retry disturbed pending open")
	}
	chart.Apply(retry())
	firstOpen := open()
	chart.Apply(firstOpen)
	state := chart.State()
	if state.CanRender() || !state.CanOpen() || state.Image == nil {
		t.Fatal("incorrect completed state")
	}
	secondOpen := chart.Open(context.Background())
	if chart.Apply(firstOpen) {
		t.Fatal("accepted obsolete open completion")
	}
	chart.Apply(secondOpen())
	if chart.State().Image != state.Image {
		t.Fatal("opening discarded the snapshot")
	}
	for _, request := range requests {
		if !reflect.DeepEqual(request.Definition, definition) {
			t.Fatal("operation changed the definition")
		}
	}
	late := chart.Open(context.Background())()
	chart.Close()
	if chart.Apply(late) || chart.State().CanOpen() || chart.State().CanRender() || chart.Open(context.Background()) != nil {
		t.Fatal("closed controller accepted work")
	}
	other := NewController(client, Request{Definition: definition})
	defer other.Close()
	if other.Apply(late) {
		t.Fatal("new revision accepted old completion")
	}
}

func TestControllerActionRules(t *testing.T) {
	for _, phase := range []Phase{Failed, Canceled} {
		state := State{Render: Status{Phase: phase}, Open: Status{Phase: phase}}
		if state.CanRender() || state.CanOpen() {
			t.Fatal("nonretryable action enabled")
		}
		state.Render.Retry, state.Open.Retry = RetryAllowed, RetryAllowed
		if !state.CanRender() || !state.CanOpen() {
			t.Fatal("retryable action disabled")
		}
	}
}
