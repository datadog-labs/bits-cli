package conversations

import (
	"errors"
	"fmt"
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
	if !strings.Contains(view, "⌕") || !strings.Contains(view, "1 malformed conversation") {
		t.Fatalf("ready picker guidance = %q", m.View())
	}
	if !strings.Contains(view, "Resume a session (1 of 1)") || !strings.Contains(view, "ESC x") {
		t.Fatalf("ready picker header = %q", m.View())
	}
	if !strings.Contains(view, "Type to Search") || strings.Contains(view, "Esc to exit search") {
		t.Fatalf("search row copy = %q", m.View())
	}
	if m.list.Height() != 3 {
		t.Fatalf("list height = %d, want 3", m.list.Height())
	}
	body := m.panelBody(m.panelBodyWidth())
	lines := strings.Split(body, "\n")
	if len(lines) < 3 || lines[1] != "" {
		t.Fatalf("search row should have a blank line before results: %q", body)
	}
}

func TestResumePanelAccentIsLocalToPicker(t *testing.T) {
	theme := styles.Default(true)
	originalBorder := theme.Panel.Frame.GetBorderTopForeground()
	panel := resumePanelStyles(theme)

	if got, want := panel.Frame.GetBorderTopForeground(), theme.Selector.Selected.GetForeground(); got != want {
		t.Fatalf("resume border = %v, want interactive %v", got, want)
	}
	if got := theme.Panel.Frame.GetBorderTopForeground(); got != originalBorder {
		t.Fatalf("resume styling mutated shared panel border: got %v, want %v", got, originalBorder)
	}
}

func TestSearchLineNeverExceedsPickerWidth(t *testing.T) {
	for _, width := range []int{20, 60} {
		m := New(width, 12)
		bodyWidth := m.panelBodyWidth()
		wantInputWidth := max(0, bodyWidth-1-ansi.StringWidth(m.search.Prompt)-1)
		if got := m.search.Width(); got != wantInputWidth {
			t.Fatalf("width %d configured input width %d, want %d", width, got, wantInputWidth)
		}
		for _, value := range []string{"", strings.Repeat("x", 128)} {
			m.search.SetValue(value)
			for lineNo, line := range strings.Split(m.searchView(bodyWidth), "\n") {
				if got := ansi.StringWidth(line); got > bodyWidth {
					t.Fatalf("width %d rendered a %d-cell search line %d in a %d-cell body for %d chars: %q", width, got, lineNo+1, bodyWidth, len(value), line)
				}
			}
		}
	}
}

func TestPickerHeaderAndOverflowTrackSelection(t *testing.T) {
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
	if !strings.Contains(view, "Resume a session (1 of 10)") || !strings.Contains(view, "↓ 4 more below") {
		t.Fatalf("initial picker position = %q", m.View())
	}
	if !strings.Contains(view, "ESC x") || strings.Contains(view, "navigate") || strings.Contains(view, "page") {
		t.Fatalf("picker should only show the compact dismiss and overflow hints: %q", m.View())
	}

	for range 6 {
		m, _ = m.Update(pickerKey(tea.KeyDown, ""))
	}
	view = escape.SingleLine(m.View())
	if !strings.Contains(view, "Resume a session (7 of 10)") || !strings.Contains(view, "↓ 3 more below") {
		t.Fatalf("scrolled picker position = %q", m.View())
	}

	for _, want := range []string{"↓ 2 more below", "↓ 1 more below", "↓ back to top"} {
		m, _ = m.Update(pickerKey(tea.KeyDown, ""))
		if got := m.overflowHint(); got != want {
			t.Fatalf("overflow hint after scrolling = %q, want %q", got, want)
		}
	}
	view = escape.SingleLine(m.View())
	if !strings.Contains(view, "Resume a session (10 of 10)") || !strings.Contains(view, "↓ back to top") {
		t.Fatalf("last picker position = %q", m.View())
	}
	m, _ = m.Update(pickerKey(tea.KeyDown, ""))
	view = escape.SingleLine(m.View())
	if !strings.Contains(view, "Resume a session (10 of 10)") || !strings.Contains(view, "↓ back to top") {
		t.Fatalf("down from the last result should remain clamped: %q", m.View())
	}

	m, _ = m.Update(pickerKey('c', "c"))
	view = escape.SingleLine(m.View())
	if !strings.Contains(view, "ESC x") || strings.Contains(view, "Esc to exit search") || strings.Contains(view, "clear filter") {
		t.Fatalf("active search should retain the same dismiss label: %q", m.View())
	}

	m, _ = m.Update(pickerKey(tea.KeyEscape, ""))
	view = escape.SingleLine(m.View())
	if m.Query() != "" || !strings.Contains(view, "ESC x") || strings.Contains(view, "esc clear") {
		t.Fatalf("cleared search should restore only the cancel action: query=%q view=%q", m.Query(), m.View())
	}
}

