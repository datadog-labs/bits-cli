package graphics

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQueueOrdersWritesBeforeQuit(t *testing.T) {
	var queue Queue
	cmd := queue.Enqueue(tea.Raw("transmit"))
	if queue.Enqueue(tea.Raw("place")) != nil || queue.Enqueue(tea.Raw("delete")) != nil {
		t.Fatal("later writes started while transmission was pending")
	}
	if queue.DrainThen(tea.Quit) != nil {
		t.Fatal("quit started before deletion")
	}

	for _, want := range []string{"transmit", "place", "delete"} {
		msg, ok := cmd().(tea.RawMsg)
		if !ok || fmt.Sprint(msg.Msg) != want {
			t.Fatalf("write = %v, want %q", msg, want)
		}
		cmd, ok = queue.Handle(msg) // Bubble Tea calls Update after writing RawMsg.
		if !ok || cmd == nil {
			t.Fatal("queue did not advance after a write")
		}
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not follow deletion")
	}
}
