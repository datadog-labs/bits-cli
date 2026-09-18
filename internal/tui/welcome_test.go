package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// layoutTranscript subtracts splashPanelHeight from the transcript, so the
// rendered block must occupy exactly that many lines in either logo form.
func TestWelcomeBlockHeightMatchesReservation(t *testing.T) {
	for name, m := range welcomeModels(0, 40) {
		for _, ready := range []bool{false, true} {
			m.splashReady = ready
			for width := minimumChatWidth; width <= 200; width++ {
				m.width = width
				if !m.showSplashPanel() {
					continue
				}
				if got := lipgloss.Height(m.splashPanelView()); got != m.splashPanelHeight() {
					t.Fatalf("%s ready=%v width=%d: block is %d rows, want %d",
						name, ready, width, got, m.splashPanelHeight())
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
				if !m.showSplashPanel() {
					continue
				}
				if got := lipgloss.Width(m.splashPanelView()); got > width {
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

// The new height gate gauges against transcriptHeight (notice/footer/composer
// subtracted), not the old minimumChatHeight reservation.
func TestWelcomeHiddenUntilTerminalIsTallEnough(t *testing.T) {
	tall := welcomeModel(100, 40)
	needed := tall.splashPanelHeight() + chatNoticeHeight + chatFooterHeight + tall.composerHeight()

	if welcomeModel(100, needed-1).showSplashPanel() {
		t.Error("splash panel shown on a terminal too short to hold it")
	}
	if !welcomeModel(100, needed).showSplashPanel() {
		t.Error("splash panel hidden on a terminal tall enough to hold it")
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
			event: graphicsReply(splash.ProbeID, "OK"), want: true, wantTrans: true,
		},
		"probe error keeps the wordmark": {
			event: graphicsReply(splash.ProbeID, "EINVAL:bad key"),
		},
		"another image's reply is ignored": {
			event: graphicsReply(splash.ProbeID+7, "OK"),
		},
		"a second probe reply does not retransmit": {
			before: true, event: graphicsReply(splash.ProbeID, "OK"), want: true,
		},
		"a rejected image falls back to the wordmark": {
			before: true, event: graphicsReply(splash.ImageID, "ENOENT:no such file"),
		},
		"a stored image keeps the image": {
			before: true, event: graphicsReply(splash.ImageID, "OK"), want: true,
		},
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

// The panel is a session header now, so a filled transcript must not hide it.
func TestSplashPanelSurvivesTranscriptContent(t *testing.T) {
	m := welcomeModel(120, 40)
	m.blocks = []agent.Block{{
		ID:       agent.BlockID{Scope: agent.ScopeLocal, Key: "a", Kind: assistant.KindText},
		Kind:     assistant.KindText,
		Complete: true,
		Markdown: &assistant.MarkdownPayload{Content: "hello"},
	}}
	if !m.showSplashPanel() {
		t.Fatal("panel hidden once the transcript had content")
	}
	if m.showResume() {
		t.Fatal("resume offered with a non-empty transcript")
	}
}

// The header is one string handed to chat.List; its height must equal the two
// blocks' computed heights plus the blank row between them.
func TestHeaderHeightMatchesItsParts(t *testing.T) {
	m := welcomeModel(120, 40)
	m.resume = *resumeFixture(32)
	for width := 40; width <= 200; width++ {
		m.width = width
		if !m.showSplashPanel() {
			continue
		}
		want := m.splashPanelHeight()
		if m.showResume() {
			want += 1 + resumeBlockHeight(m.resumeVisibleRows())
		}
		if got := lipgloss.Height(m.headerView()); got != want {
			t.Fatalf("width=%d: header is %d rows, want %d", width, got, want)
		}
	}
}

func TestHeaderNeverExceedsTerminalWidth(t *testing.T) {
	m := welcomeModel(0, 40)
	m.resume = *resumeFixture(32)
	for _, ready := range []bool{false, true} {
		m.splashReady = ready
		for width := minimumChatWidth; width <= 200; width++ {
			m.width = width
			for i, line := range strings.Split(m.headerView(), "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("ready=%v width=%d line %d measures %d columns", ready, width, i, got)
				}
			}
		}
	}
}

// The header must always fit the transcript viewport, or follow-mode scrolls
// the panel off at launch. Rows degrade, then the offer drops entirely.
func TestResumeRowsDegradeUntilTheHeaderFits(t *testing.T) {
	m := welcomeModel(120, 40)
	m.resume = *resumeFixture(32)
	previous := resumeRows + 1
	for height := 40; height >= 1; height-- {
		m.height = height
		rows := 0
		if m.showResume() {
			rows = m.resumeVisibleRows()
		}
		if rows > previous {
			t.Fatalf("height=%d: rows grew from %d to %d as the terminal shrank", height, previous, rows)
		}
		if rows > 0 && lipgloss.Height(m.headerView()) > m.transcriptHeight() {
			t.Fatalf("height=%d: header %d rows exceeds the %d-row viewport",
				height, lipgloss.Height(m.headerView()), m.transcriptHeight())
		}
		previous = rows
	}
}

// An unanswered or failed fetch leaves no conversations, and the offer must
// then contribute nothing.
func TestResumeHiddenWithoutConversations(t *testing.T) {
	m := welcomeModel(120, 40)
	if m.showResume() {
		t.Fatal("resume offered before any conversations arrived")
	}
	if got, want := lipgloss.Height(m.headerView()), m.splashPanelHeight(); got != want {
		t.Fatalf("header is %d rows, want just the panel's %d", got, want)
	}
}

func TestResumeHiddenWhileComposerHasText(t *testing.T) {
	m := welcomeModel(120, 40)
	m.resume = *resumeFixture(4)
	if !m.showResume() {
		t.Fatal("resume not offered with an empty composer")
	}
	m.editor.SetValue("draft")
	if m.showResume() {
		t.Fatal("resume still offered while the composer held text")
	}
	m.editor.SetValue("")
	if !m.showResume() {
		t.Fatal("resume did not return once the composer was cleared")
	}
}

// The header is the transcript's content now, not a section above it, so the
// chat view must not paint it twice.
func TestChatViewRendersTheHeaderOnlyThroughTheTranscript(t *testing.T) {
	m := welcomeModel(120, 40)
	m.resume = *resumeFixture(4)
	m.mode = ModeChat
	m.layoutTranscript()
	if got := strings.Count(m.chatViewBase(m.list.Render()), resumeTitle); got != 1 {
		t.Fatalf("resume title appears %d times in the chat view, want 1", got)
	}
}

// A successful fetch must make the offer appear; a failure must leave it absent.
func TestRecentConversationsResultDrivesTheOffer(t *testing.T) {
	for _, test := range []struct {
		name   string
		result agent.ConversationListResult
		want   bool
	}{
		{"populated", agent.ConversationListResult{Conversations: resumeFixture(4).conversations}, true},
		{"empty", agent.ConversationListResult{}, false},
		{"failed", agent.ConversationListResult{Err: errors.New("offline")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := welcomeModel(120, 40)
			m.mode = ModeChat
			m.Update(recentConversationsMsg{result: test.result})
			if got := m.showResume(); got != test.want {
				t.Fatalf("showResume = %v, want %v", got, test.want)
			}
		})
	}
}

// A growing composer eats the rows the header needs. Whatever the header shows,
// the composed view must stay inside the terminal, or the composer the user is
// typing into is pushed off the bottom.
func TestHeaderNeverPushesTheViewPastTheTerminal(t *testing.T) {
	for _, height := range []int{18, 19, 20, 21, 22} {
		m := welcomeModel(120, height)
		m.mode = ModeChat
		// An unfocused editor ignores input, which would leave the composer one
		// row tall and the loop asserting nothing.
		m.editor.Focus()
		for lines := 1; lines <= 12; lines++ {
			m.editor.Reset()
			m.editor.Update(tea.PasteMsg{Content: strings.Repeat("draft\n", lines)})
			if m.editor.Value() == "" {
				t.Fatalf("height=%d lines=%d: the composer took no input", height, lines)
			}
			m.layoutTranscript()

			view := lipgloss.Height(m.chatViewBase(m.list.Render()))
			if view > height {
				t.Fatalf("height=%d lines=%d: view is %d rows, past the terminal (panel shown=%v)",
					height, lines, view, m.showSplashPanel())
			}
		}
	}
}

// Both logo forms are the same height but not the same width, so a gate that
// reads the current form flips when the probe answers.
func TestWelcomeVisibilityUnchangedByTheProbe(t *testing.T) {
	for width := minimumChatWidth; width <= 200; width++ {
		m := welcomeModel(width, 40)
		m.mode = ModeChat

		m.splashReady = false
		before := m.showSplashPanel()
		m.splashReady = true
		if after := m.showSplashPanel(); after != before {
			t.Fatalf("width=%d: visibility changed from %v to %v when the probe landed",
				width, before, after)
		}
	}
}
