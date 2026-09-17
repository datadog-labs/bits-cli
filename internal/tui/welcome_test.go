package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/splash"
)

func welcomeModel(width, height int) *Model {
	m := newShell()
	m.width, m.height = width, height
	m.version = "v0.1.2"
	m.workspaceDisplayPath = "~/go/src/github.com/DataDog/bits-cli"
	return m
}

// welcomeModels covers both fact counts: without an engine only the name and
// path are known; with one the backend and authentication rows appear too.
func welcomeModels(width, height int) map[string]*Model {
	engined := welcomeModel(width, height)
	engined.engine = agent.New(fake.New(), assistant.SendOptions{})
	return map[string]*Model{"bare": welcomeModel(width, height), "engine": engined}
}

// A fact line wider than its column wraps, growing the block past its
// reservation.
func TestWelcomeFactsNeverExceedTheirWidth(t *testing.T) {
	for name, m := range welcomeModels(200, 40) {
		for width := welcomeMinFactsWidth; width <= 80; width++ {
			for i, line := range strings.Split(m.welcomeFacts(width), "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("%s: facts(%d) line %d measures %d columns", name, width, i, got)
				}
			}
		}
	}
}

// layoutTranscript subtracts welcomeHeight from the transcript, so the rendered
// block must occupy exactly that many lines in either logo form.
func TestWelcomeBlockHeightMatchesReservation(t *testing.T) {
	for name, m := range welcomeModels(0, 40) {
		for _, ready := range []bool{false, true} {
			m.splashReady = ready
			for width := minimumChatWidth; width <= 200; width++ {
				m.width = width
				if !m.showWelcome() {
					continue
				}
				if got := lipgloss.Height(m.welcomeView()); got != m.welcomeHeight() {
					t.Fatalf("%s ready=%v width=%d: block is %d rows, want %d",
						name, ready, width, got, m.welcomeHeight())
				}
			}
		}
	}
}

// A terminal too narrow for the logo must hide the block, not wrap it.
func TestWelcomeBlockNeverOverflowsTerminalWidth(t *testing.T) {
	for name, m := range welcomeModels(0, 40) {
		for _, ready := range []bool{false, true} {
			m.splashReady = ready
			for width := minimumChatWidth; width <= 200; width++ {
				m.width = width
				if !m.showWelcome() {
					continue
				}
				if got := lipgloss.Width(m.welcomeView()); got > width {
					t.Fatalf("%s ready=%v width=%d: block measures %d columns",
						name, ready, width, got)
				}
			}
		}
	}
}

// The shorter fact column sits centered against the logo, not at its top edge.
func TestWelcomeFactsCenteredAgainstLogo(t *testing.T) {
	m := welcomeModel(120, 40)
	for _, facts := range []string{
		m.welcomeFacts(60),
		"one\ntwo\nthree\nfour",
	} {
		body := lipgloss.JoinHorizontal(lipgloss.Center,
			m.welcomeLogo(), strings.Repeat(" ", welcomeGap), facts)
		lines := strings.Split(body, "\n")
		if len(lines) != splash.Rows {
			t.Fatalf("body is %d rows, want %d", len(lines), splash.Rows)
		}

		marker := strings.SplitN(ansi.Strip(facts), "\n", 2)[0]
		first := -1
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), marker) {
				first = i
				break
			}
		}
		if want := (splash.Rows - lipgloss.Height(facts)) / 2; first != want {
			t.Errorf("facts start on row %d, want %d (centered)", first, want)
		}
	}
}

// Truncated one-character values would read as noise beside the logo.
func TestWelcomeFactsDroppedWhenTooNarrow(t *testing.T) {
	if facts := welcomeModel(80, 40).welcomeFacts(welcomeMinFactsWidth - 1); facts != "" {
		t.Errorf("facts rendered at sub-minimum width: %q", facts)
	}
}

// The organization would cost a CurrentUser request, so it is never shown.
func TestWelcomeFactsOmitOrganization(t *testing.T) {
	m := welcomeModel(100, 40)
	m.statusIdentity = "Some User / Datadog HQ"
	if facts := m.welcomeFacts(60); strings.Contains(facts, "Datadog HQ") {
		t.Errorf("facts leaked the organization: %q", facts)
	}
}

