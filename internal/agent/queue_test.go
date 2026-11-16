package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func queuedMessages(s State) []string {
	var messages []string
	for _, q := range s.Queued {
		messages = append(messages, q.Message)
	}
	return messages
}

// Prompts queued during a turn join its run: the run sends them as one
// request once the turn completes, and ends once.
func TestQueueJoinsTheRunningTurn(t *testing.T) {
	backend := &scriptBackend{convID: "c", started: make(chan struct{}), hold: make(chan struct{})}
	e := New(backend, Config{})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	run, err := e.StartTurn(t.Context(), TurnInput{Message: "one"})
	if err != nil {
		t.Fatal(err)
	}
	<-backend.started
	for _, message := range []string{"two", "three"} {
		if err := e.Queue(TurnInput{Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	if got := queuedMessages(e.State()); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("queued = %q", got)
	}
	close(backend.hold)

	var terminals []EventKind
	round := 0
	var last State
	for ev := range Follow(events, run) {
		if ev.Round < round && ev.Round != 0 {
			t.Fatalf("round moved backwards from %d to %d", round, ev.Round)
		}
		round = max(round, ev.Round)
		if ev.Terminal() {
			terminals = append(terminals, ev.Kind)
		}
		if ev.Kind == EventState {
			last = ev.State
		}
	}
	if !reflect.DeepEqual(terminals, []EventKind{EventDone}) {
		t.Fatalf("terminal events = %v, want one EventDone", terminals)
	}
	if got := backend.userMessages("c"); !reflect.DeepEqual(got, []string{"one", "two\n\nthree"}) {
		t.Fatalf("sent = %q", got)
	}
	if round != 2 {
		t.Fatalf("last round = %d, want 2", round)
	}
	if len(last.Queued) != 0 || len(last.UserPrompts()) != 2 {
		t.Fatalf("final state queued %q, prompts %+v", queuedMessages(last), last.UserPrompts())
	}
}

// A cancelled turn leaves its queue waiting for SendQueued, and so does a
// prompt queued once no turn runs.
func TestCancelledTurnPausesTheQueue(t *testing.T) {
	backend := &scriptBackend{convID: "c", started: make(chan struct{}), hold: make(chan struct{})}
	e := New(backend, Config{})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(t.Context())
	run, err := e.StartTurn(ctx, TurnInput{Message: "one"})
	if err != nil {
		t.Fatal(err)
	}
	<-backend.started
	if err := e.Queue(TurnInput{Message: "two"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	for ev := range Follow(events, run) {
		if ev.Terminal() && ev.Kind != EventCanceled {
			t.Fatalf("terminal = %v, want EventCanceled", ev.Kind)
		}
	}
	close(backend.hold)

	if err := e.Queue(TurnInput{Message: "three"}); err != nil || e.OperationActive() {
		t.Fatalf("Queue while idle = %v, started a turn: %v", err, e.OperationActive())
	}
	if got := queuedMessages(e.State()); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("paused queue = %q", got)
	}
	resumed := collect(follow(e, func() (RunID, error) { return e.SendQueued(t.Context(), TurnInput{}) }))
	if last := resumed[len(resumed)-1]; last.Kind != EventDone {
		t.Fatalf("resumed run ended with %+v", last)
	}
	if got := backend.userMessages("c"); !reflect.DeepEqual(got, []string{"one", "two\n\nthree"}) {
		t.Fatalf("sent = %q", got)
	}
	if _, err := e.SendQueued(t.Context(), TurnInput{}); !errors.Is(err, ErrNothingQueued) {
		t.Fatalf("SendQueued on an empty queue = %v", err)
	}
}

func TestUnqueueRemovesAnUnsentPrompt(t *testing.T) {
	e := New(&scriptBackend{}, Config{})
	defer e.Close()
	e.do(func() {
		e.queue = []QueuedPrompt{{ID: 1, Message: "a"}, {ID: 2, Message: "b"}}
	})
	if err := e.Unqueue(1); err != nil {
		t.Fatal(err)
	}
	if err := e.Unqueue(1); !errors.Is(err, ErrStale) {
		t.Fatalf("second Unqueue = %v, want ErrStale", err)
	}
	if got := queuedMessages(e.State()); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("queue = %q", got)
	}
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if got := e.State().Queued; len(got) != 0 {
		t.Fatalf("queue after NewConversation = %+v", got)
	}
}

func TestQueueRejectsQueueingBehindRestore(t *testing.T) {
	backend := &historyBackend{history: func(ctx context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	e := New(backend, Config{ConversationID: "c"})
	defer e.Close()
	if _, err := e.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue(TurnInput{Message: "x"}); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("Queue during restore = %v, want ErrOperationActive", err)
	}
}

func TestDequeueBatchesUpToSkillsAndTheImageLimit(t *testing.T) {
	image := func(id string) assistant.ContextEntity {
		return assistant.ContextEntity{Type: assistant.EntityImageURL, ID: id}
	}
	with := func(message string, entities ...assistant.ContextEntity) QueuedPrompt {
		q := QueuedPrompt{Message: message}
		if len(entities) > 0 {
			q.Context = &assistant.AssistantContext{Entities: entities}
		}
		return q
	}
	skill := QueuedPrompt{Message: "/review x", Skill: &SkillInvocation{Name: "review", Arguments: "x"}}
	service := assistant.ContextEntity{Type: "service", ID: "web"}
	override := []assistant.SkillOverride{{Name: "skills-library/clear-writing", Source: assistant.SkillSourceSkillsLibrary, Enabled: true}}
	library := QueuedPrompt{Message: "/clear-writing x", SkillOverrides: override}
	stale := TurnInput{SkillOverrides: []assistant.SkillOverride{{Name: "stale", Enabled: true}}}

	e := New(&scriptBackend{}, Config{})
	defer e.Close()
	var batches []TurnInput
	e.do(func() {
		e.queue = []QueuedPrompt{
			with("a", service, image("1")),
			with(" ", service, image("2"), image("3")), // attachment-only, duplicate service
			with("b", image("1"), image("4"), image("5")),
			skill,
			with("c"),
			library,
			with("d"),
		}
		for in, ok := e.dequeue(stale); ok; in, ok = e.dequeue(stale) {
			batches = append(batches, in)
		}
	})
	want := []TurnInput{
		{Message: "a", Context: &assistant.AssistantContext{Entities: []assistant.ContextEntity{service, image("1"), image("2"), image("3")}}},
		{Message: "b", Context: &assistant.AssistantContext{Entities: []assistant.ContextEntity{image("1"), image("4"), image("5")}}},
		{Message: skill.Message, Skill: skill.Skill},
		{Message: "c"},
		{Message: library.Message, SkillOverrides: override},
		{Message: "d"},
	}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches = %+v\nwant %+v", batches, want)
	}
}

// A stopped turn keeps its queue; rounds keep increasing across the requests
// of a run, after a client tool round too.
func TestQueueAfterToolRoundsAndStop(t *testing.T) {
	tools, err := NewToolSet(Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stop := range []bool{false, true} {
		backend := &scriptBackend{convID: "c", rounds: toolRounds("read"), started: make(chan struct{}), hold: make(chan struct{})}
		e := New(backend, Config{})
		events, unsubscribe := e.Subscribe()
		run, err := e.StartTurn(t.Context(), TurnInput{Message: "one", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		<-backend.started
		if err := e.Queue(TurnInput{Message: "two"}); err != nil {
			t.Fatal(err)
		}
		if stop {
			if err := e.StopTools(); err != nil {
				t.Fatal(err)
			}
		}
		close(backend.hold)
		var rounds []int
		var end EventKind
		for ev := range Follow(events, run) {
			if ev.Round != 0 && (len(rounds) == 0 || rounds[len(rounds)-1] != ev.Round) {
				rounds = append(rounds, ev.Round)
			}
			end = ev.Kind
		}
		unsubscribe()
		wantSent, wantRounds, wantQueued := []string{"one", "two"}, []int{1, 2, 3}, []string(nil)
		if stop {
			wantSent, wantRounds, wantQueued = []string{"one"}, []int{1, 2}, []string{"two"}
		}
		if end != EventDone || !reflect.DeepEqual(rounds, wantRounds) {
			t.Fatalf("stop=%v: end %v, rounds %v; want EventDone, %v", stop, end, rounds, wantRounds)
		}
		if got := backend.userMessages("c"); !reflect.DeepEqual(got, wantSent) {
			t.Fatalf("stop=%v: sent %q, want %q", stop, got, wantSent)
		}
		if got := queuedMessages(e.State()); !reflect.DeepEqual(got, wantQueued) {
			t.Fatalf("stop=%v: queued %q, want %q", stop, got, wantQueued)
		}
		e.Close()
	}
}

// Overrides queued with a prompt reach its request and only its request,
// whether the running turn drains the queue or SendQueued sends it.
func TestQueuedSkillOverridesReachTheirRequest(t *testing.T) {
	override := assistant.SkillOverride{Name: "skills-library/clear-writing", Source: assistant.SkillSourceSkillsLibrary, Enabled: true}
	for _, drain := range []bool{true, false} {
		backend := &scriptBackend{convID: "c", started: make(chan struct{}), hold: make(chan struct{})}
		e := New(backend, Config{})
		events, unsubscribe := e.Subscribe()
		ctx, cancel := context.WithCancel(t.Context())
		run, err := e.StartTurn(ctx, TurnInput{Message: "one"})
		if err != nil {
			t.Fatal(err)
		}
		<-backend.started
		for _, in := range []TurnInput{
			{Message: "/clear-writing x", SkillOverrides: []assistant.SkillOverride{override}},
			{Message: "plain"},
		} {
			if err := e.Queue(in); err != nil {
				t.Fatal(err)
			}
		}
		if drain {
			close(backend.hold)
		} else {
			cancel() // the queue waits for SendQueued
		}
		for range Follow(events, run) {
		}
		if !drain {
			close(backend.hold)
			if run, err = e.SendQueued(t.Context(), TurnInput{}); err != nil {
				t.Fatal(err)
			}
			for range Follow(events, run) {
			}
		}
		unsubscribe()
		e.Close()
		cancel()
		sent := backend.opts[len(backend.opts)-2:]
		if !slices.Contains(sent[0].SkillOverrides, override) || slices.Contains(sent[1].SkillOverrides, override) {
			t.Fatalf("drain=%v: overrides = %+v then %+v", drain, sent[0].SkillOverrides, sent[1].SkillOverrides)
		}
	}
}
