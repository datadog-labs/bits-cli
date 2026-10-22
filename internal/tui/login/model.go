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
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/site"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

const (
	completionPause = 650 * time.Millisecond
	spinnerInterval = 90 * time.Millisecond
	copyFeedback    = 2 * time.Second
)

// ErrCanceled means the required startup login was canceled by the user.
var ErrCanceled = errors.New("login canceled")

// BrowserStatus reports the login URL, browser launch failures, and retryable
// errors from pasted callback URLs.
type BrowserStatus struct {
	AuthorizationURL string
	OpenError        error
	InputError       error
}

// LoginFunc performs and persists OAuth for the selected Datadog site. Status
// reports are synchronous: implementations must not call report after returning.
type LoginFunc func(context.Context, string, <-chan string, func(BrowserStatus)) error

type phase uint8

const (
	phaseSelect phase = iota
	phaseCustom
	phaseWaiting
	phaseError
	phaseComplete
)

var siteOptions = site.LoginRegions()

func customOptionIndex() int { return len(siteOptions) }

type loginFinishedMsg struct {
	attempt uint64
	err     error
}

type browserStatusMsg struct {
	attempt uint64
	status  BrowserStatus
	updates <-chan BrowserStatus
	ctx     context.Context
	done    bool
}

type (
	spinnerTickMsg     struct{ attempt uint64 }
	completionPauseMsg struct{ attempt uint64 }
	linkOpenMsg        struct {
		attempt uint64
		err     error
	}
)

// CompletedMsg tells the owning application that the persisted OAuth session
// is ready. It deliberately does not quit Bubble Tea: the root model can switch
// to chat while the same program keeps control of the alternate screen.
type CompletedMsg struct{}

// Model is the startup login state machine.
type Model struct {
	ctx   context.Context
	login LoginFunc

	phase        phase
	custom       textinput.Model
	callback     textinput.Model
	callbackURLs chan string
	callbackErr  error
	selector     *components.Selector
	panel        *components.Panel
	theme        styles.Theme
	width        int
	height       int

	attempt          uint64
	attemptCtx       context.Context
	cancel           context.CancelFunc
	activeSite       string
	authorizationURL string
	browserOpenErr   error
	linkPressX       int
	linkPressY       int
	linkPressed      bool
	copiedUntil      time.Time
	loginErr         error
	spinner          int
	completed        bool
	canceled         bool
}

// New creates a startup login model. login must persist a successful session
// before it returns nil.
func New(ctx context.Context, login LoginFunc) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	theme := styles.Default(true)
	input := textinput.New()
	input.Prompt = theme.Input.Prompt
	input.Placeholder = "acme.us3.datadoghq.com"
	input.CharLimit = 253
	input.SetWidth(48)
	input.SetStyles(theme.TextInput)
	callback := textinput.New()
	callback.Prompt = theme.Input.Prompt
	callback.Placeholder = "Paste the full address, then press Enter"
	callback.SetWidth(48)
	callback.SetStyles(theme.TextInput)

	choices := make([]components.Choice, 0, len(siteOptions)+1)
	for _, option := range siteOptions {
		choices = append(choices, components.Choice{Label: option.Name, Detail: option.WebHost})
	}
	choices = append(choices, components.Choice{Label: "Custom", Detail: "Enter another domain"})
	return &Model{
		ctx:      ctx,
		login:    login,
		custom:   input,
		callback: callback,
		selector: components.NewSelector(choices, theme.Selector),
		panel:    components.NewPanel(theme.Panel),
		theme:    theme,
	}
}

// Completed reports whether OAuth completed successfully.
func (m *Model) Completed() bool { return m.completed }

// Canceled reports whether the user exited before authenticating.
func (m *Model) Canceled() bool { return m.canceled }

// Init requests terminal colors before drawing the picker.
func (m *Model) Init() tea.Cmd {
	return func() tea.Msg { return tea.RequestBackgroundColor() }
}

func (m *Model) applyTheme(theme styles.Theme) {
	m.theme = theme
	m.custom.SetStyles(theme.TextInput)
	m.callback.SetStyles(theme.TextInput)
	m.panel.SetStyles(theme.Panel)
	m.selector.SetStyles(theme.Selector)
}

