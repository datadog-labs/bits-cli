package tui

import (
	"net/url"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/splash"
)

const (
	welcomeGap = 2

	// Narrower than this, fact values truncate to noise, so they are dropped.
	welcomeMinFactsWidth = 12
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

// splashPanelHeight is the logo slot plus the panel's border and padding.
func (m *Model) splashPanelHeight() int {
	return splash.Rows + m.styles.Panel.Frame.GetVerticalFrameSize()
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
	if m.splashReady {
		return splash.Placeholder()
	}
	return m.styles.Logo.Render(splash.Wordmark())
}

func (m *Model) splashPanelView() string {
	logo := m.welcomeLogo()
	columns := []string{logo}
	if facts := m.welcomeFacts(m.welcomeContentWidth() - lipgloss.Width(logo) - welcomeGap); facts != "" {
		columns = append(columns, strings.Repeat(" ", welcomeGap), facts)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Center, columns...)
	return m.styles.Panel.Frame.
		Width(m.welcomePanelWidth()).
		MarginLeft(max(0, m.styles.Panel.HorizontalMargin)).
		Render(body)
}

// welcomeFacts renders one value per line, or "" when width leaves no room. The
// organization is omitted: it would cost a CurrentUser request.
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
		hostnameOf(runtime.Backend.Site),
		modelAndAuth(runtime.Model, authenticationLabel(runtime.Backend.AuthenticationMode)),
		m.workspaceDisplayPath,
	} {
		if value == "" {
			continue
		}
		value = ansi.Truncate(escape.SingleLine(value), width, "…")
		lines = append(lines, m.styles.Text.Secondary.Render(value))
	}
	return strings.Join(lines, "\n")
}

// hostnameOf reduces a base URL to its host, passing through what will not parse.
func hostnameOf(site string) string {
	parsed, err := url.Parse(site)
	if err != nil || parsed.Hostname() == "" {
		return site
	}
	return parsed.Hostname()
}

func modelAndAuth(model, authentication string) string {
	switch {
	case model == "":
		return authentication
	case authentication == "":
		return model
	default:
		return model + " · " + authentication
	}
}

// authenticationLabel matches the Bits CLI's wording; unknown modes pass through.
func authenticationLabel(mode string) string {
	switch mode {
	case "oauth":
		return "OAuth"
	case "api-key":
		return "API key auth"
	default:
		return mode
	}
}

// showResume gates the offer on an idle, empty transcript, an empty composer,
// a non-empty fetch, and room for at least one row.
func (m *Model) showResume() bool {
	// turnEvents, not just the transcript: a submitted turn empties the composer
	// and publishes no block until its first event, and resuming during it would
	// hit the engine gate.
	if len(m.transcript.Blocks) != 0 || m.turnEvents != nil || m.resume.empty() || m.editor.Value() != "" {
		return false
	}
	if !m.showSplashPanel() {
		return false
	}
	return m.resumeVisibleRows() > 0
}

// resumeVisibleRows is how many conversations fit below the panel, at most
// resumeRows, and 0 when not even one fits.
func (m *Model) resumeVisibleRows() int {
	available := m.frame.transcript.Dy() - m.splashPanelHeight() - 1
	for rows := min(resumeRows, len(m.resume.conversations)); rows > 0; rows-- {
		if resumeBlockHeight(rows) <= available {
			return rows
		}
	}
	return 0
}

// headerView is the transcript's leading content: the splash panel, plus the
// resume offer separated by one blank row when it applies.
func (m *Model) headerView() string {
	if !m.showSplashPanel() {
		return ""
	}
	header := m.splashPanelView()
	if m.showResume() {
		block := m.resume.view(m.styles, m.welcomePanelWidth(), m.resumeVisibleRows())
		indent := strings.Repeat(" ", max(0, m.styles.Panel.HorizontalMargin))
		rows := strings.Split(block, "\n")
		for i, row := range rows {
			rows[i] = indent + row
		}
		header += "\n\n" + strings.Join(rows, "\n")
	}
	return header
}
