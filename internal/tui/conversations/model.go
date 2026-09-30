// Package conversations owns the local /resume picker state and filtering.
// Network operations remain in the root TUI model.
package conversations

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

type State int

const (
	StateLoading State = iota
	StateReady
	StateEmpty
	StateError
)

const maxVisibleConversationRows = 10

type Operation int

const (
	OperationList Operation = iota
	OperationOpen
)

type (
	SelectedMsg  struct{ Conversation assistant.ConversationSummary }
	CancelledMsg struct{}
	RetryMsg     struct{}
)

type conversationItem struct {
	summary assistant.ConversationSummary
	now     func() time.Time
}

func (i conversationItem) Title() string { return SafeTitle(i.summary) }
func (i conversationItem) Description() string {
	return RelativeUpdatedAt(i.summary.UpdatedAt, i.now())
}
func (i conversationItem) FilterValue() string { return SafeTitle(i.summary) }

type Model struct {
	list         list.Model
	search       textinput.Model
	panel        *components.Panel
	theme        styles.Theme
	state        State
	operation    Operation
	err          error // retained for diagnostics; never rendered directly
	errorMessage string
	warning      string
	width        int
	height       int
	frame        int
	windowStart  int
	now          func() time.Time
}

func New(width, height int, themes ...styles.Theme) Model {
	theme := styles.Default(true)
	if len(themes) > 0 {
		theme = themes[0]
	}
	delegate := newConversationDelegate()
	model := list.New(nil, delegate, max(width, 1), max(height, 1))
	// Search narrows the API's newest-first order using a contiguous,
	// case-insensitive title match. Fuzzy subsequence matching creates surprising
	// positives for long conversation titles.
	model.Filter = substringFilter
	model.SetShowTitle(false)
	model.SetShowFilter(false)
	model.DisableQuitKeybindings()
	// Filtering is driven by the persistent search input below. Disable the
	// list's modal "/ filter" key while continuing to apply queries explicitly
	// through SetFilterText.
	model.SetFilteringEnabled(false)
	model.SetShowHelp(false)
	model.SetShowStatusBar(false)
	model.SetShowPagination(false)
	configureConversationHelp(&model)
	search := textinput.New()
	search.Prompt = "⌕ "
	search.Placeholder = "Type to Search"
	search.CharLimit = 128
	search.Focus()
	m := Model{
		list: model, search: search, panel: components.NewPanel(theme.Panel), theme: theme,
		state: StateLoading, operation: OperationList,
		width: max(width, 1), height: max(height, 1), now: time.Now,
	}
	m.search.SetStyles(resumeSearchStyles(theme))
	m.resizeChildren()
	return m
}

func (m Model) State() State  { return m.state }
func (m Model) Query() string { return m.search.Value() }

// SetFrame uses the root's shared animation clock; this component owns no ticks.
func (m *Model) SetFrame(frame int) { m.frame = frame }

func (m Model) Animating() bool {
	return m.state == StateLoading && m.theme.Chat.StatusSpinner.Len() > 0
}

func (m *Model) SetLoading(operation Operation) {
	m.state = StateLoading
	m.operation = operation
	m.err = nil
	m.errorMessage = ""
	m.warning = ""
	m.frame = 0
	if operation == OperationOpen {
		m.search.Blur()
	}
	m.resizeChildren()
}

// Ordered normalises a conversation list for display: entries without the
// canonical conversation_id are dropped, and the rest are sorted newest first
// with the id as a stable tiebreak. The API's JSON:API item id is redundant
// today, but is not the documented request key and must not silently
// substitute for malformed data.
// The picker and the startup resume offer both call it, so neither can drift
// on what "recent" means.
func Ordered(summaries []assistant.ConversationSummary) []assistant.ConversationSummary {
	ordered := make([]assistant.ConversationSummary, 0, len(summaries))
	for _, summary := range summaries {
		if strings.TrimSpace(summary.ConversationID) != "" {
			ordered = append(ordered, summary)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].UpdatedAt != ordered[j].UpdatedAt {
			return ordered[i].UpdatedAt > ordered[j].UpdatedAt
		}
		return ordered[i].ConversationID < ordered[j].ConversationID
	})
	return ordered
}