func TestPickerPageKeysStillRouteToConversationList(t *testing.T) {
	m := New(120, 18)
	summaries := make([]assistant.ConversationSummary, 14)
	for i := range summaries {
		summaries[i] = assistant.ConversationSummary{
			ConversationID: string(rune('a' + i)),
			Title:          "Conversation " + string(rune('A'+i)),
		}
	}
	m.SetConversations(summaries)

	m, _ = m.Update(pickerKey(tea.KeyRight, ""))
	if m.list.Paginator.Page != 1 || m.list.Index() != m.list.Paginator.PerPage {
		t.Fatalf("right arrow did not advance one result page: page=%d index=%d perPage=%d", m.list.Paginator.Page, m.list.Index(), m.list.Paginator.PerPage)
	}
	page := ansi.Strip(m.conversationListView(m.panelBodyWidth()))
	if !strings.Contains(page, "Conversation G") || !strings.Contains(page, "Conversation L") || strings.Contains(page, "Conversation B") {
		t.Fatalf("right arrow rendered an overlapping page: %q", page)
	}
	if hint := m.overflowHint(); hint != "↓ 2 more below" {
		t.Fatalf("second-page overflow hint = %q, want %q", hint, "↓ 2 more below")
	}
	m, _ = m.Update(pickerKey(tea.KeyRight, ""))
	page = ansi.Strip(m.conversationListView(m.panelBodyWidth()))
	if !strings.Contains(page, "Conversation M") || !strings.Contains(page, "Conversation N") || strings.Contains(page, "Conversation L") {
		t.Fatalf("partial final page pulled in rows from the previous page: %q", page)
	}
	if hint := m.overflowHint(); hint != "↓ back to top" {
		t.Fatalf("final-page overflow hint = %q, want %q", hint, "↓ back to top")
	}
	m, _ = m.Update(pickerKey(tea.KeyLeft, ""))
	if m.list.Paginator.Page != 1 || m.list.Index() != m.list.Paginator.PerPage {
		t.Fatalf("left arrow did not return to the second page: page=%d index=%d", m.list.Paginator.Page, m.list.Index())
	}
	m, _ = m.Update(pickerKey(tea.KeyLeft, ""))
	if m.list.Paginator.Page != 0 || m.list.Index() != 0 {
		t.Fatalf("left arrow did not return to the first result: page=%d index=%d", m.list.Paginator.Page, m.list.Index())
	}
}

func TestPickerIsBoundedAtResponsiveWidthsAndWithUnicode(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, width := range []int{40, 80, 120} {
			t.Run(fmt.Sprintf("dark=%t/width=%d", dark, width), func(t *testing.T) {
				m := New(width, 24, styles.Default(dark))
				m.SetConversations([]assistant.ConversationSummary{
					{ConversationID: "one", Title: "調査 🔎 this conversation title is deliberately long"},
					{ConversationID: "two", Title: "Résumé café"},
				})
				view := m.View()
				if got := lipgloss.Width(view); got > width {
					t.Fatalf("view width = %d, want <= %d", got, width)
				}
				if got := lipgloss.Height(view); got > 24 {
					t.Fatalf("view height = %d, want <= 24", got)
				}
				plain := ansi.Strip(view)
				if !strings.Contains(plain, "Resume a session") || !strings.Contains(plain, "Résumé café") {
					t.Fatalf("responsive view lost picker content: %q", plain)
				}
				if width >= 80 && !strings.Contains(plain, "Resume a session (1 of 2)") {
					t.Fatalf("wide picker lost its position count: %q", plain)
				}
				if strings.Contains(plain, "Esc to exit search") {
					t.Fatalf("responsive view added redundant search instructions: %q", plain)
				}
			})
		}
	}
}