// Update advances site selection, custom input, and the asynchronous OAuth flow.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.custom.SetWidth(max(12, min(48, msg.Width-10)))
		bodyWidth, _ := m.panel.BodySize(msg.Width, msg.Height, true)
		m.callback.SetWidth(max(1, bodyWidth-ansi.StringWidth(m.callback.Prompt)-1))
		return m, nil
	case tea.BackgroundColorMsg:
		m.applyTheme(styles.Default(msg.IsDark()))
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if m.phase == phaseWaiting && key != "ctrl+c" && key != "ctrl+y" && key != "enter" && key != "esc" {
			if !m.callbackVisible() {
				return m, nil
			}
			var cmd tea.Cmd
			m.callback, cmd = m.callback.Update(msg)
			return m, cmd
		}
		if m.phase == phaseCustom && key != "ctrl+c" && key != "enter" && key != "esc" {
			var cmd tea.Cmd
			m.custom, cmd = m.custom.Update(msg)
			return m, cmd
		}
		return m.handleKey(key)
	case tea.MouseClickMsg:
		m.linkPressed = msg.Button == tea.MouseLeft && msg.Mod == 0 && m.linkAt(msg.X, msg.Y)
		m.linkPressX, m.linkPressY = msg.X, msg.Y
		return m, nil
	case tea.MouseMotionMsg:
		if m.linkPressed && (msg.X != m.linkPressX || msg.Y != m.linkPressY) {
			m.linkPressed = false
		}
		return m, nil
	case tea.MouseReleaseMsg:
		pressed := m.linkPressed
		m.linkPressed = false
		if pressed && msg.Button == tea.MouseLeft && msg.Mod == 0 && msg.X == m.linkPressX && msg.Y == m.linkPressY && m.linkAt(msg.X, msg.Y) {
			if m.browserOpenErr != nil {
				m.copiedUntil = time.Now().Add(copyFeedback)
				return m, tea.SetClipboard(m.authorizationURL)
			}
			attempt, target := m.attempt, m.authorizationURL
			ctx := m.attemptCtx
			return m, func() tea.Msg { return linkOpenMsg{attempt: attempt, err: browser.Open(ctx, target)} }
		}
		return m, nil
	case linkOpenMsg:
		if msg.attempt == m.attempt && m.phase == phaseWaiting {
			m.browserOpenErr = msg.err
		}
		return m, nil
	case browserStatusMsg:
		if msg.attempt != m.attempt || m.phase != phaseWaiting || msg.done {
			return m, nil
		}
		if msg.status.AuthorizationURL != "" {
			m.authorizationURL = msg.status.AuthorizationURL
			m.browserOpenErr = msg.status.OpenError
		}
		m.callbackErr = msg.status.InputError
		if msg.updates != nil {
			return m, waitBrowserStatus(msg.attempt, msg.updates, msg.ctx)
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
			if time.Now().After(m.copiedUntil) {
				m.copiedUntil = time.Time{}
			}
			return m, spinnerTick(m.attempt)
		}
	case completionPauseMsg:
		if msg.attempt == m.attempt && m.phase == phaseComplete {
			return m, func() tea.Msg { return CompletedMsg{} }
		}
	}

	if m.phase == phaseCustom {
		var cmd tea.Cmd
		m.custom, cmd = m.custom.Update(msg)
		return m, cmd
	}
	if m.phase == phaseWaiting && m.callbackVisible() {
		var cmd tea.Cmd
		m.callback, cmd = m.callback.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		m.abort()
		if !m.completed {
			m.canceled = true
		}
		return m, tea.Quit
	}

	switch m.phase {
	case phaseSelect:
		if m.selector.UpdateKey(key) {
			return m, nil
		}
		switch key {
		case "enter":
			selected := m.selector.Index()
			if selected == customOptionIndex() {
				m.phase = phaseCustom
				m.loginErr = nil
				return m, m.custom.Focus()
			}
			return m, m.startLogin("https://" + siteOptions[selected].WebHost)
		case "esc":
			m.canceled = true
			return m, tea.Quit
		}
	case phaseCustom:
		switch key {
		case "enter":
			site, err := normalizeCustomSite(m.custom.Value())
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
		if key == "ctrl+y" && m.authorizationURL != "" {
			m.copiedUntil = time.Now().Add(copyFeedback)
			return m, tea.SetClipboard(m.authorizationURL)
		}
		if key == "enter" && m.callbackVisible() {
			if raw := strings.TrimSpace(m.callback.Value()); raw != "" {
				select {
				case m.callbackURLs <- raw:
					m.callback.SetValue("")
					m.callbackErr = nil
				default:
				}
			}
			return m, nil
		}
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
	case phaseComplete:
		// Ignore ordinary keys during the brief success confirmation. Ctrl+C is
		// handled above as a successful early exit, not a canceled login.
	}
	return m, nil
}

