// Package login implements the startup-only Datadog OAuth site picker.
package login

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/auth"
)

const (
	panelMaxWidth   = 112
	completionPause = 650 * time.Millisecond
	spinnerInterval = 90 * time.Millisecond
)

// ErrCanceled means the required startup login was canceled by the user.
var ErrCanceled = errors.New("login canceled")

// BrowserStatus reports where the OAuth flow opened and whether launching the
// browser failed. The authorization URL remains usable as a manual fallback.
type BrowserStatus struct {
	AuthorizationURL string
	OpenError        error
}

// LoginFunc performs and persists OAuth for the selected Datadog site.
type LoginFunc func(context.Context, string, func(BrowserStatus)) error

type phase uint8

const (
	phaseSelect phase = iota
	phaseCustom
	phaseWaiting
	phaseError
	phaseComplete
)

type siteOption struct {
	name   string
	domain string
}

var siteOptions = []siteOption{
	{name: "US1", domain: "app.datadoghq.com"},
	{name: "US3", domain: "us3.datadoghq.com"},
	{name: "US5", domain: "us5.datadoghq.com"},
	{name: "EU1", domain: "app.datadoghq.eu"},
	{name: "AP1", domain: "ap1.datadoghq.com"},
	{name: "AP2", domain: "ap2.datadoghq.com"},
}

const customOptionIndex = 6

type loginFinishedMsg struct {
	attempt uint64
	err     error
}

type browserStatusMsg struct {
	attempt uint64
	status  BrowserStatus
}

type spinnerTickMsg struct{ attempt uint64 }
type completionPauseMsg struct{ attempt uint64 }

// Model is the startup login state machine.
type Model struct {
	ctx      context.Context
	login    LoginFunc
	clientID string

	phase    phase
	selected int
	custom   textinput.Model
	width    int
	height   int
	dark     bool

	attempt          uint64
	cancel           context.CancelFunc
	activeSite       string
	authorizationURL string
	browserOpenErr   error
	loginErr         error
	spinner          int
	completed        bool
	canceled         bool
}

// New creates a startup login model. login must persist a successful session
// before it returns nil. clientID enables validation of explicitly configured
// environments such as GovCloud.
func New(ctx context.Context, login LoginFunc, clientID string) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "acme.us3.datadoghq.com"
	input.CharLimit = 253
	input.SetWidth(48)

	input.SetStyles(textinput.DefaultDarkStyles())
	return &Model{ctx: ctx, login: login, clientID: clientID, custom: input, dark: true}
}

// Completed reports whether OAuth completed successfully.
func (m *Model) Completed() bool { return m.completed }

// Canceled reports whether the user exited before authenticating.
func (m *Model) Canceled() bool { return m.canceled }

// Init requests terminal colors before drawing the picker.
func (m *Model) Init() tea.Cmd {
	return func() tea.Msg { return tea.RequestBackgroundColor() }
}

