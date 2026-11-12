package viz

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type controllerClient struct {
	render func(context.Context, Request) Result
	open   func(context.Context, Request) Result
}

func (c controllerClient) Render(ctx context.Context, request Request) Result {
	return c.render(ctx, request)
}

func (c controllerClient) Interactive() bool { return true }

func (c controllerClient) Open(ctx context.Context, request Request) Result {
	return c.open(ctx, request)
}

func (controllerClient) Close() error { return nil }

func TestControllerIndependentOperationsAndRetry(t *testing.T) {
	app := request(t).App
	var requests []Request
	attempts := 0
	client := controllerClient{
		render: func(_ context.Context, request Request) Result {
			requests = append(requests, request)
			attempts++
			if attempts == 1 {
				return Result{Status: Status{Phase: Failed, Retry: RetryAllowed}}
			}
			image, err := NewSnapshot(imageBytes(), Availability{Available: true})
			if err != nil {
				t.Fatal(err)
			}
			return Result{Status: Status{Phase: Succeeded}, Image: image}
		},
		open: func(ctx context.Context, request Request) Result {
			if err := ctx.Err(); err != nil {
				t.Fatalf("render canceled open: %v", err)
			}
			requests = append(requests, request)
			return Result{Status: Status{Phase: Succeeded}}
		},
	}
	chart := NewController(client, Request{App: app})
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
		if !reflect.DeepEqual(request.App, app) {
			t.Fatal("operation changed the app")
		}
	}
	late := chart.Open(context.Background())()
	chart.Close()
	if chart.Apply(late) || chart.State().CanOpen() || chart.State().CanRender() || chart.Open(context.Background()) != nil {
		t.Fatal("closed controller accepted work")
	}
	other := NewController(client, Request{App: app})
	defer other.Close()
	if other.Apply(late) {
		t.Fatal("new revision accepted old completion")
	}
}

// Later operations, retries included, reuse the app an operation restored,
// whatever its outcome: the client decides when to restore again.
func TestControllerReusesRestoredApp(t *testing.T) {
	live := assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{}`), RelayHandle: "old", Restorable: true}
	restored := assistant.AppPayload{ResourceURI: DatavizURI, Result: json.RawMessage(`{}`), RelayHandle: "new", Restorable: true}
	var handles []string
	client := controllerClient{
		render: func(_ context.Context, request Request) Result {
			handles = append(handles, request.App.RelayHandle)
			return Result{Status: Status{Phase: Failed, Retry: RetryAllowed}, App: &restored}
		},
		open: func(_ context.Context, request Request) Result {
			handles = append(handles, request.App.RelayHandle)
			return Result{Status: Status{Phase: Succeeded}}
		},
	}
	c := NewController(client, Request{App: live, ConversationID: "conv", MessageID: "msg"})
	defer c.Close()
	c.Apply(c.Render(context.Background())())
	c.Apply(c.Render(context.Background())()) // a retry
	c.Apply(c.Open(context.Background())())
	if !slices.Equal(handles, []string{"old", "new", "new"}) || c.App().RelayHandle != "new" {
		t.Fatalf("handles = %v, app = %+v", handles, c.App())
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