func (m *Model) startLogin(site string) tea.Cmd {
	m.abort()
	m.attempt++
	attempt := m.attempt
	ctx, cancel := context.WithCancel(m.ctx)
	m.attemptCtx = ctx
	m.cancel = cancel
	m.activeSite = site
	m.authorizationURL = ""
	m.browserOpenErr = nil
	m.linkPressed = false
	m.copiedUntil = time.Time{}
	m.callback.SetValue("")
	m.callbackErr = nil
	m.callbackURLs = make(chan string, 1)
	callbacks := m.callbackURLs
	m.loginErr = nil
	m.phase = phaseWaiting
	m.spinner = 0

	login := m.login
	status := make(chan BrowserStatus, 2)
	report := func(update BrowserStatus) {
		select {
		case status <- update:
		case <-ctx.Done():
		}
	}
	return tea.Batch(
		func() tea.Msg {
			if login == nil {
				close(status)
				return loginFinishedMsg{attempt: attempt, err: errors.New("OAuth login is unavailable")}
			}
			err := login(ctx, site, callbacks, report)
			close(status)
			return loginFinishedMsg{attempt: attempt, err: err}
		},
		waitBrowserStatus(attempt, status, ctx),
		spinnerTick(attempt),
		m.callback.Focus(),
	)
}

func waitBrowserStatus(attempt uint64, updates <-chan BrowserStatus, ctx context.Context) tea.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		select {
		case update, ok := <-updates:
			return browserStatusMsg{attempt: attempt, status: update, updates: updates, ctx: ctx, done: !ok}
		case <-ctx.Done():
			return browserStatusMsg{attempt: attempt, done: true}
		}
	}
}

func (m *Model) abort() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.attemptCtx = nil
	m.attempt++ // make any result already in flight stale
}

func spinnerTick(attempt uint64) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{attempt: attempt}
	})
}

func normalizeCustomSite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter your Datadog site")
	}
	cfg, err := auth.ConfigForSite(raw, "")
	if err != nil {
		return "", fmt.Errorf("enter a Datadog site, such as acme.us3.datadoghq.com: %w", err)
	}
	if cfg.Site == cfg.AssistantBase {
		return "", errors.New("enter your Datadog site, not its API endpoint, such as acme.us3.datadoghq.com")
	}
	return cfg.Site, nil
}

// View composes the login state into shared panel and selector components.
func (m *Model) View() tea.View {
	view := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, BackgroundColor: m.theme.Background}
	if m.width <= 0 || m.height <= 0 {
		view.Content = "Loading…"
		return view
	}
	view.Content = m.panel.View(m.width, m.height, m.panelContent())
	return view
}

func (m *Model) callbackVisible() bool {
	return m.panel.Fits(m.width, m.height, m.panelContent())
}

func (m *Model) panelContent() components.PanelContent {
	content := components.PanelContent{
		Title:          m.title(),
		Dismiss:        "ESC x",
		Body:           m.bodyView,
		CompactTitle:   "Sign in to Bits",
		CompactMessage: "Resize the terminal to continue.",
		TinyMessage:    "Resize terminal to sign in",
	}
	if m.phase == phaseWaiting && m.authorizationURL != "" {
		compactLabel := "Open sign-in link"
		if m.browserOpenErr != nil {
			compactLabel = "Copy sign-in link"
		}
		copyHint := "ctrl+y copy link"
		if m.copyVisible() {
			copyHint = "Copied sign-in link"
		}
		content.CompactMessage = m.authorizationLink(compactLabel) + "\n" + copyHint + "\nEnlarge window to paste"
		content.TinyMessage = "Enlarge window to paste"
	}
	switch m.phase {
	case phaseSelect:
		content.FooterLeft = "↑/↓ navigate"
		content.FooterRight = "enter to continue"
	case phaseCustom:
		content.FooterLeft = "esc back"
		content.FooterRight = "enter to continue"
	case phaseWaiting:
		content.FooterLeft = "esc choose another site"
		if m.authorizationURL != "" {
			content.FooterRight = "ctrl+y copy sign-in link"
		}
	case phaseError:
		content.FooterLeft = "esc choose another site"
		content.FooterRight = "enter to retry"
	case phaseComplete:
		// The success state has no available action before handoff.
	}
	return content
}