func TestPickerRemainsUsableWithoutColor(t *testing.T) {
	theme := styles.Default(true)
	noForeground := func(style lipgloss.Style) lipgloss.Style { return style.UnsetForeground() }
	theme.Input.Cursor = lipgloss.NoColor{}
	theme.Panel.Frame = theme.Panel.Frame.UnsetForeground().UnsetBackground().UnsetBorderForeground().UnsetBorderBackground()
	theme.Panel.Title = noForeground(theme.Panel.Title)
	theme.Panel.Dismiss = noForeground(theme.Panel.Dismiss)
	theme.Panel.Help = noForeground(theme.Panel.Help)
	theme.Panel.Compact = noForeground(theme.Panel.Compact)
	theme.Text.Primary = noForeground(theme.Text.Primary)
	theme.Text.Secondary = noForeground(theme.Text.Secondary)
	theme.Text.Tertiary = noForeground(theme.Text.Tertiary)
	theme.Selector.Item = noForeground(theme.Selector.Item)
	theme.Selector.Selected = noForeground(theme.Selector.Selected)
	theme.Selector.Detail = noForeground(theme.Selector.Detail)
	theme.Selector.SelectedDetail = noForeground(theme.Selector.SelectedDetail)
	theme.TextInput.Focused.Text = noForeground(theme.TextInput.Focused.Text)
	theme.TextInput.Focused.Prompt = noForeground(theme.TextInput.Focused.Prompt)
	theme.TextInput.Focused.Placeholder = noForeground(theme.TextInput.Focused.Placeholder)
	theme.TextInput.Focused.Suggestion = noForeground(theme.TextInput.Focused.Suggestion)
	theme.TextInput.Cursor.Color = lipgloss.NoColor{}

	m := New(80, 18, theme)
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "one", Title: "Selected conversation"},
		{ConversationID: "two", Title: "Another conversation"},
	})
	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "› Selected conversation") || !strings.Contains(plain, "  Another conversation") {
		t.Fatalf("colorless picker lost its selection affordance: %q", plain)
	}
	if strings.Contains(view, "\x1b[38") || strings.Contains(view, "\x1b[48") {
		t.Fatalf("colorless picker emitted foreground/background color escapes: %q", view)
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
	if !strings.Contains(view, "ESC x") || strings.Contains(view, "Esc to exit search") || strings.Contains(view, "more below") {
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

func TestConversationRowsUseSelectorTextRoles(t *testing.T) {
	theme := styles.Default(true)
	m := New(80, 18, theme)
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.SetConversations([]assistant.ConversationSummary{
		{ConversationID: "one", Title: "Selected", UpdatedAt: now.Add(-time.Hour).UnixMilli()},
		{ConversationID: "two", Title: "Normal", UpdatedAt: now.Add(-48 * time.Hour).UnixMilli()},
	})

	view := m.conversationListView(60)
	if !strings.Contains(view, theme.Selector.Selected.Render("› Selected")) ||
		!strings.Contains(view, theme.Selector.Selected.Render("1 hour ago")) {
		t.Fatalf("selected title and timestamp should use interactive text: %q", view)
	}
	if !strings.Contains(view, theme.Text.Secondary.Render("  Normal  ")) ||
		!strings.Contains(view, theme.Text.Tertiary.Render("2 days ago")) {
		t.Fatalf("normal title and timestamp use the wrong text roles: %q", view)
	}
}