// SetConversations replaces the picker's rows with the normalised list.
func (m *Model) SetConversations(summaries []assistant.ConversationSummary) tea.Cmd {
	ordered := Ordered(summaries)
	items := make([]list.Item, len(ordered))
	for i := range ordered {
		items[i] = conversationItem{summary: ordered[i], now: m.now}
	}
	m.err = nil
	m.search.Focus()
	if len(items) == 0 {
		m.state = StateEmpty
	} else {
		m.state = StateReady
	}
	m.list.ResetFilter()
	m.list.ResetSelected()
	m.windowStart = 0
	cmd := m.list.SetItems(items)
	m.applySearch()
	return cmd
}

func newConversationDelegate() list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	return delegate
}

func resumeSearchStyles(theme styles.Theme) textinput.Styles {
	search := theme.TextInput
	search.Cursor.Color = theme.Input.Cursor
	return search
}

func configureConversationHelp(model *list.Model) {
	// The persistent search field owns printable keys, Home, and End, so only
	// advertise list shortcuts that the picker actually routes to the list.
	model.KeyMap.CursorUp.SetKeys("up")
	model.KeyMap.CursorUp.SetHelp("↑/↓", "navigate")
	model.KeyMap.CursorDown.Unbind()
	model.KeyMap.PrevPage.SetKeys("left", "pgup")
	model.KeyMap.NextPage.SetKeys("right", "pgdown")
	model.KeyMap.GoToStart.Unbind()
	model.KeyMap.GoToEnd.Unbind()
	model.KeyMap.ClearFilter.Unbind()
	model.KeyMap.ShowFullHelp.Unbind()
	model.KeyMap.CloseFullHelp.Unbind()
}

func substringFilter(term string, targets []string) []list.Rank {
	needle := foldedRunes(term)
	if len(needle) == 0 {
		ranks := make([]list.Rank, len(targets))
		for i := range targets {
			ranks[i] = list.Rank{Index: i}
		}
		return ranks
	}

	ranks := make([]list.Rank, 0, len(targets))
	for i, target := range targets {
		start := runeSliceIndex(foldedRunes(target), needle)
		if start < 0 {
			continue
		}
		matched := make([]int, len(needle))
		for j := range needle {
			matched[j] = start + j
		}
		ranks = append(ranks, list.Rank{Index: i, MatchedIndexes: matched})
	}
	return ranks
}

func foldedRunes(value string) []rune {
	runes := []rune(value)
	for i, r := range runes {
		runes[i] = unicode.ToLower(r)
	}
	return runes
}

func runeSliceIndex(haystack, needle []rune) int {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for i := range needle {
			if haystack[start+i] != needle[i] {
				matched = false
				break
			}
		}
		if matched {
			return start
		}
	}
	return -1
}

func (m *Model) updateHelp() {
	escapeDescription := "cancel"
	if m.search.Value() != "" {
		escapeDescription = "clear"
	}
	escapeKey := key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", escapeDescription))
	selectKey := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select"))
	bindings := []key.Binding{escapeKey, selectKey}
	if m.list.Paginator.TotalPages > 1 {
		pageKey := key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←/→", "page"))
		bindings = append([]key.Binding{pageKey}, bindings...)
	}
	m.list.AdditionalShortHelpKeys = func() []key.Binding {
		return bindings
	}
}

