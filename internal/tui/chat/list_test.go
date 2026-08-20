package chat

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// lineItem builds a single-line user item with a stable id, so list geometry is
// deterministic: each item is 1 row and the gap adds 1 blank row between items.
func lineItem(id, text string) Item {
	return Item{
		ID:   ItemID{Scope: ScopeLocal, Key: id},
		Kind: assistant.KindText,
		Role: assistant.RoleUser,
		Text: text,
	}
}

func newTestList(w, h int, items ...Item) *List {
	l := NewList()
	l.SetStyles(DefaultStyles(true))
	l.SetWidth(w)
	l.SetHeight(h)
	l.SetItems(items)
	return l
}

func lines(s string) []string { return strings.Split(plain(s), "\n") }

func lastNonEmpty(s string) string {
	ls := lines(s)
	for i := len(ls) - 1; i >= 0; i-- {
		if strings.TrimSpace(ls[i]) != "" {
			return ls[i]
		}
	}
	return ""
}

// Render always returns exactly Height() rows, padding short content so the
// caller's footer stays pinned to the bottom (the viewport's fill behavior).
func TestList_RenderPadsToHeight(t *testing.T) {
	l := newTestList(80, 10, lineItem("1", "alpha"), lineItem("2", "beta"))
	got := l.Render()
	if n := len(lines(got)); n != 10 {
		t.Fatalf("want 10 rows, got %d:\n%s", n, plain(got))
	}
	// Content is top-aligned: alpha, gap, beta, then padding.
	ls := lines(got)
	if !strings.Contains(ls[0], "alpha") || !strings.Contains(ls[2], "beta") {
		t.Errorf("content not top-aligned with a gap row:\n%s", plain(got))
	}
}

// A short list (fits in the viewport) reports AtBottom regardless of scroll.
func TestList_AtBottomWhenNotOverflowing(t *testing.T) {
	l := newTestList(80, 20, lineItem("1", "a"), lineItem("2", "b"))
	if !l.AtBottom() {
		t.Error("a list shorter than the viewport must be at bottom")
	}
}

// The render memo is keyed by (id, version, width): a mutation without a version
// bump is served stale from cache; bumping the version invalidates the entry.
func TestList_CacheKeyedByVersion(t *testing.T) {
	items := []Item{lineItem("1", "original")}
	l := newTestList(80, 5, items...)

	if got := plain(l.Render()); !strings.Contains(got, "original") {
		t.Fatalf("first render missing text:\n%s", got)
	}

	// Mutate the text but leave Version untouched: still a cache hit (stale).
	items[0].Text = "changed"
	l.SetItems(items)
	if got := plain(l.Render()); !strings.Contains(got, "original") {
		t.Errorf("expected stale cached render without a version bump, got:\n%s", got)
	}

	// Bump the version: now the entry is invalid and re-renders.
	items[0].Version++
	l.SetItems(items)
	if got := plain(l.Render()); !strings.Contains(got, "changed") {
		t.Errorf("version bump did not invalidate the cache, got:\n%s", got)
	}
}

// A width change invalidates every entry, so restyled/rewrapped output appears.
func TestList_WidthChangeInvalidates(t *testing.T) {
	// A phrase that fits on one line at width 80 but must wrap at width 10.
	items := []Item{lineItem("1", "aaaa bbbb cccc dddd eeee")}
	l := newTestList(80, 8, items...)
	if n := len(strings.Split(strings.TrimRight(plain(l.Render()), "\n \t"), "\n")); n == 0 {
		t.Fatal("unexpected empty render")
	}
	wide := l.Render()
	l.SetWidth(10)
	narrow := l.Render()
	if wide == narrow {
		t.Error("width change should re-wrap and change the rendered output")
	}
}

func makeOverflowingList(t *testing.T, n, h int) (*List, []Item) {
	t.Helper()
	items := make([]Item, n)
	for i := range items {
		items[i] = lineItem(string(rune('a'+i)), "item"+itoa(i))
	}
	return newTestList(80, h, items...), items
}

// itoa avoids strconv noise in test ids.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Auto-follow: an overflowing list is not at bottom from the top, and
// ScrollToBottom pins the last item's content to the last visible row.
func TestList_ScrollToBottomShowsLastItem(t *testing.T) {
	l, _ := makeOverflowingList(t, 20, 5)

	// Scroll to the top (which releases the tail pin) to inspect the head.
	l.ScrollToTop()
	if l.AtBottom() {
		t.Fatal("overflowing list at the top must not report AtBottom")
	}
	if got := plain(l.Render()); !strings.Contains(got, "item0") {
		t.Errorf("top view should show item0:\n%s", got)
	}

	l.ScrollToBottom()
	if !l.AtBottom() {
		t.Error("ScrollToBottom must leave the list at bottom")
	}
	got := l.Render()
	if !strings.Contains(lastNonEmpty(got), "item19") {
		t.Errorf("bottom view should end on the last item, last row = %q\n%s",
			lastNonEmpty(got), plain(got))
	}
}

// A list follows the tail by default and stays pinned when its last item grows
// in place past the viewport (the streaming case: SetItems sees the mutated
// slice, so follow must be tracked explicitly, not derived from AtBottom).
func TestList_FollowsTailThroughInPlaceGrowth(t *testing.T) {
	items := []Item{lineItem("1", "start")}
	l := newTestList(80, 5, items...)
	if !l.Following() {
		t.Fatal("a new list should follow the tail by default")
	}
	l.ScrollToBottom()

	// Grow the item well past the viewport and re-sync as refreshViewport does.
	items[0] = Item{
		ID:      items[0].ID,
		Kind:    assistant.KindReasoning,
		Role:    assistant.RoleAssistant,
		Text:    strings.Repeat("word ", 200),
		Version: 1,
	}
	l.SetItems(items)
	// Render self-heals the pin — no explicit ScrollToBottom needed.
	if !strings.Contains(lastNonEmpty(l.Render()), "word") || !l.AtBottom() {
		t.Error("list must remain pinned to the tail after in-place growth")
	}
}

// Scrolling down by one item + its gap advances the top item; scrolling back up
// restores it (offset math over the (itemIdx, lineOffset) model).
func TestList_ScrollByAdvancesAndReverses(t *testing.T) {
	l, _ := makeOverflowingList(t, 20, 5)
	l.ScrollToTop()

	l.ScrollBy(2) // one content row + one gap row = past item0
	if first := lines(l.Render())[0]; !strings.Contains(first, "item1") {
		t.Errorf("after scrolling past item0, top = %q", first)
	}

	l.ScrollBy(-2)
	if first := lines(l.Render())[0]; !strings.Contains(first, "item0") {
		t.Errorf("scrolling back up should restore item0 at top, got %q", first)
	}
}

// PageDown from the top reaches the bottom; PageUp returns toward the top.
func TestList_PagingClampsAtEnds(t *testing.T) {
	// 4 single-line items with gaps = 7 rows, just over the 5-row page.
	l, _ := makeOverflowingList(t, 4, 5)
	l.ScrollToTop()

	l.PageDown()
	if !l.AtBottom() {
		t.Errorf("PageDown over a list a bit taller than one page should reach bottom")
	}

	l.PageUp()
	if l.AtBottom() {
		t.Errorf("PageUp should move away from the bottom")
	}
	// Never scroll above the first item.
	l.PageUp()
	l.PageUp()
	if first := lines(l.Render())[0]; !strings.Contains(first, "item0") {
		t.Errorf("repeated PageUp should clamp at item0, got %q", first)
	}
}
