package editor

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestViewCacheIsSharedByHeightAndView(t *testing.T) {
	e := New()
	e.SetFiles([]string{"README.md"})
	_ = e.Height()
	if !e.viewCached {
		t.Fatal("Height did not populate the editor view cache")
	}
	first := e.view
	if got := e.View(); got != first || !e.viewCached {
		t.Fatalf("View = %q, want cached %q", got, first)
	}

	e.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if e.viewCached {
		t.Fatal("editor update did not invalidate the view cache")
	}

	e.ta.SetValue("@")
	e.recompute()
	_ = e.View()
	e.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !e.viewCached {
		t.Fatal("menu-only navigation discarded the unchanged textarea view")
	}
	e.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if e.viewCached {
		t.Fatal("accepted completion left the changed textarea view cached")
	}

	e.SetPlaceholder("Ask Bits…")
	_ = e.View()
	e.SetPlaceholder("Ask Bits…")
	if !e.viewCached {
		t.Fatal("unchanged placeholder invalidated the editor view")
	}
	e.SetPlaceholder("Working on it…")
	if e.viewCached {
		t.Fatal("changed placeholder left the editor view cached")
	}

	_ = e.View()
	e.SetInputStyles(styles.Default(false).Input)
	if e.viewCached {
		t.Fatal("changed input styles left the editor view cached")
	}
}

func TestSetWidthTracksRequestedOuterWidth(t *testing.T) {
	e := New()
	e.SetWidth(80)
	innerWidth := e.ta.Width()
	_ = e.View()

	// textarea.Width reports the content width after subtracting the prompt.
	// Passing that value back as a new outer width must still resize the editor.
	e.SetWidth(innerWidth)
	if got := e.ta.Width(); got >= innerWidth {
		t.Fatalf("inner width after shrinking outer width = %d, want less than %d", got, innerWidth)
	}
	if e.viewCached {
		t.Fatal("outer-width change left the editor view cached")
	}

	_ = e.View()
	e.SetWidth(innerWidth)
	if !e.viewCached {
		t.Fatal("unchanged outer width invalidated the editor view")
	}
}

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

func TestFileCandidatesFilter(t *testing.T) {
	got := FileCandidates([]string{"README.md", "internal/agent/engine.go"}, "engine")
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

	clear := FakeCommands("cl")
	if len(clear) != 1 || clear[0].Insert != "/new" {
		t.Fatalf("/clear alias candidate = %+v, want one canonical /new insertion", clear)
	}
}