// RelativeUpdatedAt renders updatedAt relative to now for display.
func RelativeUpdatedAt(updatedAt int64, now time.Time) string {
	if updatedAt <= 0 {
		return "Activity time unavailable"
	}

	elapsed := now.Sub(time.UnixMilli(updatedAt))
	if elapsed < time.Second {
		return "just now"
	}

	type interval struct {
		duration time.Duration
		name     string
	}
	intervals := []interval{
		{365 * 24 * time.Hour, "year"},
		{30 * 24 * time.Hour, "month"},
		{7 * 24 * time.Hour, "week"},
		{24 * time.Hour, "day"},
		{time.Hour, "hour"},
		{time.Minute, "minute"},
		{time.Second, "second"},
	}
	for _, candidate := range intervals {
		if elapsed >= candidate.duration {
			count := int64(elapsed / candidate.duration)
			name := candidate.name
			if count != 1 {
				name += "s"
			}
			return fmt.Sprintf("%d %s ago", count, name)
		}
	}
	return "just now"
}

func (m *Model) SetError(message string, err error) {
	m.state = StateError
	m.err = err
	m.errorMessage = ansi.Truncate(escape.SingleLine(message), 240, "…")
}

func (m *Model) SetWarning(message string) {
	m.warning = ansi.Truncate(escape.SingleLine(message), 120, "…")
	m.resizeChildren()
}

func (m *Model) SetSize(width, height int) {
	m.width = max(width, 1)
	m.height = max(height, 1)
	m.resizeChildren()
}

// SetStyles applies the root terminal theme without resetting picker state.
func (m *Model) SetStyles(theme styles.Theme) {
	m.theme = theme
	m.panel.SetStyles(theme.Panel)
	m.search.SetStyles(resumeSearchStyles(theme))
	m.list.SetDelegate(newConversationDelegate())
	m.resizeChildren()
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(size.Width, size.Height)
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if m.operation == OperationOpen && m.state != StateReady && key.String() != "esc" && key.String() != "enter" {
			return m, nil
		}
		switch key.String() {
		case "esc":
			if m.state == StateReady && m.search.Value() != "" {
				m.search.SetValue("")
				m.applySearch()
				return m, nil
			}
			return m, func() tea.Msg { return CancelledMsg{} }
		case "enter":
			if m.state == StateError {
				return m, func() tea.Msg { return RetryMsg{} }
			}
			if m.state == StateReady {
				if item, ok := m.list.SelectedItem().(conversationItem); ok {
					return m, func() tea.Msg { return SelectedMsg{Conversation: item.summary} }
				}
			}
			return m, nil
		case "up":
			if m.state == StateReady {
				if len(m.list.VisibleItems()) > 0 && m.list.Index() == 0 {
					m.list.GoToEnd()
				} else {
					m.list.CursorUp()
				}
				m.syncWindowToSelection()
			}
			return m, nil
		case "down":
			if m.state == StateReady {
				items := m.list.VisibleItems()
				if len(items) > 0 && m.list.Index() == len(items)-1 {
					m.list.GoToStart()
					m.windowStart = 0
				} else {
					m.list.CursorDown()
				}
				m.syncWindowToSelection()
			}
			return m, nil
		case "left", "right", "pgup", "pgdown":
			if m.state == StateReady {
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(msg)
				m.alignWindowToPage()
				return m, cmd
			}
			return m, nil
		}

		before := m.search.Value()
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		if m.search.Value() != before {
			m.applySearch()
		}
		return m, cmd
	}

	var searchCmd, listCmd tea.Cmd
	before := m.search.Value()
	m.search, searchCmd = m.search.Update(msg)
	if m.search.Value() != before {
		m.applySearch()
	}
	if m.state == StateReady {
		m.list, listCmd = m.list.Update(msg)
	}
	return m, tea.Batch(searchCmd, listCmd)
}

func (m Model) View() string {
	content := components.PanelContent{
		Title:          m.title(),
		Dismiss:        "ESC x",
		Body:           m.panelBody,
		CompactTitle:   "Resume a session",
		CompactMessage: m.compactMessage(),
		TinyMessage:    "Resize terminal to resume",
	}
	if m.state == StateLoading {
		content.CompactMessage = m.loadingLabel()
	}
	switch {
	case m.state == StateLoading && len(m.list.Items()) > 0:
		content.FooterLeft = m.loadingLabel()
	case m.state == StateError && len(m.list.Items()) > 0:
		content.FooterRight = "enter retry"
		bodyWidth, _ := m.panel.BodySize(m.width, m.height, true)
		message := ansi.Truncate(m.compactMessage(), max(1, bodyWidth-len(content.FooterRight)-1), "…")
		content.FooterLeft = m.theme.Feedback.Error.Render(message)
	case m.state == StateReady && len(m.list.VisibleItems()) > 0:
		content.FooterLeft = m.overflowHint()
	}
	return m.panel.View(m.width, m.height, content)
}