// Update advances site selection, custom input, and the asynchronous OAuth flow.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.custom.SetWidth(max(12, min(48, msg.Width-10)))
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.custom.SetStyles(textinput.DefaultStyles(m.dark))
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if m.phase == phaseCustom && key != "ctrl+c" && key != "enter" && key != "esc" {
			var cmd tea.Cmd
			m.custom, cmd = m.custom.Update(msg)
			return m, cmd
		}
		return m.handleKey(key)
	case browserStatusMsg:
		if msg.attempt == m.attempt && m.phase == phaseWaiting {
			m.authorizationURL = msg.status.AuthorizationURL
			m.browserOpenErr = msg.status.OpenError
		}
		return m, nil
	case loginFinishedMsg:
		if msg.attempt != m.attempt || m.phase != phaseWaiting {
			return m, nil
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if msg.err != nil {
			m.loginErr = msg.err
			m.phase = phaseError
			return m, nil
		}
		m.completed = true
		m.phase = phaseComplete
		attempt := m.attempt
		return m, tea.Tick(completionPause, func(time.Time) tea.Msg {
			return completionPauseMsg{attempt: attempt}
		})
	case spinnerTickMsg:
		if msg.attempt == m.attempt && m.phase == phaseWaiting {
			m.spinner++
			return m, spinnerTick(m.attempt)
		}
	case completionPauseMsg:
		if msg.attempt == m.attempt && m.phase == phaseComplete {
			return m, tea.Quit
		}
	}

	if m.phase == phaseCustom {
		var cmd tea.Cmd
		m.custom, cmd = m.custom.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		m.abort()
		m.canceled = true
		return m, tea.Quit
	}

	switch m.phase {
	case phaseSelect:
		switch key {
		case "up", "k", "ctrl+p":
			m.selected = (m.selected - 1 + len(siteOptions) + 1) % (len(siteOptions) + 1)
		case "down", "j", "ctrl+n":
			m.selected = (m.selected + 1) % (len(siteOptions) + 1)
		case "enter":
			if m.selected == customOptionIndex {
				m.phase = phaseCustom
				m.loginErr = nil
				return m, m.custom.Focus()
			}
			return m, m.startLogin("https://" + siteOptions[m.selected].domain)
		case "esc":
			m.canceled = true
			return m, tea.Quit
		}
	case phaseCustom:
		switch key {
		case "enter":
			site, err := normalizeCustomSite(m.custom.Value(), m.clientID)
			if err != nil {
				m.loginErr = err
				return m, nil
			}
			m.custom.Blur()
			return m, m.startLogin(site)
		case "esc":
			m.custom.Blur()
			m.phase = phaseSelect
			m.loginErr = nil
			return m, nil
		}
	case phaseWaiting:
		if key == "esc" {
			m.abort()
			m.phase = phaseSelect
			m.loginErr = nil
			return m, nil
		}
	case phaseError:
		switch key {
		case "enter", "r":
			return m, m.startLogin(m.activeSite)
		case "esc":
			m.phase = phaseSelect
			m.loginErr = nil
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) startLogin(site string) tea.Cmd {
	m.abort()
	m.attempt++
	attempt := m.attempt
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.activeSite = site
	m.authorizationURL = ""
	m.browserOpenErr = nil
	m.loginErr = nil
	m.phase = phaseWaiting
	m.spinner = 0

	login := m.login
	status := make(chan BrowserStatus, 1)
	report := func(update BrowserStatus) {
		select {
		case status <- update:
		default:
		}
	}
	return tea.Batch(
		func() tea.Msg {
			if login == nil {
				return loginFinishedMsg{attempt: attempt, err: errors.New("OAuth login is unavailable")}
			}
			return loginFinishedMsg{attempt: attempt, err: login(ctx, site, report)}
		},
		func() tea.Msg {
			select {
			case update := <-status:
				return browserStatusMsg{attempt: attempt, status: update}
			case <-ctx.Done():
				return browserStatusMsg{attempt: attempt}
			}
		},
		spinnerTick(attempt),
	)
}

func (m *Model) abort() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.attempt++ // make any result already in flight stale
}

func spinnerTick(attempt uint64) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{attempt: attempt}
	})
}

func normalizeCustomSite(raw, clientID string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter your Datadog site")
	}
	cfg, err := auth.ConfigForSite(raw, clientID)
	if err != nil {
		return "", fmt.Errorf("enter a Datadog site, such as acme.us3.datadoghq.com: %w", err)
	}
	return cfg.Site, nil
}

// View renders the login as a centered modal-like panel, following the same
// compact list treatment as the rest of the Bits TUI.
func (m *Model) View() tea.View {
	view := tea.View{AltScreen: true}
	if m.width <= 0 || m.height <= 0 {
		view.Content = "Loading…"
		return view
	}

	panel := m.panelView()
	view.Content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
	return view
}

func (m *Model) panelView() string {
	p := paletteFor(m.dark)
	if m.width < 12 {
		return m.compactView(p)
	}

	outerWidth := min(panelMaxWidth, m.width-4)
	contentWidth := max(1, outerWidth-p.panel.GetHorizontalFrameSize())
	heading := p.heading.Render(m.title())
	closeHint := p.close.Render("esc ×")
	gap := max(1, contentWidth-lipgloss.Width(heading)-lipgloss.Width(closeHint))
	header := lipgloss.JoinHorizontal(lipgloss.Top, heading, strings.Repeat(" ", gap), closeHint)
	body := m.bodyView(contentWidth, p)
	content := lipgloss.JoinVertical(lipgloss.Left, header, "", body)
	panel := p.panel.Width(outerWidth).Render(content)
	if lipgloss.Width(panel) <= m.width && lipgloss.Height(panel) <= m.height {
		return panel
	}
	return m.compactView(p)
}

func (m *Model) compactView(p loginPalette) string {
	if m.height >= 4 && m.width >= 20 {
		compact := p.compact.Width(min(m.width, 34)).Render("Sign in to Bits\n\nResize the terminal to continue.")
		if lipgloss.Width(compact) <= m.width && lipgloss.Height(compact) <= m.height {
			return compact
		}
	}
	return ansi.Truncate("Resize terminal to sign in", max(1, m.width), "")
}

func (m *Model) title() string {
	switch m.phase {
	case phaseWaiting:
		return "Sign in to Datadog"
	case phaseError:
		return "Login failed"
	case phaseComplete:
		return "Signed in"
	case phaseCustom:
		return "Enter your Datadog domain"
	default:
		return "Choose your Datadog site"
	}
}

