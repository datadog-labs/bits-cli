package conversations

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func pickerKey(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func TestPickerLoadingEmptyErrorRetryAndCancel(t *testing.T) {
	m := New(40, 12)
	if m.State() != StateLoading || !strings.Contains(m.View(), "Loading conversations") {
		t.Fatalf("initial picker = %v %q", m.State(), m.View())
	}
	_, cmd := m.Update(pickerKey(tea.KeyEscape, ""))
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatalf("loading escape emitted %T", cmd())
	}
	m.SetConversations(nil)
	if m.State() != StateEmpty || !strings.Contains(m.View(), "No conversations") {
		t.Fatalf("empty picker = %v %q", m.State(), m.View())
	}
	m.SetError("Could not load conversations: unavailable", errors.New("unavailable"))
	if m.State() != StateError || !strings.Contains(m.View(), "unavailable") {
		t.Fatalf("error picker = %v %q", m.State(), m.View())
	}
	_, cmd = m.Update(pickerKey(tea.KeyEnter, ""))
	if _, ok := cmd().(RetryMsg); !ok {
		t.Fatalf("retry emitted %T", cmd())
	}
}

func TestPickerClosingIsNonInteractive(t *testing.T) {
	m := New(60, 16)
	m.SetConversations([]assistant.ConversationSummary{{ConversationID: "one", Title: "One"}})
	m.SetClosing()

	for _, msg := range []tea.Msg{
		pickerKey('x', "x"),
		pickerKey(tea.KeyEnter, ""),
		pickerKey(tea.KeyEscape, ""),
	} {
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		if cmd != nil {
			t.Fatalf("closing picker emitted command for %T", msg)
		}
	}
	if m.State() != StateClosing || m.Query() != "" || !strings.Contains(m.View(), "Closing…") {
		t.Fatalf("closing picker changed state: state=%v query=%q view=%q", m.State(), m.Query(), m.View())
	}
}

func TestPickerDistinguishesHistoryLoadingAndSanitizesErrors(t *testing.T) {
	m := New(40, 12)
	m.SetLoading(OperationOpen)
	if view := m.View(); !strings.Contains(view, "Loading conversation…") || strings.Contains(view, "Loading conversations…") {
		t.Fatalf("history loading view = %q", view)
	}
	raw := errors.New("\x1b[31mraw\a backend detail")
	m.SetError("Could not open conversation: \x1b[31mraw\x1b[0m\nbackend detail", raw)
	if strings.ContainsAny(m.errorMessage, "\x1b\a\n") || m.errorMessage != "Could not open conversation: raw backend detail" {
		t.Fatalf("sanitized error view = %q", m.View())
	}
	if m.err != raw {
		t.Fatal("original error was not retained for diagnostics")
	}
}

func TestPickerShowsSearchBarAndMalformedRecordWarning(t *testing.T) {
	m := New(60, 16)
	m.SetConversations([]assistant.ConversationSummary{{ConversationID: "one", Title: "One"}})
	m.SetWarning("1 malformed conversation record omitted")
	view := escape.SingleLine(m.View())
	if !strings.Contains(view, "> Type to search") || !strings.Contains(view, "1 malformed conversation") {
		t.Fatalf("ready picker guidance = %q", m.View())
	}
	if strings.Contains(view, "1 conversation") {
		t.Fatalf("ready picker should not render a conversation count: %q", m.View())
	}
	if m.list.Height() != 2 {
		t.Fatalf("list height = %d, want 2", m.list.Height())
	}
	body := m.panelBody(m.panelBodyWidth())
	lines := strings.Split(body, "\n")
	if len(lines) < 3 || lines[1] != "" {
		t.Fatalf("search row should have a blank line before results: %q", body)
	}
}

func TestSearchLineNeverExceedsPickerWidth(t *testing.T) {
	for _, width := range []int{20, 60} {
		m := New(width, 12)
		bodyWidth := m.panelBodyWidth()
		wantInputWidth := max(0, bodyWidth-ansi.StringWidth(m.search.Prompt)-1)
		if got := m.search.Width(); got != wantInputWidth {
			t.Fatalf("width %d configured input width %d, want %d", width, got, wantInputWidth)
		}
		for _, value := range []string{"", strings.Repeat("x", 128)} {
			m.search.SetValue(value)
			firstLine := strings.SplitN(m.panelBody(bodyWidth), "\n", 2)[0]
			if got := ansi.StringWidth(firstLine); got > bodyWidth {
				t.Fatalf("width %d rendered a %d-cell search line in a %d-cell body for %d chars: %q", width, got, bodyWidth, len(value), firstLine)
			}
		}
	}
}

