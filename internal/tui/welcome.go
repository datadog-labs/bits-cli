package tui

import (
	"context"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/site"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
	"github.com/datadog-labs/bits-cli/internal/tui/splash"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
	"github.com/datadog-labs/bits-cli/internal/update"
)

const (
	welcomeGap = 2

	// Narrower than this, fact values truncate to noise, so they are dropped.
	welcomeMinFactsWidth = 12

	aiNotice = "Bits is an AI assistant. It can make mistakes — review its suggestions and actions."
)

// welcomeLogoWidth is the wider of the two logo forms. Gating on it rather than
// on the current form keeps the probe's answer from changing visibility.
var welcomeLogoWidth = max(lipgloss.Width(splash.Wordmark()), splash.Columns)

// showSplashPanel gates on both dimensions. The panel is the transcript's
// first rows for the whole session, but a viewport that cannot hold it would
// otherwise leave no room for the conversation, and follow mode would scroll a
// taller header off at launch.
func (m *Model) showSplashPanel() bool {
	if m.frame.transcript.Dy() < m.splashPanelHeight() {
		return false
	}
	return m.welcomeContentWidth() >= welcomeLogoWidth
}

func (m *Model) splashPanelHeight() int {
	return lipgloss.Height(m.splashPanelBody()) + m.styles.Panel.Frame.GetVerticalFrameSize()
}

// welcomePanelWidth spans the terminal less the panel margin. Panel.MaxWidth is
// not applied: this is a banner, not a centered dialog.
func (m *Model) welcomePanelWidth() int {
	return max(1, m.width-2*max(0, m.styles.Panel.HorizontalMargin))
}

func (m *Model) welcomeContentWidth() int {
	return max(1, m.welcomePanelWidth()-m.styles.Panel.Frame.GetHorizontalFrameSize())
}

// welcomeLogo returns the image once the terminal answers the graphics probe,
// the wordmark otherwise. Both are splash.Rows tall.
func (m *Model) welcomeLogo() string {
	if m.graphics.Support == graphics.Supported {
		return splash.Placeholder()
	}
	return m.styles.Logo.Render(splash.Wordmark())
}

func (m *Model) splashPanelView() string {
	// The splash box edges the composer, not the panels: the two frame the same
	// column, so the border is read off Input.Block rather than copied.
	return m.styles.Panel.Frame.
		Width(m.welcomePanelWidth()).
		MarginLeft(max(0, m.styles.Panel.HorizontalMargin)).
		BorderForeground(m.styles.Input.Block.GetBorderTopForeground()).
		Render(m.splashPanelBody())
}

func (m *Model) splashPanelBody() string {
	width := m.welcomeContentWidth()
	if facts := m.welcomeFacts(width - welcomeLogoWidth - welcomeGap); facts != "" {
		return lipgloss.JoinHorizontal(lipgloss.Center, m.welcomeLogo(), strings.Repeat(" ", welcomeGap), facts)
	}
	notice := textwrap.WrapProseText(width, aiNotice, m.styles.Text.Secondary)
	return lipgloss.JoinVertical(lipgloss.Left, m.welcomeLogo(), "", notice)
}

func (m *Model) welcomeFacts(width int) string {
	if width < welcomeMinFactsWidth {
		return ""
	}

	var runtime agent.RuntimeStatus
	if m.engine != nil {
		runtime = m.engine.Status()
	}

	name := m.styles.Text.Primary.Bold(true).Render("bits")
	if m.version != "" {
		// As given: module versions already carry a "v".
		name += m.styles.Text.Secondary.Render(" " + m.version)
	}
	lines := []string{ansi.Truncate(name, width, "…")}

	for _, value := range []string{
		joinFacts(firstNonEmpty(m.currentUser.Name, m.currentUser.Handle, m.currentUser.Email),
			strings.TrimSpace(m.currentUser.Organization), webHostOf(runtime.Backend.Site)),
		joinFacts(runtime.Model, authenticationLabel(runtime.Backend.AuthenticationMode)),
		m.workspaceDisplayPath,
	} {
		if value == "" {
			continue
		}
		value = ansi.Truncate(escape.SingleLine(value), width, "…")
		lines = append(lines, m.styles.Text.Secondary.Render(value))
	}
	lines = append(lines, "")
	lines = append(lines, textwrap.WrapProse(width, aiNotice, m.styles.Text.Secondary)...)
	return strings.Join(lines, "\n")
}

// webHostOf names the Datadog web app for an API base URL, as users know it
// from the browser. A host outside the catalog stays as configured, and what
// will not parse passes through.
func webHostOf(apiSite string) string {
	parsed, err := url.Parse(apiSite)
	if err != nil || parsed.Hostname() == "" {
		return apiSite
	}
	if web, ok := site.WebHostForAssistantHost(parsed.Hostname()); ok {
		return web
	}
	return parsed.Hostname()
}

// joinFacts shares one row between related facts, skipping unknown ones, so
// facts arriving late never change the panel's height.
func joinFacts(values ...string) string {
	var known []string
	for _, value := range values {
		if value != "" {
			known = append(known, value)
		}
	}
	return strings.Join(known, " · ")
}

type currentUserMsg struct{ user assistant.CurrentUser }

// fetchCurrentUser names the login and org for the welcome panel. A failure
// stays silent: the panel only loses the names, and /status explains why.
func (m *Model) fetchCurrentUser() tea.Cmd {
	engine := m.engine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
		defer cancel()
		identity, err := engine.CurrentUser(ctx)
		if err != nil {
			return nil
		}
		return currentUserMsg{user: identity}
	}
}

