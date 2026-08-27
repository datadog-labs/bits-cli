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
	panelWidth      = 64
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
	if m.width < 36 || m.height < 16 || (m.browserOpenErr != nil && m.height < 24) {
		return p.compact.Width(max(1, min(m.width, 34))).Render("Sign in to Bits\n\nResize the terminal to continue.")
	}

	outerWidth := min(panelWidth, m.width-2)
	contentWidth := max(1, outerWidth-p.panel.GetHorizontalFrameSize())
	title := p.eyebrow.Render("DATADOG") + "\n" + p.title.Render(m.title())
	body := m.bodyView(contentWidth, p)
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body)

	return p.panel.Width(outerWidth).Render(content)
}

func (m *Model) title() string {
	switch m.phase {
	case phaseWaiting:
		return "Sign in with your browser"
	case phaseError:
		return "We couldn't sign you in"
	case phaseComplete:
		return "You're signed in"
	default:
		return "Sign in to Bits"
	}
}

func (m *Model) bodyView(width int, p loginPalette) string {
	switch m.phase {
	case phaseCustom:
		parts := []string{
			p.description.Render("Enter your Datadog hostname."),
			"",
			p.input.Width(width).Render(m.custom.View()),
		}
		if m.loginErr != nil {
			parts = append(parts, "", p.error.Width(width).Render(m.loginErr.Error()))
		}
		parts = append(parts, "", p.help.Render("enter continue   esc back   ctrl+c quit"))
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	case phaseWaiting:
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		parts := []string{
			p.description.Render("Complete sign-in in the browser window we opened."),
			"",
			p.waiting.Render(frames[m.spinner%len(frames)] + "  Waiting for Datadog…"),
			p.domain.Render(strings.TrimPrefix(m.activeSite, "https://")),
		}
		if m.browserOpenErr != nil {
			parts[0] = p.error.Render("We couldn't open a browser: " + m.browserOpenErr.Error())
			if m.authorizationURL != "" {
				parts = append(parts, "", p.description.Render("Open this URL:"), p.domain.Render(ansi.Hardwrap(m.authorizationURL, width, false)))
			}
		}
		parts = append(parts, "", p.help.Render("esc choose another site   ctrl+c quit"))
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	case phaseError:
		message := "Login did not complete."
		if m.loginErr != nil {
			message = m.loginErr.Error()
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			p.error.Width(width).Render(message),
			"",
			p.domain.Render(strings.TrimPrefix(m.activeSite, "https://")),
			"",
			p.help.Render("enter retry   esc choose another site   ctrl+c quit"),
		)
	case phaseComplete:
		return lipgloss.JoinVertical(lipgloss.Left,
			p.success.Render("✓  Datadog authentication complete"),
			"",
			p.description.Render("Opening Bits…"),
		)
	default:
		rows := make([]string, 0, len(siteOptions)+1)
		for i, option := range siteOptions {
			marker := "  "
			style := p.row
			if i == m.selected {
				marker = "› "
				style = p.selected
			}
			label := fmt.Sprintf("%s%-5s  %s", marker, option.name, option.domain)
			rows = append(rows, style.Width(width).Render(label))
		}
		marker := "  "
		style := p.row
		if m.selected == customOptionIndex {
			marker = "› "
			style = p.selected
		}
		rows = append(rows, style.Width(width).Render(marker+"Custom domain…"))
		return lipgloss.JoinVertical(lipgloss.Left,
			p.description.Render("Choose the Datadog site where your organization lives."),
			"",
			lipgloss.JoinVertical(lipgloss.Left, rows...),
			"",
			p.help.Render("↑/↓ navigate   enter continue   esc quit"),
		)
	}
}

type loginPalette struct {
	panel       lipgloss.Style
	compact     lipgloss.Style
	eyebrow     lipgloss.Style
	title       lipgloss.Style
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
	text, muted, surface, inputSurface, border := "#F2F2F2", "#8A8D98", "#181A21", "#11131A", "#4B4E59"
	selectedText, selectedSurface := "#FFFFFF", "#4D58AF"
	if !dark {
		text, muted, surface, inputSurface, border = "#1C2E38", "#66707A", "#FFFFFF", "#EEF0F3", "#B8BCC4"
		selectedText, selectedSurface = "#FFFFFF", "#5E6DD6"
	}

	return loginPalette{
		panel: lipgloss.NewStyle().
			Foreground(lipgloss.Color(text)).
			Background(lipgloss.Color(surface)).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(border)).
			Padding(1, 2),
		compact:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(text)).Background(lipgloss.Color(surface)).Padding(1),
		eyebrow:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8C97FF")).Background(lipgloss.Color(surface)),
		title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(text)).Background(lipgloss.Color(surface)),
		description: lipgloss.NewStyle().Foreground(lipgloss.Color(text)).Background(lipgloss.Color(surface)),
		row:         lipgloss.NewStyle().Foreground(lipgloss.Color(text)).Background(lipgloss.Color(surface)),
		selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(selectedText)).Background(lipgloss.Color(selectedSurface)),
		domain:      lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(surface)),
		help:        lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(surface)),
		input:       lipgloss.NewStyle().Foreground(lipgloss.Color(text)).Background(lipgloss.Color(inputSurface)).Padding(0, 1),
		waiting:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8C97FF")).Background(lipgloss.Color(surface)),
		error:       lipgloss.NewStyle().Foreground(lipgloss.Color("#EB5364")).Background(lipgloss.Color(surface)),
		success:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#41C464")).Background(lipgloss.Color(surface)),
	}
}
