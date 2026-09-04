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
	StateClosing
)

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

func (i conversationItem) Title() string { return safeTitle(i.summary) }
func (i conversationItem) Description() string {
	return relativeUpdatedAt(i.summary.UpdatedAt, i.now())
}
func (i conversationItem) FilterValue() string { return safeTitle(i.summary) }

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
	now          func() time.Time
}

func New(width, height int, themes ...styles.Theme) Model {
	theme := styles.Default(true)
	if len(themes) > 0 {
		theme = themes[0]
	}
	delegate := newConversationDelegate(theme)
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
	configureConversationHelp(&model)
	search := textinput.New()
	search.Prompt = " >  "
	search.Placeholder = "Type to search"
	search.CharLimit = 128
	search.Focus()
	m := Model{
		list: model, search: search, panel: components.NewPanel(theme.Panel), theme: theme,
		state: StateLoading, operation: OperationList,
		width: max(width, 1), height: max(height, 1), now: time.Now,
	}
	m.search.SetStyles(theme.TextInput)
	m.resizeChildren()
	return m
}

func (m Model) State() State  { return m.state }
func (m Model) Query() string { return m.search.Value() }

func (m *Model) SetLoading(operation Operation) {
	m.state = StateLoading
	m.operation = operation
	m.err = nil
	m.errorMessage = ""
	m.warning = ""
	m.resizeChildren()
	m.list.StopSpinner()
	_ = m.list.SetItems(nil)
}

// SetClosing makes the picker non-interactive while the root model waits for
// its canceled engine operation to publish a terminal result and release
// ownership.
func (m *Model) SetClosing() {
	m.state = StateClosing
	m.err = nil
	m.errorMessage = ""
	m.warning = ""
	m.list.StopSpinner()
}

// SetConversations drops entries without the canonical conversation_id. The
// API's JSON:API item id is redundant today, but is not the documented request
// key and must not silently substitute for malformed data.
func (m *Model) SetConversations(summaries []assistant.ConversationSummary) tea.Cmd {
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
	items := make([]list.Item, len(ordered))
	for i := range ordered {
		items[i] = conversationItem{summary: ordered[i], now: m.now}
	}
	m.err = nil
	if len(items) == 0 {
		m.state = StateEmpty
	} else {
		m.state = StateReady
	}
	m.list.ResetFilter()
	m.list.ResetSelected()
	cmd := m.list.SetItems(items)
	m.applySearch()
	return cmd
}

func newConversationDelegate(themes ...styles.Theme) list.DefaultDelegate {
	theme := styles.Default(true)
	if len(themes) > 0 {
		theme = themes[0]
	}
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(theme.Selector.Item.GetForeground()).Bold(true)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(theme.Selector.Selected.GetForeground()).Bold(true)
	// Keep the timestamp subordinate to the title, including on the selected
	// row where the default delegate otherwise gives both lines equal emphasis.
	muted := theme.Selector.Detail.GetForeground()
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.Foreground(muted).Bold(false).Faint(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(muted).Bold(false).Faint(true)
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.Foreground(muted).Bold(false).Faint(true)
	return delegate
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

func relativeUpdatedAt(updatedAt int64, now time.Time) string {
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
	m.search.SetStyles(theme.TextInput)
	m.list.SetDelegate(newConversationDelegate(theme))
	m.resizeChildren()
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(size.Width, size.Height)
		return m, nil
	}
	if m.state == StateClosing {
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
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
				m.list.CursorUp()
			}
			return m, nil
		case "down":
			if m.state == StateReady {
				m.list.CursorDown()
			}
			return m, nil
		case "left", "right", "pgup", "pgdown":
			if m.state == StateReady {
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(msg)
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
	dismiss := "esc ×"
	content := components.PanelContent{
		Title:          "Resume a conversation",
		Dismiss:        dismiss,
		Body:           m.panelBody,
		CompactTitle:   "Resume a conversation",
		CompactMessage: m.compactMessage(),
		TinyMessage:    "Resize terminal to resume",
	}
	if m.state == StateReady {
		content.FooterLeft = "↑/↓ navigate"
		if m.list.Paginator.TotalPages > 1 {
			content.FooterLeft += "   ←/→ page"
		}
	}
	return m.panel.View(m.width, m.height, content)
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
		return "Could not load conversations."
	case StateClosing:
		return "Closing…"
	default:
		return "Resize terminal to choose a conversation"
	}
}

func (m Model) panelBody(width int) string {
	m.resizeBody(width)
	search := m.search.View()
	var body string
	switch m.state {
	case StateLoading:
		if m.operation == OperationOpen {
			body = m.theme.Feedback.Progress.Render("Loading conversation…")
		} else {
			body = m.theme.Feedback.Progress.Render("Loading conversations…")
		}
	case StateEmpty:
		body = joinWarning(m.theme.Text.Muted.Render("No conversations found."), m.warning)
	case StateError:
		message := m.errorMessage
		if message == "" {
			if m.operation == OperationOpen {
				message = "Could not open conversation."
			} else {
				message = "Could not load conversations."
			}
		}
		body = m.theme.Feedback.Error.Render(message) + "\n\n" + m.theme.Text.Help.Render("enter retry")
	case StateClosing:
		body = m.theme.Feedback.Progress.Render("Closing…")
	case StateReady:
		body = m.list.View()
		if m.warning != "" {
			body = m.theme.Text.Muted.Render(m.warning) + "\n" + body
		}
	default:
		body = ""
	}
	return search + "\n\n" + body
}

func (m *Model) applySearch() {
	if m.search.Value() == "" {
		m.list.SetStatusBarItemName("conversation", "conversations")
		m.list.ResetFilter()
		m.list.ResetSelected()
		m.updateHelp()
		return
	}
	m.list.SetStatusBarItemName("conversation matches your search", "conversations match your search")
	m.list.SetFilterText(m.search.Value())
	m.updateHelp()
}

func (m *Model) resizeChildren() {
	m.resizeBody(m.panelBodyWidth())
	m.updateHelp()
}

func (m *Model) resizeBody(width int) {
	// textinput renders its prompt outside the configured text width. Reserve
	// both the prompt and cursor so the complete search line cannot wrap.
	searchWidth := max(0, width-lipgloss.Width(m.search.Prompt)-1)
	m.search.SetWidth(searchWidth)
	listHeight := max(1, m.height-m.theme.Panel.Frame.GetVerticalFrameSize()-9)
	if m.warning != "" && m.state == StateReady {
		listHeight = max(1, listHeight-1)
	}
	m.list.SetSize(width, listHeight)
}

func (m Model) panelBodyWidth() int {
	available := m.width - 2*max(0, m.theme.Panel.HorizontalMargin)
	if m.theme.Panel.MaxWidth > 0 {
		available = min(available, m.theme.Panel.MaxWidth)
	}
	return max(1, available-m.theme.Panel.Frame.GetHorizontalFrameSize())
}

func joinWarning(body, warning string) string {
	if warning == "" {
		return body
	}
	return warning + "\n\n" + body
}

func safeTitle(summary assistant.ConversationSummary) string {
	if title := escape.SingleLine(summary.Title); title != "" {
		return title
	}
	return "Untitled conversation"
}