func TestRecomputeOpensAndClosesMenu(t *testing.T) {
	e := New()
	e.SetFiles([]string{"internal/agent/engine.go"})

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

func TestSlashCompletionOnlyOpensForFirstPromptToken(t *testing.T) {
	e := New()

	e.ta.SetValue("/new")
	e.ta.SetCursorColumn(len([]rune("/new")))
	e.recompute()
	if !e.MenuOpen() {
		t.Fatal("menu should open for a leading slash command")
	}
	if name, ok := e.SelectedCommand(); !ok || name != "new" {
		t.Fatalf("SelectedCommand() = (%q, %v), want (new, true)", name, ok)
	}

	e.ta.SetValue("explain /new")
	e.ta.SetCursorColumn(len([]rune("explain /new")))
	e.recompute()
	if e.MenuOpen() {
		t.Fatal("menu should stay closed for a slash mid-prompt")
	}
	if name, ok := e.SelectedCommand(); ok || name != "" {
		t.Fatalf("SelectedCommand() = (%q, %v), want (empty, false)", name, ok)
	}
}

func TestAcceptReplacesActiveWord(t *testing.T) {
	e := New()
	e.SetFiles([]string{"internal/agent/engine.go"})
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

	e.menu.selector.UpdateKey("up")
	if e.menu.selector.Index() != n-1 {
		t.Errorf("up from 0 = %d, want %d", e.menu.selector.Index(), n-1)
	}
	e.menu.selector.UpdateKey("down")
	if e.menu.selector.Index() != 0 {
		t.Errorf("down from last = %d, want 0", e.menu.selector.Index())
	}
}

func TestAutocompleteDetailsFollowLabelsWithoutColumnGap(t *testing.T) {
	e := New()
	e.SetWidth(80)
	e.ta.SetValue("/")
	e.ta.SetCursorColumn(1)
	e.recompute()
	slash := ansi.Strip(e.MenuView())
	if !strings.Contains(slash, "/help  show help") {
		t.Fatalf("slash menu has an unexpected label/detail gap: %q", slash)
	}
	for lineNo, line := range strings.Split(e.MenuView(), "\n") {
		if got, want := ansi.StringWidth(line), e.menuWidth(); got != want {
			t.Fatalf("slash row %d width = %d, want opaque width %d", lineNo+1, got, want)
		}
	}

	e.SetFiles([]string{"README.md"})
	e.ta.SetValue("@")
	e.ta.SetCursorColumn(1)
	e.SetEntityResults("", RemoteReady, []Candidate{{
		Kind: CandidateEntity, ID: "c1", Label: "DD checkout-api",
		Detail: "service · APM", Insert: "@checkout-api",
	}})
	mentions := ansi.Strip(e.MenuView())
	if !strings.Contains(mentions, "file README.md  local") {
		t.Fatalf("file menu row has an unexpected label/detail gap: %q", mentions)
	}
	if !strings.Contains(mentions, "DD checkout-api  service · APM") {
		t.Fatalf("Datadog menu row has an unexpected label/detail gap: %q", mentions)
	}
	for lineNo, line := range strings.Split(e.MenuView(), "\n") {
		if got, want := ansi.StringWidth(line), e.menuWidth(); got != want {
			t.Fatalf("mention row %d width = %d, want opaque width %d", lineNo+1, got, want)
		}
	}
}

func TestEntityTriggerSupportsSpacesAndRejectsMiddleOfWord(t *testing.T) {
	e := New()
	e.ta.SetValue("investigate @checkout api")
	e.ta.SetCursorColumn(len([]rune("investigate @checkout api")))
	if query, ok := e.ActiveEntityQuery(); !ok || query != "checkout api" {
		t.Fatalf("ActiveEntityQuery = (%q, %v), want checkout api", query, ok)
	}

	e.ta.SetValue("email@example.com")
	e.ta.SetCursorColumn(len([]rune("email@example.com")))
	if query, ok := e.ActiveEntityQuery(); ok {
		t.Fatalf("middle-of-word trigger returned %q", query)
	}
}

func TestMixedCandidatesAttachCanonicalEntityAndCanRemoveIt(t *testing.T) {
	e := New()
	e.SetFiles([]string{"checkout.md"})
	e.ta.SetValue("@checkout")
	e.ta.SetCursorColumn(len([]rune("@checkout")))
	attachment := Attachment{Type: "service", ID: "checkout-api", Label: "checkout-api", CandidateID: "candidate-1", SearchFlowID: "flow-1"}
	e.SetEntityResults("checkout", RemoteReady, []Candidate{{
		Kind: CandidateEntity, ID: "candidate-1", Label: "DD checkout-api",
		Detail: "service · APM", Insert: "@checkout-api", Attachment: &attachment,
	}})
	if len(e.menu.items) != 2 || e.menu.items[0].Kind != CandidateFile || e.menu.items[1].Kind != CandidateEntity {
		t.Fatalf("mixed menu = %#v", e.menu.items)
	}
	e.menu.selector.SetIndex(1)
	e.accept()
	if got := e.Attachments(); len(got) != 1 || !reflect.DeepEqual(got[0], attachment) {
		t.Fatalf("attachments = %#v", got)
	}
	if !strings.Contains(e.Value(), "@checkout-api") {
		t.Fatalf("visible prompt = %q", e.Value())
	}
	e.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if len(e.Attachments()) != 0 {
		t.Fatalf("attachment was not removed: %#v", e.Attachments())
	}
}

func TestMenuAndAttachmentsAreUnicodeSafeAtNarrowWidths(t *testing.T) {
	e := New()
	e.SetWidth(18)
	e.ta.SetValue("@déplo")
	e.ta.SetCursorColumn(len([]rune("@déplo")))
	attachment := Attachment{Type: "workflow", ID: "w1", Label: "Déploiement 東京"}
	e.SetEntityResults("déplo", RemoteReady, []Candidate{{
		Kind: CandidateEntity, ID: "c1", Label: "DD Déploiement 東京 très long",
		Detail: "workflow", Insert: "@Déploiement 東京", Attachment: &attachment,
	}})
	for _, line := range strings.Split(e.MenuView(), "\n") {
		if width := ansi.StringWidth(line); width > 18 {
			t.Fatalf("menu line is %d cells: %q", width, ansi.Strip(line))
		}
	}
	e.accept()
	for _, line := range strings.Split(e.View(), "\n") {
		if width := ansi.StringWidth(line); width > 18 {
			t.Fatalf("editor line is %d cells: %q", width, ansi.Strip(line))
		}
	}
}