func (m Model) title() string {
	if len(m.list.Items()) == 0 {
		return "Resume a session"
	}
	total := len(m.list.VisibleItems())
	selected := 0
	if total > 0 {
		selected = min(m.list.Index()+1, total)
	}
	return fmt.Sprintf("Resume a session (%d of %d)", selected, total)
}

func (m Model) compactMessage() string {
	switch m.state {
	case StateLoading:
		if m.operation == OperationOpen {
			return "Loading conversation…"
		}
		return "Loading conversations…"
	case StateEmpty:
		return "No conversations found."
	case StateError:
		if m.errorMessage != "" {
			return m.errorMessage
		}
		if m.operation == OperationOpen {
			return "Could not open conversation."
		}
		return "Could not load conversations."
	default:
		return "Resize terminal to choose a conversation"
	}
}

// loadingLabel shares the compact layout's copy and the theme's activity glyph.
func (m Model) loadingLabel() string {
	glyph := m.theme.Chat.StatusSpinner.Frame(m.frame)
	if glyph == "" {
		glyph = "·"
	}
	return m.theme.Feedback.Progress.Render(glyph + " " + m.compactMessage())
}

func (m Model) panelBody(width, height int) string {
	m.resizeBody(width, height)
	// Opening keeps the exact list, query, and selection in place. Only the
	// footer changes; a failed open can retry without reconstructing the list.
	if m.operation == OperationOpen && len(m.list.Items()) > 0 {
		return m.searchView(width) + "\n\n" + m.conversationListView(width)
	}
	var body string
	switch m.state {
	case StateLoading:
		detail := "Fetching your recent sessions"
		if m.operation == OperationOpen {
			detail = "Restoring your conversation"
		}
		body = m.loadingLabel() + "\n" + m.theme.Text.Tertiary.Render(detail)
		if m.operation == OperationOpen {
			return lipgloss.Place(width, min(5, max(2, height)), lipgloss.Center, lipgloss.Center, body)
		}
		return m.searchView(width) + "\n\n" + lipgloss.Place(width, min(3, max(2, height-2)), lipgloss.Center, lipgloss.Center, body)
	case StateEmpty:
		body = joinWarning(m.theme.Text.Secondary.Render("No conversations found."), m.warning)
	case StateError:
		body = m.theme.Feedback.Error.Render(m.compactMessage()) + "\n\n" + m.theme.Text.Secondary.Render("enter retry")
	case StateReady:
		body = m.conversationListView(width)
		if len(m.list.VisibleItems()) == 0 && m.search.Value() != "" {
			body = m.theme.Text.Secondary.Render("No conversations match your search.")
		}
		if m.warning != "" {
			body = m.theme.Text.Secondary.Render(m.warning) + "\n" + body
		}
	default:
		body = ""
	}
	return m.searchView(width) + "\n\n" + body
}

func (m Model) searchView(width int) string {
	if width < 1 {
		return ""
	}
	return lipgloss.NewStyle().PaddingRight(1).Width(width).Render(m.search.View())
}

func (m Model) conversationListView(width int) string {
	items, start, _ := m.visibleWindow()
	if len(items) == 0 || width < 1 {
		return ""
	}
	choices := make([]components.Choice, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(conversationItem)
		if !ok {
			continue
		}
		choices = append(choices, components.Choice{Label: item.Title(), Detail: item.Description()})
	}
	selectorStyles := m.theme.Selector
	selectorStyles.Item = m.theme.Text.Secondary
	selectorStyles.Detail = m.theme.Text.Tertiary
	selectorStyles.SelectedDetail = selectorStyles.Selected
	selector := components.NewSelector(choices, selectorStyles)
	selector.SetAlignDetailRight(true)
	selector.SetIndex(m.list.Index() - start)
	return selector.View(width)
}