// authenticationLabel names only a mode the user chose: OAuth is the default,
// so naming it on every launch says nothing. Unknown modes pass through.
func authenticationLabel(mode string) string {
	switch mode {
	case "oauth":
		return ""
	case "api-key":
		return "API key auth"
	default:
		return mode
	}
}

// welcomeIdle gates the welcome offers on an idle, empty transcript, an empty
// composer, and a visible splash panel.
func (m *Model) welcomeIdle() bool {
	// The operation, not just the transcript: a submitted turn empties the
	// composer and publishes no block until its first event, and resuming during
	// it would hit the engine gate.
	if len(m.state.Blocks) != 0 || m.op.busy() || m.editor.Value() != "" || len(m.editor.Attachments()) > 0 {
		return false
	}
	return m.showSplashPanel()
}

// welcomeOfferHeight reserves the panel, its gap, and transcript notices before
// sizing either welcome offer. Otherwise following the tail clips the panel.
func (m *Model) welcomeOfferHeight() int {
	return m.frame.transcript.Dy() - m.splashPanelHeight() - 1 - m.list.NoticeRows()
}

// showResume gates the offer on the welcome conditions, a running or non-empty
// fetch, and room for at least one row. A pending update offer takes its place.
func (m *Model) showResume() bool {
	if m.resume.rows() == 0 || m.showUpgrade() || !m.welcomeIdle() {
		return false
	}
	return m.resumeVisibleRows() > 0
}

// resumeVisibleRows is how many conversations fit below the panel, at most
// resumeMaxRows, and 0 when not even one fits.
func (m *Model) resumeVisibleRows() int {
	available := m.welcomeOfferHeight()
	for rows := min(resumeMaxRows, m.resume.rows()); rows > 0; rows-- {
		if resumeBlockHeight(rows) <= available {
			return rows
		}
	}
	return 0
}

// headerView is the transcript's leading content: the splash panel, plus the
// update or resume offer separated by one blank row when one applies.
func (m *Model) headerView() string {
	if !m.showSplashPanel() {
		return ""
	}
	header := m.splashPanelView()
	var block string
	switch {
	case m.showUpgrade():
		block = m.upgradeView(m.welcomePanelWidth())
	case m.showResume():
		block = m.resume.view(m.styles, m.welcomePanelWidth(), m.resumeVisibleRows())
	default:
		return header
	}
	return header + "\n\n" + lipgloss.NewStyle().MarginLeft(max(0, m.styles.Panel.HorizontalMargin)).Render(block)
}

// upgradePrompt is the update offer. Until answered it takes the resume
// offer's place on the welcome screen, under the same conditions.
type upgradePrompt struct {
	offer     *update.Offer
	pending   bool
	cursor    int
	requested bool
}

func newUpgradePrompt(offer *update.Offer) upgradePrompt {
	return upgradePrompt{offer: offer, pending: offer != nil && offer.Latest != ""}
}

var upgradeChoices = [...]string{"Update now", "Remind me later", "Skip this version"}

const upgradeHint = " ↑/↓ choose · start typing for a new conversation "

// UpgradeRequested reports whether the user asked to update Bits. The session
// has quit; the caller runs the installer.
func (m *Model) UpgradeRequested() bool { return m.upgrade.requested }

func (m *Model) showUpgrade() bool {
	return m.upgrade.pending && m.welcomeIdle() &&
		resumeBlockHeight(len(upgradeChoices)) <= m.welcomeOfferHeight()
}

func (m *Model) moveUpgrade(delta int) {
	m.upgrade.cursor = min(max(m.upgrade.cursor+delta, 0), len(upgradeChoices)-1)
}

// chooseUpgrade answers the offer, which then gives way to the resume offer.
func (m *Model) chooseUpgrade() tea.Cmd {
	m.upgrade.pending = false
	var err error
	switch m.upgrade.cursor {
	case 0:
		_, cmd := m.requestUpgrade()
		return cmd
	case 1:
		err = m.upgrade.offer.Postpone()
	case 2:
		err = m.upgrade.offer.Skip()
	}
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not save your update choice."))
	}
	return nil
}

// requestUpgrade ends the session like /quit, since the installer needs the
// terminal, and leaves the command layer to run it.
func (m *Model) requestUpgrade() (tea.Model, tea.Cmd) {
	if m.upgrade.offer == nil {
		return m, m.postNotice(notice(chat.NoticeWarn, update.ErrUnsupportedInstallation,
			"Automatic updates are unavailable for this installation."))
	}
	m.upgrade.pending = false
	m.upgrade.requested = true
	return m.quit()
}

// upgradeView renders exactly resumeBlockHeight(len(upgradeChoices)) rows,
// laid out like the resume offer so either can fill the same space.
func (m *Model) upgradeView(width int) string {
	offer := m.upgrade.offer
	title := "Bits " + offer.Latest + " is available. You're on " + offer.Current + "."
	lines := []string{
		ansi.Truncate(m.styles.Text.Primary.Bold(true).Render(title), width, "…"),
		"",
	}
	for i, label := range upgradeChoices {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, offerRow(m.styles, width, i == m.upgrade.cursor, label, ""))
	}
	return strings.Join(append(lines, "", offerHint(m.styles, width, upgradeHint)), "\n")
}
