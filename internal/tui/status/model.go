package status

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Connectivity is observed Assistant transport state. The separate current-user
// lookup never changes this value.
type Connectivity string

const (
	ConnectivityNotChecked Connectivity = "not checked"
	ConnectivityConnecting Connectivity = "connecting"
	ConnectivityConnected  Connectivity = "connected"
	ConnectivityOffline    Connectivity = "offline"
	ConnectivityError      Connectivity = "service error"
)

// Runtime is the non-secret live state owned by the root TUI model.
type Runtime struct {
	Site                string
	AuthenticationMode  string
	AuthenticationState string
	Identity            string
	ConversationID      string
	Profile             string
	Model               string
	ApprovalMode        string
	Phase               string
	Connectivity        Connectivity
	Usage               *assistant.Usage
}

// ClosedMsg asks the root TUI to return to chat.
type ClosedMsg struct{}

// Model renders a responsive shared panel whose document body scrolls within
// the available terminal height.
type Model struct {
	viewport         viewport.Model
	panel            *components.Panel
	theme            styles.Theme
	runtime          Runtime
	environment      Environment
	environmentReady bool
	width            int
	height           int
	bodyWidth        int
}

// New creates a status model using the same theme and panel component as the
// conversation picker.
func New(width, height int, themes ...styles.Theme) Model {
	theme := styles.Default(true)
	if len(themes) > 0 {
		theme = themes[0]
	}
	vp := viewport.New()
	vp.SoftWrap = true
	vp.FillHeight = true
	vp.MouseWheelEnabled = true
	m := Model{
		viewport: vp,
		panel:    components.NewPanel(theme.Panel),
		theme:    theme,
		width:    max(1, width),
		height:   max(1, height),
	}
	m.resizeBody(m.panelBodyWidth())
	m.rebuild()
	return m
}

// Open resets scroll position and starts a new fresh environment collection.
func (m *Model) Open(runtime Runtime) {
	m.runtime = runtime
	m.environment = Environment{}
	m.environmentReady = false
	m.rebuild()
	m.viewport.GotoTop()
}

// SetRuntime refreshes fields that can change while the status screen is open.
func (m *Model) SetRuntime(runtime Runtime) {
	m.runtime = runtime
	m.rebuild()
}

// SetEnvironment installs the completed local snapshot.
func (m *Model) SetEnvironment(environment Environment) {
	m.environment = environment
	m.environmentReady = true
	m.rebuild()
}

// SetSize updates the full-screen bounds.
func (m *Model) SetSize(width, height int) {
	m.width = max(1, width)
	m.height = max(1, height)
	m.resizeBody(m.panelBodyWidth())
}

// SetStyles applies a complete theme change to the panel and document.
func (m *Model) SetStyles(theme styles.Theme) {
	m.theme = theme
	m.panel.SetStyles(theme.Panel)
	m.resizeBody(m.panelBodyWidth())
	m.rebuild()
}

// Update routes document navigation and closes on Escape.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "esc" {
		return m, func() tea.Msg { return ClosedMsg{} }
	}
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(msg)
	return m, command
}

// View renders the shared bordered panel with compact fallbacks at small sizes.
func (m *Model) View() string {
	content := components.PanelContent{
		Title:          "Bits status",
		Dismiss:        "esc ×",
		Body:           m.panelBody,
		FooterLeft:     "↑/↓ scroll   pgup/pgdown page",
		CompactTitle:   "Bits status",
		CompactMessage: "Resize the terminal to view session status.",
		TinyMessage:    "Resize to view status",
	}
	return m.panel.View(m.width, m.height, content)
}

func (m *Model) panelBody(width int) string {
	m.resizeBody(width)
	return m.viewport.View()
}

func (m *Model) resizeBody(width int) {
	// Panel overhead is its frame plus one header, one footer, and the two
	// section gaps between them and the body.
	gap := max(1, m.theme.Panel.SectionGap+1)
	height := m.height - m.theme.Panel.Frame.GetVerticalFrameSize() - 2 - 2*gap
	width = max(1, width)
	widthChanged := width != m.bodyWidth
	m.bodyWidth = width
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, height))
	if widthChanged {
		m.rebuild()
	}
}

func (m Model) panelBodyWidth() int {
	available := m.width - 2*max(0, m.theme.Panel.HorizontalMargin)
	if m.theme.Panel.MaxWidth > 0 {
		available = min(available, m.theme.Panel.MaxWidth)
	}
	return max(1, available-m.theme.Panel.Frame.GetHorizontalFrameSize())
}

func (m *Model) rebuild() {
	model := safeDisplay(m.runtime.Model)
	if model == "" {
		// TODO(BCLI-36): Replace this fallback with the backend-selected model
		// once /api/v2/assistant exposes it in the public response contract.
		model = "default"
	}
	datadog := []statusRow{
		{label: "Site", value: available(m.runtime.Site, "no remote Datadog site is configured")},
		{label: "Authentication", value: authentication(m.runtime)},
		{label: "User / org", value: available(m.runtime.Identity, "authenticated identity is not exposed by this client")},
	}
	assistantRows := []statusRow{
		{label: "Conversation", value: available(m.runtime.ConversationID, "no conversation has been created")},
		{label: "Profile", value: available(m.runtime.Profile, "assistant profile is not known")},
		{label: "Model", value: model},
		{label: "Approval", value: available(m.runtime.ApprovalMode, "approval mode is not known")},
		{label: "Turn", value: available(m.runtime.Phase, "turn state is not known")},
		{label: "Connectivity", value: available(string(m.runtime.Connectivity), "no backend activity has been observed")},
		{label: "Context tokens", value: usage(m.runtime.Usage)},
	}
	workspace := m.workspaceRows()
	content := strings.Join([]string{
		m.renderSection("Datadog", datadog),
		m.renderSection("Assistant", assistantRows),
		m.renderSection("Workspace", workspace),
	}, "\n\n")
	m.viewport.SetContent(content)
}