func (m Model) overflowHint() string {
	items := len(m.list.VisibleItems())
	if items == 0 {
		return ""
	}
	_, _, end := m.visibleWindow()
	hiddenBelow := items - end
	selected := max(0, min(m.list.Index(), items-1))
	if hiddenBelow == 0 && selected == items-1 {
		return "↓ back to top"
	}
	if hiddenBelow == 0 {
		hiddenBelow = items - selected - 1
	}
	return fmt.Sprintf("↓ %d more below", hiddenBelow)
}

func (m Model) visibleWindow() ([]list.Item, int, int) {
	items := m.list.VisibleItems()
	if len(items) == 0 {
		return nil, 0, 0
	}
	rows := min(max(1, m.list.Height()), len(items))
	start := max(0, min(m.windowStart, len(items)-1))
	end := min(len(items), start+rows)
	return items[start:end], start, end
}

func (m *Model) syncWindowToSelection() {
	items := len(m.list.VisibleItems())
	if items == 0 {
		m.windowStart = 0
		return
	}
	rows := min(max(1, m.list.Height()), items)
	selected := max(0, min(m.list.Index(), items-1))
	start := max(0, min(m.windowStart, items-1))
	if selected < start {
		start = selected
	} else if selected >= start+rows {
		start = selected - rows + 1
	}
	m.windowStart = max(0, min(start, items-1))
}

func (m *Model) fillVisibleWindow() {
	items := len(m.list.VisibleItems())
	if items == 0 {
		m.windowStart = 0
		return
	}
	rows := min(max(1, m.list.Height()), items)
	m.windowStart = max(0, min(m.windowStart, items-rows))
}

func (m *Model) alignWindowToPage() {
	items := len(m.list.VisibleItems())
	if items == 0 {
		m.windowStart = 0
		return
	}
	m.windowStart = m.list.Paginator.Page * m.list.Paginator.PerPage
	m.syncWindowToSelection()
}

func (m *Model) applySearch() {
	if m.search.Value() == "" {
		m.list.SetStatusBarItemName("conversation", "conversations")
		m.list.ResetFilter()
		m.list.ResetSelected()
		m.windowStart = 0
		m.updateHelp()
		return
	}
	m.list.SetStatusBarItemName("conversation matches your search", "conversations match your search")
	m.list.SetFilterText(m.search.Value())
	m.windowStart = 0
	m.updateHelp()
}

func (m *Model) resizeChildren() {
	m.resizeBody(m.panel.BodySize(m.width, m.height, true))
	m.updateHelp()
}

func (m *Model) resizeBody(width, height int) {
	// The transparent search row has one right padding cell. textinput
	// renders its prompt outside the configured text width, so reserve the
	// prompt and cursor as well.
	const searchPaddingWidth = 1
	searchWidth := max(0, width-searchPaddingWidth-lipgloss.Width(m.search.Prompt)-1)
	m.search.SetWidth(searchWidth)
	// The search row and the blank row below it sit above the results.
	availableHeight := height - 2
	if m.warning != "" && m.state == StateReady {
		availableHeight--
	}
	listHeight := max(1, min(maxVisibleConversationRows, availableHeight))
	previousListHeight := m.list.Height()
	m.list.SetSize(width, listHeight)
	m.syncWindowToSelection()
	if listHeight > previousListHeight {
		m.fillVisibleWindow()
	}
}

func joinWarning(body, warning string) string {
	if warning == "" {
		return body
	}
	return warning + "\n\n" + body
}

// SafeTitle returns a display-safe title for the conversation summary.
func SafeTitle(summary assistant.ConversationSummary) string {
	if title := escape.SingleLine(summary.Title); title != "" {
		return title
	}
	return "Untitled conversation"
}
