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
)

type State int

const (
	StateLoading State = iota
	StateReady
	StateEmpty
	StateError
)

type Operation int

const (
	OperationList Operation = iota
	OperationOpen
)

type SelectedMsg struct{ Conversation assistant.ConversationSummary }
type CancelledMsg struct{}
type RetryMsg struct{}

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
	state        State
	operation    Operation
	err          error // retained for diagnostics; never rendered directly
	errorMessage string
	warning      string
	width        int
	height       int
	now          func() time.Time
}

func New(width, height int) Model {
	delegate := newConversationDelegate()
	model := list.New(nil, delegate, max(width, 1), max(height, 1))
	model.SetShowTitle(false)
	model.SetShowFilter(false)
	model.DisableQuitKeybindings()
	// Filtering is driven by the persistent search input below. Disable the
	// list's modal "/ filter" key while continuing to apply queries explicitly
	// through SetFilterText.
	model.SetFilteringEnabled(false)
	model.SetShowHelp(true)
	model.SetShowStatusBar(false)
	configureConversationHelp(&model)
	search := textinput.New()
	search.Prompt = " >  "
	search.Placeholder = "Type to search"
	search.CharLimit = 128
	search.Focus()
	m := Model{
		list: model, search: search, state: StateLoading, operation: OperationList,
		width: max(width, 1), height: max(height, 1), now: time.Now,
	}
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

func newConversationDelegate() list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.Bold(true)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Bold(true)
	// Keep the timestamp subordinate to the title, including on the selected
	// row where the default delegate otherwise gives both lines equal emphasis.
	muted := delegate.Styles.NormalDesc.GetForeground()
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.Bold(false).Faint(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(muted).Bold(false).Faint(true)
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.Bold(false).Faint(true)
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
	m.errorMessage = ansi.Truncate(safeDisplay(message), 240, "…")
}

func (m *Model) SetWarning(message string) {
	m.warning = ansi.Truncate(safeDisplay(message), 120, "…")
	m.resizeChildren()
}

func (m *Model) SetSize(width, height int) {
	m.width = max(width, 1)
	m.height = max(height, 1)
	m.resizeChildren()
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(size.Width, size.Height)
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
	search := m.search.View()
	var body string
	switch m.state {
	case StateLoading:
		if m.operation == OperationOpen {
			body = "Loading conversation…\n\nEsc cancel"
		} else {
			body = "Loading conversations…\n\nEsc cancel"
		}
	case StateEmpty:
		body = joinWarning("No conversations found.\n\nEsc cancel", m.warning)
	case StateError:
		message := m.errorMessage
		if message == "" {
			if m.operation == OperationOpen {
				message = "Could not open conversation."
			} else {
				message = "Could not load conversations."
			}
		}
		body = message + "\n\nEnter retry · Esc cancel"
	case StateReady:
		body = joinWarning(m.list.View(), m.warning)
	default:
		body = ""
	}
	return search + "\n" + body
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
	// textinput renders its prompt outside the configured text width. Reserve
	// both the prompt and cursor so the complete search line cannot wrap.
	searchWidth := max(0, m.width-lipgloss.Width(m.search.Prompt)-1)
	m.search.SetWidth(searchWidth)
	reserved := 1 // persistent search bar
	if m.warning != "" && m.state == StateReady {
		reserved += 2 // warning plus separating blank line
	}
	m.list.SetSize(m.width, max(1, m.height-reserved))
	m.updateHelp()
}

func joinWarning(body, warning string) string {
	if warning == "" {
		return body
	}
	return warning + "\n\n" + body
}

func safeTitle(summary assistant.ConversationSummary) string {
	if title := safeDisplay(summary.Title); title != "" {
		return title
	}
	return "Untitled conversation"
}

func safeDisplay(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cf) {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