type statusRow struct {
	label string
	value string
}

func (m Model) renderSection(title string, rows []statusRow) string {
	lines := []string{m.theme.Text.Body.Bold(true).Render(title)}
	for _, row := range rows {
		const labelWidth = 18
		label := m.theme.Text.Muted.Render(fmt.Sprintf("  %-16s", row.label))
		value := safeDisplay(row.value)
		style := m.theme.Text.Body
		if strings.HasPrefix(value, "unavailable") || value == "not applicable" || value == "not a Git repository" || value == "collecting…" {
			style = m.theme.Text.Muted
		}
		wrapped := strings.Split(ansi.Wordwrap(value, max(1, m.bodyWidth-labelWidth), "-"), "\n")
		lines = append(lines, label+style.Render(wrapped[0]))
		for _, continuation := range wrapped[1:] {
			lines = append(lines, strings.Repeat(" ", labelWidth)+style.Render(continuation))
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) workspaceRows() []statusRow {
	if !m.environmentReady {
		return []statusRow{
			{label: "Directory", value: "collecting…"},
			{label: "Repository", value: "collecting…"},
			{label: "Branch", value: "collecting…"},
			{label: "Commit", value: "collecting…"},
			{label: "Working tree", value: "collecting…"},
		}
	}
	directory := available(m.environment.WorkingDirectory, "working directory could not be read")
	repository := m.environment.Repository
	switch repository.State {
	case RepositoryAbsent:
		return []statusRow{
			{label: "Directory", value: directory},
			{label: "Repository", value: "not a Git repository"},
			{label: "Branch", value: "not applicable"},
			{label: "Commit", value: "not applicable"},
			{label: "Working tree", value: "not applicable"},
		}
	case RepositoryPresent:
		branch := repository.Branch
		if repository.Detached {
			branch = "detached HEAD"
		} else if branch == "" {
			branch = "unavailable — branch could not be determined"
		}
		commit := repository.Commit
		if repository.Unborn {
			commit = "unavailable — repository has no commits"
		} else if commit == "" {
			commit = "unavailable — HEAD commit could not be determined"
		}
		return []statusRow{
			{label: "Directory", value: directory},
			{label: "Repository", value: repositoryLabel(repository)},
			{label: "Branch", value: branch},
			{label: "Commit", value: commit},
			{label: "Working tree", value: changesLabel(repository.Changes)},
		}
	default:
		return []statusRow{
			{label: "Directory", value: directory},
			{label: "Repository", value: "unavailable — Git context could not be collected"},
			{label: "Branch", value: "unavailable — Git context could not be collected"},
			{label: "Commit", value: "unavailable — Git context could not be collected"},
			{label: "Working tree", value: "unavailable — Git context could not be collected"},
		}
	}
}

func authentication(runtime Runtime) string {
	mode := safeDisplay(runtime.AuthenticationMode)
	state := safeDisplay(runtime.AuthenticationState)
	switch {
	case mode != "" && state != "":
		return mode + " · " + state
	case mode != "":
		return mode + " · unavailable — authentication state is not known"
	case state != "":
		return "unavailable — authentication mode is not known · " + state
	default:
		return "unavailable — authentication mode and state are not known"
	}
}

func available(value, reason string) string {
	if value = safeDisplay(value); value != "" {
		return value
	}
	return "unavailable — " + reason
}

func usage(value *assistant.Usage) string {
	if value == nil {
		return "unavailable — no usage has been reported"
	}
	used := formatInteger(value.TokensUsed)
	if value.MaxTokens <= 0 {
		return used
	}
	return used + " / " + formatInteger(value.MaxTokens)
}

func repositoryLabel(repository Repository) string {
	name := available(repository.Name, "repository name could not be determined")
	root := safeDisplay(repository.Root)
	if root == "" {
		return name
	}
	return name + " · " + root
}

func changesLabel(changes Changes) string {
	if !changes.Known {
		return "unavailable — working-tree state could not be collected"
	}
	var states []string
	if changes.Staged {
		states = append(states, "staged")
	}
	if changes.Unstaged {
		states = append(states, "unstaged")
	}
	if changes.Untracked {
		states = append(states, "untracked")
	}
	if changes.Conflicted {
		states = append(states, "conflicts")
	}
	if len(states) == 0 {
		return "clean"
	}
	return strings.Join(states, ", ")
}

func formatInteger(value int) string {
	text := strconv.Itoa(value)
	start := 0
	if strings.HasPrefix(text, "-") {
		start = 1
	}
	for i := len(text) - 3; i > start; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

// safeDisplay strips terminal control sequences and collapses control
// characters so backend IDs and unusual local paths cannot inject terminal UI.
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