func (m *Model) authorizationLink(label string) string {
	return ansi.SetHyperlink(m.authorizationURL) + m.theme.Text.Link.Render(label) + ansi.ResetHyperlink()
}

func (m *Model) linkLabel() string {
	if m.browserOpenErr != nil {
		return "Click to copy sign-in link"
	}
	return "Open sign-in link"
}

func (m *Model) copyVisible() bool {
	return !m.copiedUntil.IsZero() && time.Now().Before(m.copiedUntil)
}

// linkAt reads the rendered cells, like the chat transcript's LinkAt. This
// follows both centered and compact panel layouts without duplicated geometry.
func (m *Model) linkAt(x, y int) bool {
	if m.phase != phaseWaiting || m.authorizationURL == "" || x < 0 || x >= m.width || y < 0 || y >= m.height {
		return false
	}
	buf := uv.NewScreenBuffer(m.width, m.height)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(m.View().Content).Draw(&buf, buf.Bounds())
	line := buf.Line(y)
	if x >= len(line) {
		return false
	}
	for x > 0 && line[x].Width == 0 {
		x--
	}
	return line[x].Width > 0 && line[x].Link.URL == m.authorizationURL
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

func (m *Model) bodyView(width, _ int) string {
	text := m.theme.Text
	feedback := m.theme.Feedback
	switch m.phase {
	case phaseCustom:
		parts := []string{
			text.Secondary.Render("Enter the hostname where your organization lives."),
			"",
			ansi.Truncate(m.custom.View(), width, "…"),
		}
		if m.loginErr != nil {
			parts = append(parts, "", textwrap.WrapProseText(width, escape.Multiline(m.loginErr.Error()), feedback.Error))
		}
		return strings.Join(parts, "\n")
	case phaseWaiting:
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		parts := []string{
			text.Secondary.Render("Sign in to Datadog in your browser."),
			"",
			feedback.Progress.Render(frames[m.spinner%len(frames)] + "  Waiting for Datadog"),
			text.Secondary.Render(strings.TrimPrefix(m.activeSite, "https://")),
		}
		if m.browserOpenErr != nil {
			parts[0] = text.Secondary.Render("Copy the sign-in link and paste it into a browser on your computer.")
		}
		if m.authorizationURL != "" {
			parts = append(parts, "", m.authorizationLink(m.linkLabel()))
			if m.copyVisible() {
				parts = append(parts, feedback.Success.Render("✓  Copied sign-in link"))
			}
			parts = append(parts, "", textwrap.WrapProseText(width, "If the redirect fails, copy the address from your browser.", text.Secondary), ansi.Truncate(m.callback.View(), width, "…"))
		}
		if m.callbackErr != nil {
			parts = append(parts, textwrap.WrapProseText(width, m.callbackErr.Error(), feedback.Error))
		}
		return strings.Join(parts, "\n")
	case phaseError:
		message := "Login did not complete."
		if errors.Is(m.loginErr, auth.ErrLoginTimedOut) {
			message = "Sign-in timed out. Press Enter to retry."
		} else if m.loginErr != nil {
			message = escape.Multiline(m.loginErr.Error())
		}
		return strings.Join([]string{
			textwrap.WrapProseText(width, message, feedback.Error),
			text.Secondary.Render(strings.TrimPrefix(m.activeSite, "https://")),
		}, "\n")
	case phaseComplete:
		return strings.Join([]string{
			feedback.Success.Render("✓  Authentication complete"),
			text.Secondary.Render(strings.TrimPrefix(m.activeSite, "https://")),
		}, "\n")
	default:
		return strings.Join([]string{
			text.Secondary.Render("Select the site where your organization lives."),
			"",
			m.selector.View(width),
		}, "\n")
	}
}