func TestPickerHelpUsesConciseStateAwareActions(t *testing.T) {
	m := New(120, 18)
	summaries := make([]assistant.ConversationSummary, 10)
	for i := range summaries {
		summaries[i] = assistant.ConversationSummary{
			ConversationID: string(rune('a' + i)),
			Title:          "Conversation " + string(rune('A'+i)),
		}
	}
	m.SetConversations(summaries)

	view := escape.SingleLine(m.View())
	if !strings.Contains(view, "↑/↓ navigate") || strings.Count(view, "navigate") != 1 {
		t.Fatalf("navigation help should use one combined binding: %q", m.View())
	}
	if !strings.Contains(view, "esc ×") || strings.Contains(view, "esc cancel") {
		t.Fatalf("dismiss help should match the site picker: %q", m.View())
	}
	if !strings.Contains(view, "←/→ page") || strings.Contains(view, "? more") {
		t.Fatalf("picker should show page arrows without expandable help: %q", m.View())
	}
	if strings.Contains(view, "enter select") {
		t.Fatalf("picker should not advertise enter selection: %q", m.View())
	}

	m, _ = m.Update(pickerKey(tea.KeyRight, ""))
	if m.list.Paginator.Page != 1 {
		t.Fatalf("right arrow did not advance page: %d", m.list.Paginator.Page)
	}
	m, _ = m.Update(pickerKey(tea.KeyLeft, ""))
	if m.list.Paginator.Page != 0 {
		t.Fatalf("left arrow did not return to previous page: %d", m.list.Paginator.Page)
	}

	m, _ = m.Update(pickerKey('c', "c"))
	view = escape.SingleLine(m.View())
	if !strings.Contains(view, "esc ×") || strings.Contains(view, "esc clear") || strings.Contains(view, "clear filter") {
		t.Fatalf("active search should retain the same dismiss label: %q", m.View())
	}

	m, _ = m.Update(pickerKey(tea.KeyEscape, ""))
	view = escape.SingleLine(m.View())
	if m.Query() != "" || !strings.Contains(view, "esc ×") || strings.Contains(view, "esc clear") {
		t.Fatalf("cleared search should restore only the cancel action: query=%q view=%q", m.Query(), m.View())
	}
}

func TestPickerHidesPageHelpWithoutPaginationAndExplainsNoMatches(t *testing.T) {
	m := New(120, 18)
	m.SetConversations([]assistant.ConversationSummary{{ConversationID: "one", Title: "Alpha"}})
	if view := escape.SingleLine(m.View()); strings.Contains(view, "←/→ page") {
		t.Fatalf("single-page picker advertised pagination: %q", m.View())
	}

	for _, r := range "missing" {
		m, _ = m.Update(pickerKey(r, string(r)))
	}
	view := escape.SingleLine(m.View())
	if !strings.Contains(view, "No conversations match your search.") {
		t.Fatalf("zero-match picker = %q", m.View())
	}
	if !strings.Contains(view, "esc ×") || strings.Contains(view, "esc clear") || strings.Contains(view, "←/→ page") {
		t.Fatalf("zero-match help is misleading: %q", m.View())
	}
}

func TestSearchUsesCaseInsensitiveSubstringAndPreservesNewestFirstOrder(t *testing.T) {
	m := New(80, 18)
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "unrelated", UpdatedAt: 300, Title: "Create a new ordered Datadog dashboard for: POST /api/unstable/interpolate-widget service:morpheus-widget-toolkit"},
		{ConversationID: "older", UpdatedAt: 100, Title: "SELECT COALESCE(attempt_method, success_method)"},
		{ConversationID: "newer", UpdatedAt: 200, Title: "Notes about coalesce usage"},
	})

	for _, r := range "coalesce" {
		m, _ = m.Update(pickerKey(r, string(r)))
	}
	visible := m.list.VisibleItems()
	if len(visible) != 2 {
		t.Fatalf("visible matches = %+v, want only 2 contiguous matches", visible)
	}
	if first := visible[0].(conversationItem).summary.ConversationID; first != "newer" {
		t.Fatalf("first filtered conversation = %q, want newest conversation", first)
	}
	if second := visible[1].(conversationItem).summary.ConversationID; second != "older" {
		t.Fatalf("second filtered conversation = %q, want older case-insensitive match", second)
	}
}

