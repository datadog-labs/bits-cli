package agent

import (
	"fmt"
	"reflect"
	"testing"
)

func TestOutboxCoalescesOverlappingStatesInOrder(t *testing.T) {
	o := &outbox{}
	o.ready.L = &o.mu
	for i := 1; i <= 10000; i++ {
		o.push(Event{Kind: EventState, Run: RunID(i%2 + 1), Round: 1, State: State{Version: uint64(i)}})
	}
	if len(o.queue) != 2 {
		t.Fatalf("queued %d snapshots, want two", len(o.queue))
	}
	if o.queue[0].State.Version != 9999 || o.queue[1].State.Version != 10000 {
		t.Fatalf("snapshots out of order: %+v", o.queue)
	}
}

func TestOutboxPreservesLifecycleAndRoundBoundaries(t *testing.T) {
	state := func(run RunID, round int, version uint64) Event {
		return Event{Kind: EventState, Run: run, Round: round, State: State{Version: version}}
	}
	for _, kind := range []EventKind{EventBackground, EventDone, EventCanceled, EventError, EventUsage, EventConversation} {
		t.Run(fmt.Sprintf("event_%d", kind), func(t *testing.T) {
			o := &outbox{}
			o.ready.L = &o.mu
			boundary := Event{Kind: kind, Run: 1, Round: 1}
			input := []Event{state(1, 1, 1), state(2, 1, 2), boundary, state(1, 1, 3), state(2, 1, 4), state(1, 1, 5), state(1, 2, 6)}
			for _, ev := range input {
				o.push(ev)
			}
			want := []Event{input[0], input[1], boundary, input[4], input[5], input[6]}
			if !reflect.DeepEqual(o.queue, want) {
				t.Fatalf("queue = %+v, want %+v", o.queue, want)
			}
		})
	}
}