func (m *Model) bodyView(width int, p loginPalette) string {
	switch m.phase {
	case phaseCustom:
		parts := []string{
			p.description.Render("Enter the hostname where your organization lives."),
			"",
			p.input.Width(width).Render(m.custom.View()),
		}
		if m.loginErr != nil {
			parts = append(parts, "", p.error.Width(width).Render(m.loginErr.Error()))
		}
		parts = append(parts, "", footer(width, p.help.Render("esc back"), p.help.Render("enter to continue")))
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	case phaseWaiting:
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		parts := []string{
			p.description.Render("Complete sign-in in the browser window."),
			"",
			p.waiting.Render(frames[m.spinner%len(frames)] + "  Waiting for Datadog"),
			p.domain.Render(strings.TrimPrefix(m.activeSite, "https://")),
		}
		if m.browserOpenErr != nil {
			parts[0] = p.error.Render("We couldn't open a browser: " + m.browserOpenErr.Error())
			if m.authorizationURL != "" {
				parts = append(parts, "", p.description.Render("Open this URL:"), p.domain.Render(ansi.Hardwrap(m.authorizationURL, width, false)))
			}
		}
		parts = append(parts, "", p.help.Render("esc choose another site"))
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	case phaseError:
		message := "Login did not complete."
		if m.loginErr != nil {
			message = m.loginErr.Error()
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			p.error.Width(width).Render(message),
			p.domain.Render(strings.TrimPrefix(m.activeSite, "https://")),
			"",
			footer(width, p.help.Render("esc choose another site"), p.help.Render("enter to retry")),
		)
	case phaseComplete:
		return lipgloss.JoinVertical(lipgloss.Left,
			p.success.Render("✓  Authentication complete"),
			p.domain.Render(strings.TrimPrefix(m.activeSite, "https://")),
		)
	default:
		rows := make([]string, 0, len(siteOptions)+1)
		for i, option := range siteOptions {
			rows = append(rows, siteRow(option.name, option.domain, i == m.selected, p))
		}
		rows = append(rows, siteRow("Custom", "Enter another domain", m.selected == customOptionIndex, p))
		return lipgloss.JoinVertical(lipgloss.Left,
			p.description.Render("Select the site where your organization lives."),
			"",
			lipgloss.JoinVertical(lipgloss.Left, rows...),
			"",
			footer(width, p.help.Render("↑/↓ navigate"), p.help.Render("enter to continue")),
		)
	}
}

func siteRow(name, detail string, selected bool, p loginPalette) string {
	marker := "  "
	nameStyle := p.row
	detailStyle := p.domain
	if selected {
		marker = "› "
		nameStyle = p.selected
		detailStyle = p.row
	}
	return nameStyle.Render(marker+fmt.Sprintf("%-7s", name)) + detailStyle.Render(detail)
}

func footer(width int, left, right string) string {
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
}

type loginPalette struct {
	panel       lipgloss.Style
	compact     lipgloss.Style
	heading     lipgloss.Style
	close       lipgloss.Style
	description lipgloss.Style
	row         lipgloss.Style
	selected    lipgloss.Style
	domain      lipgloss.Style
	help        lipgloss.Style
	input       lipgloss.Style
	waiting     lipgloss.Style
	error       lipgloss.Style
	success     lipgloss.Style
}

func paletteFor(dark bool) loginPalette {
	text, muted, border, accent, danger, success := "#C9CBD1", "#6F727C", "#474A54", "#5E6DD6", "#C85A68", "#65A875"
	if !dark {
		text, muted, border, accent, danger, success = "#29333A", "#737A80", "#B8BCC4", "#4D58AF", "#B23A4A", "#397A4A"
	}

	return loginPalette{
		panel: lipgloss.NewStyle().
			Foreground(lipgloss.Color(text)).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(border)).
			Padding(1, 2),
		compact:     lipgloss.NewStyle().Foreground(lipgloss.Color(text)),
		heading:     lipgloss.NewStyle().Foreground(lipgloss.Color(text)),
		close:       lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(muted)),
		description: lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		row:         lipgloss.NewStyle().Foreground(lipgloss.Color(text)),
		selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(accent)),
		domain:      lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(muted)),
		help:        lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(muted)),
		input:       lipgloss.NewStyle().Foreground(lipgloss.Color(text)),
		waiting:     lipgloss.NewStyle().Foreground(lipgloss.Color(accent)),
		error:       lipgloss.NewStyle().Foreground(lipgloss.Color(danger)),
		success:     lipgloss.NewStyle().Foreground(lipgloss.Color(success)),
	}
}
