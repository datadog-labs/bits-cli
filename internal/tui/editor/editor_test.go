package editor

import (
	"strings"
	"testing"
)

func TestWordBounds(t *testing.T) {
	runes := []rune("look at @engine")
	start, end := wordBounds(runes, len(runes)) // cursor at end
	if got := string(runes[start:end]); got != "@engine" {
		t.Errorf("wordBounds at end = %q, want @engine", got)
	}

	// Cursor in the middle of a word still spans the whole token.
	start, end = wordBounds(runes, 11) // inside "@engine"
	if got := string(runes[start:end]); got != "@engine" {
		t.Errorf("wordBounds mid-word = %q, want @engine", got)
	}

	// Cursor on whitespace yields an empty range.
	if s, e := wordBounds([]rune("a b"), 2); s != e {
		t.Errorf("wordBounds on space = [%d,%d), want empty", s, e)
	}
}

func TestDispatchTriggers(t *testing.T) {
	if got := Dispatch("hello"); got != nil {
		t.Errorf("plain word should not trigger, got %v", got)
	}
	if got := Dispatch("@"); len(got) == 0 {
		t.Error("@ should return file candidates")
	}
	if got := Dispatch("/"); len(got) == 0 {
		t.Error("/ should return command candidates")
	}
}

func TestFakeFilesFilter(t *testing.T) {
	got := FakeFiles("engine")
	if len(got) == 0 {
		t.Fatal("expected a match for 'engine'")
	}
	for _, c := range got {
		if !strings.Contains(strings.ToLower(c.Label), "engine") {
			t.Errorf("candidate %q does not contain query", c.Label)
		}
		if !strings.HasPrefix(c.Insert, "@") {
			t.Errorf("file insert %q should start with @", c.Insert)
		}
	}
}

func TestFakeCommandsPrefix(t *testing.T) {
	got := FakeCommands("m")
	if len(got) == 0 {
		t.Fatal("expected /model for prefix 'm'")
	}
	for _, c := range got {
		if !strings.HasPrefix(c.Insert, "/m") {
			t.Errorf("command insert %q should start with /m", c.Insert)
		}
	}
}

func TestFakeCommandsAliasDiscoverable(t *testing.T) {
	// The canonical name matches its own prefix.
	quit := FakeCommands("q")
	if len(quit) == 0 {
		t.Fatal("expected /quit for prefix 'q'")
	}
	for _, c := range quit {
		if c.Insert != "/quit" {
			t.Errorf("canonical insert = %q, want /quit", c.Insert)
		}
	}

	// The alias is discoverable by its own prefix and normalizes to canonical on accept.
	exit := FakeCommands("ex")
	if len(exit) != 1 {
		t.Fatalf("expected one candidate for alias prefix 'ex', got %d", len(exit))
	}
	if exit[0].Insert != "/quit" {
		t.Errorf("alias accept should insert canonical /quit, got %q", exit[0].Insert)
	}
}

func TestRecomputeOpensAndClosesMenu(t *testing.T) {
	e := New()

	e.ta.SetValue("@eng")
	e.recompute()
	if !e.MenuOpen() {
		t.Fatal("menu should open on an @ trigger")
	}

	e.ta.SetValue("plain text")
	e.recompute()
	if e.MenuOpen() {
		t.Fatal("menu should close without a trigger")
	}
}

func TestAcceptReplacesActiveWord(t *testing.T) {
	e := New()
	e.ta.SetValue("look at @engine")
	e.recompute()
	if !e.MenuOpen() {
		t.Fatal("menu should be open")
	}
	e.accept()

	got := e.Value()
	if !strings.HasPrefix(got, "look at @") {
		t.Errorf("head not preserved: %q", got)
	}
	if !strings.Contains(got, "engine.go") {
		t.Errorf("candidate not inserted: %q", got)
	}
	if !strings.HasSuffix(got, " ") {
		t.Errorf("accepted completion should end with a space: %q", got)
	}
	if e.MenuOpen() {
		t.Error("menu should close after accept")
	}
}

func TestMoveWraps(t *testing.T) {
	e := New()
	e.ta.SetValue("/")
	e.recompute()
	n := len(e.menu.items)
	if n < 2 {
		t.Fatalf("need >=2 candidates to test wrap, got %d", n)
	}

	e.move(-1) // wrap from 0 to last
	if e.menu.selected != n-1 {
		t.Errorf("move(-1) from 0 = %d, want %d", e.menu.selected, n-1)
	}
	e.move(1) // wrap back to 0
	if e.menu.selected != 0 {
		t.Errorf("move(1) from last = %d, want 0", e.menu.selected)
	}
}
