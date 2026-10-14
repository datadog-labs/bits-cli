package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// attachServices picks each label from the @ menu, as a user would.
func attachServices(e *Editor, labels ...string) {
	for _, label := range labels {
		e.Update(tea.PasteMsg{Content: "@x"})
		e.SetEntityResults("x", RemoteReady, []Candidate{{
			Kind:   CandidateEntity,
			ID:     label,
			Label:  label,
			Insert: "@" + label,
			Attachment: &Attachment{
				Type:  "service",
				ID:    label,
				Label: label,
			},
		}})
		e.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}

func attachmentIDs(e *Editor) []string {
	var ids []string
	for _, a := range e.Attachments() {
		ids = append(ids, a.ID)
	}
	return ids
}

// cellOf returns where text is drawn in the editor view.
func cellOf(t *testing.T, e *Editor, text string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(e.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q is not drawn in %q", text, ansi.Strip(e.View()))
	return 0, 0
}

func TestDeletingMentionTextKeepsItsChip(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(80)
	attachServices(e, "checkout-api")
	if got := e.Value(); got != "@checkout-api " {
		t.Fatalf("prompt = %q", got)
	}
	e.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if e.Value() != "" || strings.Join(attachmentIDs(e), ",") != "checkout-api" {
		t.Fatalf("value = %q, attachments = %v", e.Value(), attachmentIDs(e))
	}
	if !strings.Contains(ansi.Strip(e.View()), "Service: checkout-api ×") {
		t.Fatalf("chip disappeared with its text: %q", ansi.Strip(e.View()))
	}
}

func TestAttachingTheSameEntityTwiceShowsOneChip(t *testing.T) {
	e := New()
	e.Focus()
	attachServices(e, "checkout-api", "checkout-api")
	if got := attachmentIDs(e); len(got) != 1 {
		t.Fatalf("attachments = %v", got)
	}
}

func TestClickingAChipRemovesIt(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(80)
	attachServices(e, "checkout-api", "payments")
	before := e.Value()

	x, y := cellOf(t, e, "Service: payments")
	if !e.Click(x, y) {
		t.Fatal("click on the chip was not handled")
	}
	if got := strings.Join(attachmentIDs(e), ","); got != "checkout-api" {
		t.Fatalf("attachments = %v", got)
	}
	if e.Value() != before {
		t.Fatalf("removing a chip changed the prompt: %q", e.Value())
	}
	if x, y := cellOf(t, e, "@checkout-api"); e.Click(x, y) {
		t.Fatal("a click on the prompt text was taken by the chips")
	}
}

func TestBackspaceAtPromptStartWalksAndRemovesChips(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(80)
	attachServices(e, "a", "b", "c")
	e.SetValue("")

	e.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !e.ChipsFocused() || len(e.Attachments()) != 3 {
		t.Fatalf("first backspace: focused = %v, attachments = %v", e.ChipsFocused(), attachmentIDs(e))
	}
	e.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	e.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := strings.Join(attachmentIDs(e), ","); got != "a,c" {
		t.Fatalf("attachments = %v, want the focused chip removed", got)
	}
	e.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if e.ChipsFocused() {
		t.Fatal("esc did not return to the text")
	}
	e.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if e.Value() != "h" || len(e.Attachments()) != 2 {
		t.Fatalf("typing after chips: value = %q, attachments = %v", e.Value(), attachmentIDs(e))
	}

	e.SetValue("")
	e.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	e.Update(tea.PasteMsg{Content: "x"})
	e.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if e.Value() != "" || len(e.Attachments()) != 2 {
		t.Fatalf("backspace after a paste: value = %q, attachments = %v", e.Value(), attachmentIDs(e))
	}
}

func TestManyChipsCollapseIntoMoreThatTogglesOnClick(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(40)
	attachServices(e, "checkout-api", "payments-api", "inventory", "shipping")
	collapsed := e.Height()

	// The editor grows upward, so a cell recorded before the click no longer
	// matches what is under a still mouse.
	e.SetPointer(cellOf(t, e, "checkout-api"))
	x, y := cellOf(t, e, "+3 more")
	if !e.Click(x, y) {
		t.Fatal("click on +3 more was not handled")
	}
	if e.ChipHovered() {
		t.Fatal("the editor grew under a still pointer but kept its hover")
	}
	view := ansi.Strip(e.View())
	for _, label := range []string{"checkout-api", "payments-api", "inventory", "shipping"} {
		if !strings.Contains(view, "Service: "+label+" ×") {
			t.Fatalf("expanded row misses %s: %q", label, view)
		}
	}
	if strings.Contains(view, "more") || e.Height() <= collapsed {
		t.Fatalf("row did not expand: height %d -> %d, %q", collapsed, e.Height(), view)
	}
	for _, line := range strings.Split(e.View(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide: %q", w, ansi.Strip(line))
		}
	}

	x, y = cellOf(t, e, "less")
	if !e.Click(x, y) || e.Height() != collapsed || !strings.Contains(ansi.Strip(e.View()), "+3 more") {
		t.Fatalf("less did not collapse the row: height %d, want %d", e.Height(), collapsed)
	}
}

func TestChipLabelsAreEscaped(t *testing.T) {
	e := New()
	e.Focus()
	e.SetWidth(80)
	e.AddAttachment(Attachment{
		Type:  "service\x1b[31m",
		ID:    "s1",
		Label: "evil\x1b]52;c;Y2xpcA==\a\nforged",
	})
	view := e.View()
	if strings.Contains(view, "\x1b]52") || strings.Contains(view, "\a") {
		t.Fatalf("chip rendered a terminal control: %q", view)
	}
	if !strings.Contains(ansi.Strip(view), "Service: evil forged ×") {
		t.Fatalf("chip = %q", ansi.Strip(view))
	}
}

func TestRecallingAPromptRestoresItsChips(t *testing.T) {
	e := New()
	e.Focus()
	service := func(id string) Attachment { return Attachment{Type: "service", ID: id, Label: id} }
	e.SetHistorySource(func() []HistoryEntry {
		return []HistoryEntry{
			{Text: "first", Attachments: []Attachment{service("a")}},
			{Text: "second", Attachments: []Attachment{service("b"), service("c")}},
		}
	})
	for _, step := range []struct {
		key, text, ids string
	}{
		{key: "up", text: "second", ids: "b,c"},
		{key: "up", text: "first", ids: "a"},
		{key: "down", text: "second", ids: "b,c"},
		{key: "down", text: "", ids: ""},
	} {
		e.Update(historyKeyMsg(step.key))
		if e.Value() != step.text || strings.Join(attachmentIDs(e), ",") != step.ids {
			t.Fatalf("after %s: value %q, chips %v; want %q, %q", step.key, e.Value(), attachmentIDs(e), step.text, step.ids)
		}
	}
}

func TestHistoryDoesNotReplaceChipsBeingPrepared(t *testing.T) {
	e := New()
	e.Focus()
	e.SetHistorySource(func() []HistoryEntry { return textEntries("previous") })
	attachServices(e, "draft")
	e.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	e.Update(historyKeyMsg("up"))
	if e.Value() != "" || strings.Join(attachmentIDs(e), ",") != "draft" {
		t.Fatalf("history replaced the draft: value %q, chips %v", e.Value(), attachmentIDs(e))
	}
}

func TestRemovingARecalledChipLeavesHistory(t *testing.T) {
	e := New()
	e.Focus()
	chip := Attachment{Type: "service", ID: "b", Label: "b"}
	e.SetHistorySource(func() []HistoryEntry {
		return []HistoryEntry{{Text: "first"}, {Text: "second", Attachments: []Attachment{chip}}}
	})
	e.Update(historyKeyMsg("up"))
	e.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	e.Update(historyKeyMsg("up"))
	if e.Value() != "second" {
		t.Fatalf("up after removing a recalled chip switched entries: %q", e.Value())
	}
}
