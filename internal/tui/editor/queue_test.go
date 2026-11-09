package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// queue mirrors prompts as the parent's queue, with IDs from 1.
func queue(e *Editor, paused bool, prompts ...HistoryEntry) {
	items := make([]QueuedPrompt, len(prompts))
	for i, p := range prompts {
		items[i] = QueuedPrompt{ID: uint64(i + 1), HistoryEntry: p}
	}
	e.SetQueue(items, paused)
}

func queuedTexts(e *Editor) []string {
	var texts []string
	for _, q := range e.queue.items {
		texts = append(texts, q.Text)
	}
	return texts
}

func TestRemovingAQueuedPromptReportsItAndKeepsTheDraft(t *testing.T) {
	e := New()
	var removed []uint64
	e.SetQueueRemover(func(id uint64) { removed = append(removed, id) })
	e.SetValue("draft")
	queue(e, true, HistoryEntry{Text: "first"}, HistoryEntry{Text: "second"})
	e.RemoveQueued(0)
	if got := queuedTexts(e); len(got) != 1 || got[0] != "second" || len(removed) != 1 || removed[0] != 1 || e.Value() != "draft" {
		t.Fatalf("queue %q, removed %v, draft %q", got, removed, e.Value())
	}
	e.RemoveQueued(0)
	if e.QueueLen() != 0 || e.QueuePaused() {
		t.Fatal("an empty queue stays paused")
	}
}

func TestQueueNavigationRemovalAndBoundedPreview(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(44)
	e.SetValue("draft")
	e.AddAttachment(Attachment{ID: "draft attachment", Type: "service"})
	var prompts []HistoryEntry
	for i := range 8 {
		prompts = append(prompts, HistoryEntry{Text: fmt.Sprintf("prompt %d\n%s", i, strings.Repeat("界", 50)), Attachments: []Attachment{{ID: "one"}, {ID: "two"}}})
	}
	queue(e, false, prompts...)
	view := ansi.Strip(e.QueueView(11))
	if strings.Contains(view, "Queued") || !strings.Contains(view, "one") || !strings.Contains(view, "two") || !strings.Contains(view, "…") {
		t.Fatalf("preview = %q", view)
	}
	for _, row := range strings.Split(view, "\n") {
		if ansi.StringWidth(row) > 44 {
			t.Fatalf("row overflow: %q", row)
		}
	}
	if len(strings.Split(view, "\n")) > 11 {
		t.Fatalf("unbounded queue: %q", view)
	}
	e.ControlKey("tab") // attachments
	if !e.ChipsFocused() {
		t.Fatal("Tab did not reach attachments")
	}
	e.ControlKey("tab") // queue
	for range 7 {
		e.ControlKey("down")
	}
	view = ansi.Strip(e.QueueView(11))
	if !strings.Contains(view, "prompt 7") {
		t.Fatalf("selection not visible: %q", view)
	}
	e.ControlKey("delete")
	e.ControlKey("esc")
	if e.QueueLen() != 7 || e.Value() != "draft" || len(e.Attachments()) != 1 || e.focus != focusText {
		t.Fatal("queue navigation modified draft")
	}
	e.ScrollQueue(-100)
	e.QueueView(11)
	if e.ClickQueue(5, 3) {
		t.Fatal("preview body removed prompt")
	}
	if !e.ClickQueue(42, 3) {
		t.Fatal("close control missed")
	}
	if first := queuedTexts(e)[0]; !strings.HasPrefix(first, "prompt 1") {
		t.Fatalf("removed wrong prompt: %q", first)
	}
	e.SetWidth(12)
	if got := e.QueueView(1); strings.Contains(got, "\n") || ansi.StringWidth(got) > 12 {
		t.Fatalf("narrow queue overflow: %q", got)
	}
}

func TestQueueStackKeepsOneTextRowWhileAnimating(t *testing.T) {
	e := New()
	e.SetWidth(60)
	long := HistoryEntry{Text: strings.Repeat("long prompt\n", 30), Attachments: []Attachment{{Type: "service", ID: "api", Label: "API"}}}
	queue(e, false, long)
	e.SetWorking(true)
	e.SetActivityFrame(0)
	first := ansi.Strip(e.QueueView(11))
	if rows := strings.Split(first, "\n"); len(rows) != 5 || !strings.Contains(rows[1], "API") || !strings.Contains(rows[3], "…") {
		t.Fatalf("expected frame, chip row, gap, one text row and frame: %q", first)
	}
	e.SetActivityFrame(2)
	second := ansi.Strip(e.QueueView(11))
	if first == second || strings.Count(first, "\n") != strings.Count(second, "\n") {
		t.Fatal("spinner did not animate with stable geometry")
	}
	queue(e, true, long)
	paused := e.QueueView(11)
	e.SetActivityFrame(4)
	if e.QueueView(11) != paused {
		t.Fatal("paused queue animated")
	}
	queue(e, false, long)
	input := e.inputStyle
	input.SweepMotion = false
	e.SetInputStyles(input)
	still := e.QueueView(11)
	e.SetActivityFrame(6)
	if e.QueueView(11) != still {
		t.Fatal("reduced-motion queue animated")
	}
	queue(e, false, long, HistoryEntry{Text: "second"}, HistoryEntry{Text: "third"})
	stack := ansi.Strip(e.QueueView(11))
	lines := strings.Split(stack, "\n")
	for _, line := range lines[1 : len(lines)-1] {
		if strings.ContainsAny(line, "▔▁") {
			t.Fatalf("interior prompt border: %q", line)
		}
	}
	if strings.Count(stack, "×") != 3 {
		t.Fatalf("stack did not show three prompts: %q", stack)
	}
	if !e.ClickQueue(58, 5) || e.QueueLen() != 2 || queuedTexts(e)[1] != "third" {
		t.Fatal("shared frame changed the second prompt's close target")
	}
}

func TestQueueSelectionFollowsItsPrompt(t *testing.T) {
	e := New()
	queue(e, false, HistoryEntry{Text: "a"}, HistoryEntry{Text: "b"}, HistoryEntry{Text: "c"})
	e.queueUI.cursor = 1
	e.SetQueue(e.queue.items[1:], false) // "a" was sent
	if e.queueUI.cursor != 0 {
		t.Fatalf("cursor = %d, want it on %q", e.queueUI.cursor, "b")
	}
}