func TestPickerSortsFiltersAndDropsMalformedIDs(t *testing.T) {
	m := New(60, 16)
	input := []assistant.ConversationSummary{
		{ConversationID: "c", UpdatedAt: 10},
		{ConversationID: "b", UpdatedAt: 20, Title: "Beta"},
		{ConversationID: "a", UpdatedAt: 20, Title: "Alpha"},
		{ID: "json-api-only", UpdatedAt: 30, Title: "Malformed"},
		{ConversationID: "   ", UpdatedAt: 40},
	}
	m.SetConversations(input)
	items := m.list.Items()
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 valid canonical ids", len(items))
	}
	want := []string{"a", "b", "c"}
	for i, id := range want {
		if got := items[i].(conversationItem).summary.ConversationID; got != id {
			t.Fatalf("item %d = %q, want %q", i, got, id)
		}
	}
	if got := items[2].(conversationItem).Title(); got != "Untitled conversation" {
		t.Fatalf("fallback title = %q", got)
	}
	m.list.SetFilterText("beta")
	visible := m.list.VisibleItems()
	if len(visible) != 1 || visible[0].(conversationItem).summary.ConversationID != "b" {
		t.Fatalf("filter results = %+v", visible)
	}
	if input[0].ConversationID != "c" {
		t.Fatal("picker mutated caller input")
	}
}

func TestPersistentSearchTypingSelectionAndEscape(t *testing.T) {
	m := New(40, 12)
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "one", Title: "Alpha"},
		{ConversationID: "two", Title: "Beta"},
	})
	for _, r := range "beta" {
		m, _ = m.Update(pickerKey(r, string(r)))
	}
	if m.Query() != "beta" {
		t.Fatalf("query = %q", m.Query())
	}
	visible := m.list.VisibleItems()
	if len(visible) != 1 || visible[0].(conversationItem).summary.ConversationID != "two" {
		t.Fatalf("typed filter results = %+v", visible)
	}
	_, cmd := m.Update(pickerKey(tea.KeyEnter, ""))
	selected, ok := cmd().(SelectedMsg)
	if !ok || selected.Conversation.ConversationID != "two" {
		t.Fatalf("selection = %#v", selected)
	}

	m, cmd = m.Update(pickerKey(tea.KeyEscape, ""))
	if cmd != nil || m.Query() != "" || len(m.list.VisibleItems()) != 2 {
		t.Fatalf("first escape did not clear query: cmd=%v query=%q visible=%d", cmd != nil, m.Query(), len(m.list.VisibleItems()))
	}
	_, cmd = m.Update(pickerKey(tea.KeyEscape, ""))
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatalf("second escape emitted %T", cmd())
	}
}

func TestSearchPersistsAcrossLoadingAndArrowsNavigateResults(t *testing.T) {
	m := New(50, 12)
	for _, r := range "al" {
		m, _ = m.Update(pickerKey(r, string(r)))
	}
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "one", Title: "Alpha"},
		{ConversationID: "two", Title: "Alpine"},
		{ConversationID: "three", Title: "Beta"},
	})
	if m.Query() != "al" || len(m.list.VisibleItems()) != 2 {
		t.Fatalf("post-load search: query=%q visible=%d", m.Query(), len(m.list.VisibleItems()))
	}
	m, _ = m.Update(pickerKey(tea.KeyDown, ""))
	_, cmd := m.Update(pickerKey(tea.KeyEnter, ""))
	selected, ok := cmd().(SelectedMsg)
	if !ok || selected.Conversation.ConversationID != "two" {
		t.Fatalf("arrow-selected result = %#v", selected)
	}
}

func TestPastingIntoSearchFiltersImmediately(t *testing.T) {
	m := New(50, 12)
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "one", Title: "Alpha"},
		{ConversationID: "two", Title: "Beta"},
	})
	m, _ = m.Update(tea.PasteMsg{Content: "beta"})
	visible := m.list.VisibleItems()
	if m.Query() != "beta" || len(visible) != 1 || visible[0].(conversationItem).summary.ConversationID != "two" {
		t.Fatalf("paste filter: query=%q visible=%+v", m.Query(), visible)
	}
}

