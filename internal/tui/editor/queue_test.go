package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestQueueSnapshotsSurviveDraftReset(t *testing.T) {
	e := New()
	a := Attachment{ID: "one", Type: "service"}
	e.SetValue("first")
	e.AddAttachment(a)
	prompt := e.Snapshot()
	e.RemoveLastAttachment()
	if len(prompt.Attachments) != 1 || prompt.Attachments[0].ID != "one" {
		t.Fatal("removing a draft attachment changed the snapshot")
	}
	e.Enqueue(prompt)
	prompt.Attachments[0].ID = "changed"
	e.Reset()
	e.SetValue("second")
	e.Enqueue(e.Snapshot())
	first := e.QueuedPrompts()[0]
	if first.Text != "first" || first.Attachments[0].ID != "one" {
		t.Fatalf("queue did not preserve snapshot: %+v", first)
	}
	first.Attachments[0].ID = "changed"
	if e.QueuedPrompts()[0].Attachments[0].ID != "one" {
		t.Fatal("modifying a returned attachment changed the queue")
	}
	e.RemoveQueued(0)
	second := e.QueuedPrompts()[0]
	if second.Text != "second" || e.Value() != "second" {
		t.Fatalf("queue advanced into draft: %+v", second)
	}
	e.PauseQueue()
	e.Reset()
	if !e.QueuePaused() || e.QueueLen() != 1 {
		t.Fatal("draft reset changed queue")
	}
	e.ClearQueue()
	if e.QueueLen() != 0 || e.QueuePaused() {
		t.Fatal("queue did not clear")
	}
}

func TestQueueNavigationRemovalAndBoundedPreview(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(44)
	e.SetValue("draft")
	e.AddAttachment(Attachment{ID: "draft attachment", Type: "service"})
	for i := range 8 {
		e.Enqueue(Prompt{Text: fmt.Sprintf("prompt %d\n%s", i, strings.Repeat("界", 50)), Attachments: []Attachment{{ID: "one"}, {ID: "two"}}})
	}
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
	p := e.QueuedPrompts()[0]
	if !strings.HasPrefix(p.Text, "prompt 1") {
		t.Fatalf("removed wrong prompt: %q", p.Text)
	}
	e.SetWidth(12)
	if got := e.QueueView(1); strings.Contains(got, "\n") || ansi.StringWidth(got) > 12 {
		t.Fatalf("narrow queue overflow: %q", got)
	}
}

func TestQueueStackKeepsOneTextRowWhileAnimating(t *testing.T) {
	e := New()
	e.SetWidth(60)
	e.Enqueue(Prompt{Text: strings.Repeat("long prompt\n", 30), Attachments: []Attachment{{Type: "service", ID: "api", Label: "API"}}})
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
	e.PauseQueue()
	paused := e.QueueView(11)
	e.SetActivityFrame(4)
	if e.QueueView(11) != paused {
		t.Fatal("paused queue animated")
	}
	e.ResumeQueue()
	input := e.inputStyle
	input.SweepMotion = false
	e.SetInputStyles(input)
	still := e.QueueView(11)
	e.SetActivityFrame(6)
	if e.QueueView(11) != still {
		t.Fatal("reduced-motion queue animated")
	}
	e.Enqueue(Prompt{Text: "second"})
	e.Enqueue(Prompt{Text: "third"})
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
	if !e.ClickQueue(58, 5) || e.QueueLen() != 2 || e.QueuedPrompts()[1].Text != "third" {
		t.Fatal("shared frame changed the second prompt's close target")
	}
}