func TestWelcomeHiddenUntilTerminalIsTallEnough(t *testing.T) {
	tall := welcomeModel(100, 40)
	needed := minimumChatHeight + tall.welcomeHeight()

	if welcomeModel(100, needed-1).showWelcome() {
		t.Error("welcome shown on a terminal too short to hold it")
	}
	if !welcomeModel(100, needed).showWelcome() {
		t.Error("welcome hidden on a terminal tall enough to hold it")
	}
}

func TestHostnameOf(t *testing.T) {
	for site, want := range map[string]string{
		"https://api.datadoghq.com":     "api.datadoghq.com",
		"https://dd.datad0g.com/api/v2": "dd.datad0g.com",
		"":                              "",
		"not a url":                     "not a url",
	} {
		if got := hostnameOf(site); got != want {
			t.Errorf("hostnameOf(%q) = %q, want %q", site, got, want)
		}
	}
}

func TestAuthenticationLabel(t *testing.T) {
	for mode, want := range map[string]string{
		"oauth":               "OAuth",
		"api-key":             "API key auth",
		"none (fake backend)": "none (fake backend)",
	} {
		if got := authenticationLabel(mode); got != want {
			t.Errorf("authenticationLabel(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestModelAndAuthJoinsKnownValues(t *testing.T) {
	if got := modelAndAuth("claude-opus", "OAuth"); got != "claude-opus · OAuth" {
		t.Errorf("both values: got %q", got)
	}
	if got := modelAndAuth("", "OAuth"); got != "OAuth" {
		t.Errorf("model absent: got %q", got)
	}
	if got := modelAndAuth("claude-opus", ""); got != "claude-opus" {
		t.Errorf("auth absent: got %q", got)
	}
}

// BuildVersion already returns a v-prefixed module version, so the block must
// not add one: an untagged build otherwise reads "vdev".
func TestWelcomeVersionRenderedAsGiven(t *testing.T) {
	for _, version := range []string{"v0.1.2", "dev", "dev-abc1234"} {
		m := welcomeModel(120, 40)
		m.version = version
		first := strings.SplitN(ansi.Strip(m.welcomeFacts(60)), "\n", 2)[0]
		if want := "bits " + version; first != want {
			t.Errorf("version %q rendered as %q, want %q", version, first, want)
		}
	}
}

func graphicsReply(id int, payload string) uv.KittyGraphicsEvent {
	return uv.KittyGraphicsEvent{Options: kitty.Options{ID: id}, Payload: []byte(payload)}
}

// The handler is where a regression would silently disable the image path or
// enable it on a terminal that cannot paint, so the wiring is pinned here and
// not only in the predicates it calls.
func TestGraphicsReplyDrivesLogoForm(t *testing.T) {
	for name, tc := range map[string]struct {
		before    bool
		event     uv.KittyGraphicsEvent
		want      bool
		wantTrans bool
	}{
		"probe ok enables the image": {
			event: graphicsReply(splash.ProbeID, "OK"), want: true, wantTrans: true},
		"probe error keeps the wordmark": {
			event: graphicsReply(splash.ProbeID, "EINVAL:bad key")},
		"another image's reply is ignored": {
			event: graphicsReply(splash.ProbeID+7, "OK")},
		"a second probe reply does not retransmit": {
			before: true, event: graphicsReply(splash.ProbeID, "OK"), want: true},
		"a rejected image falls back to the wordmark": {
			before: true, event: graphicsReply(splash.ImageID, "ENOENT:no such file")},
		"a stored image keeps the image": {
			before: true, event: graphicsReply(splash.ImageID, "OK"), want: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := welcomeModel(100, 40)
			m.mode = ModeChat
			m.splashReady = tc.before

			_, cmd := m.Update(tc.event)

			if m.splashReady != tc.want {
				t.Errorf("splashReady = %v, want %v", m.splashReady, tc.want)
			}
			if got := cmd != nil; got != tc.wantTrans {
				t.Errorf("transmitted = %v, want %v", got, tc.wantTrans)
			}
		})
	}
}

// The welcome block is only safe inside chatViewBase because it never coexists
// with transcript content, which is what keeps selection row mapping correct.
func TestWelcomeNeverShownWithTranscriptContent(t *testing.T) {
	m := welcomeModel(100, 40)
	if !m.showWelcome() {
		t.Fatal("expected the welcome block on an empty transcript")
	}
	m.blocks = []agent.Block{{}}
	if m.showWelcome() {
		t.Error("welcome shown alongside transcript content")
	}
}