func TestPickerSanitizesTitlesBeforeRendering(t *testing.T) {
	m := New(60, 12)
	m.SetConversations([]assistant.ConversationSummary{{
		ConversationID: "12345678-1234-1234-1234-123456781234",
		Title:          "\x1b[31mDanger\x1b[0m\nnext\a",
	}})
	title := m.list.Items()[0].(conversationItem).Title()
	if title != "Danger next" || strings.ContainsAny(title, "\x1b\a\n") {
		t.Fatalf("sanitized title = %q", title)
	}
}

func TestConversationItemShowsRelativeUpdateTimeInsteadOfID(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	item := conversationItem{
		summary: assistant.ConversationSummary{
			ConversationID: "12345678-1234-1234-1234-123456781234",
			UpdatedAt:      now.Add(-5 * time.Hour).UnixMilli(),
		},
		now: func() time.Time { return now },
	}
	if got := item.Description(); got != "5 hours ago" {
		t.Fatalf("description = %q, want relative update time", got)
	}
	if strings.Contains(item.Description(), item.summary.ConversationID) {
		t.Fatalf("description exposes conversation ID: %q", item.Description())
	}
}

func TestRelativeUpdatedAt(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		updatedAt int64
		want      string
	}{
		{name: "unavailable", updatedAt: 0, want: "Activity time unavailable"},
		{name: "future clock skew", updatedAt: now.Add(time.Minute).UnixMilli(), want: "just now"},
		{name: "sub-second", updatedAt: now.Add(-500 * time.Millisecond).UnixMilli(), want: "just now"},
		{name: "one second", updatedAt: now.Add(-time.Second).UnixMilli(), want: "1 second ago"},
		{name: "seconds", updatedAt: now.Add(-3 * time.Second).UnixMilli(), want: "3 seconds ago"},
		{name: "minutes", updatedAt: now.Add(-12 * time.Minute).UnixMilli(), want: "12 minutes ago"},
		{name: "hours", updatedAt: now.Add(-5 * time.Hour).UnixMilli(), want: "5 hours ago"},
		{name: "days", updatedAt: now.Add(-2 * 24 * time.Hour).UnixMilli(), want: "2 days ago"},
		{name: "weeks", updatedAt: now.Add(-3 * 7 * 24 * time.Hour).UnixMilli(), want: "3 weeks ago"},
		{name: "months", updatedAt: now.Add(-4 * 30 * 24 * time.Hour).UnixMilli(), want: "4 months ago"},
		{name: "years", updatedAt: now.Add(-2 * 365 * 24 * time.Hour).UnixMilli(), want: "2 years ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativeUpdatedAt(tt.updatedAt, now); got != tt.want {
				t.Fatalf("relativeUpdatedAt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConversationDelegateEmphasizesTitleOverTimestamp(t *testing.T) {
	delegate := newConversationDelegate()
	if !delegate.Styles.NormalTitle.GetBold() || !delegate.Styles.SelectedTitle.GetBold() {
		t.Fatal("conversation titles should be bold in normal and selected states")
	}
	if delegate.Styles.NormalDesc.GetBold() || delegate.Styles.SelectedDesc.GetBold() {
		t.Fatal("timestamps should not be bold")
	}
	wantTimestamp := styles.Default(true).Text.Tertiary.GetForeground()
	for name, style := range map[string]lipgloss.Style{
		"NormalDesc":   delegate.Styles.NormalDesc,
		"SelectedDesc": delegate.Styles.SelectedDesc,
		"DimmedDesc":   delegate.Styles.DimmedDesc,
	} {
		if style.GetFaint() {
			t.Errorf("%s sets Faint; the level supplies the dimming", name)
		}
		if got := style.GetForeground(); !reflect.DeepEqual(got, wantTimestamp) {
			t.Errorf("%s foreground = %v, want tertiary %v", name, got, wantTimestamp)
		}
	}
	// The selected row must not promote the timestamp to the title's accent.
	if !reflect.DeepEqual(delegate.Styles.NormalDesc.GetForeground(), delegate.Styles.SelectedDesc.GetForeground()) {
		t.Fatal("selected timestamp should stay subdued instead of inheriting the title accent")
	}
}
