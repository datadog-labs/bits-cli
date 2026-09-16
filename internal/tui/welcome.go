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

// showWelcome gates on both dimensions: content that does not fit wraps and
// exceeds the height layoutTranscript reserved.
func (m *Model) showWelcome() bool {
	if len(m.blocks) != 0 || m.height < minimumChatHeight+m.welcomeHeight() {
		return false
	}
	return m.welcomeContentWidth() >= lipgloss.Width(m.welcomeLogo())
}

// welcomeHeight is the logo slot plus the panel's border and padding.
func (m *Model) welcomeHeight() int {
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

func (m *Model) welcomeView() string {
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
		name += m.styles.Text.Secondary.Render(" v" + m.version)
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
