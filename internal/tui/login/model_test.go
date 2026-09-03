package login

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tuistyles "github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestSitePickerNavigatesAndStartsSelectedSite(t *testing.T) {
	called := make(chan string, 1)
	m := newModel(func(_ context.Context, site string) error {
		called <- site
		return nil
	})

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.phase != phaseWaiting {
		t.Fatalf("phase = %v, want waiting", m.phase)
	}
	runBatch(t, m, cmd)
	if got := <-called; got != "https://us3.datadoghq.com" {
		t.Fatalf("selected site = %q", got)
	}
	if !m.Completed() || m.phase != phaseComplete {
		t.Fatalf("completed = %t, phase = %v", m.Completed(), m.phase)
	}
}

func TestSitePickerWraps(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.selector.Index() != customOptionIndex() {
		t.Fatalf("up from first selected %d, want custom", m.selector.Index())
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selector.Index() != 0 {
		t.Fatalf("down from custom selected %d, want first", m.selector.Index())
	}
}

func TestCustomDomainValidationAndLogin(t *testing.T) {
	called := make(chan string, 1)
	m := newModel(func(_ context.Context, site string) error {
		called <- site
		return nil
	})
	m.selector.SetIndex(customOptionIndex())
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.phase != phaseCustom || !m.custom.Focused() {
		t.Fatalf("custom input phase = %v, focused = %t", m.phase, m.custom.Focused())
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if got := m.custom.Value(); got != "a" {
		t.Fatalf("typed custom value = %q", got)
	}

	m.custom.SetValue("example.com")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || m.loginErr == nil || m.phase != phaseCustom {
		t.Fatalf("invalid domain: cmd=%v err=%v phase=%v", cmd != nil, m.loginErr, m.phase)
	}

	m.custom.SetValue("Acme.US3.DatadogHQ.com")
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatch(t, m, cmd)
	if got := <-called; got != "https://acme.us3.datadoghq.com" {
		t.Fatalf("custom site = %q", got)
	}
}

func TestLoginErrorCanRetry(t *testing.T) {
	attempts := 0
	m := newModel(func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("authorization denied")
		}
		return nil
	})

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatch(t, m, cmd)
	if m.phase != phaseError || m.loginErr == nil {
		t.Fatalf("phase = %v, error = %v", m.phase, m.loginErr)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatch(t, m, cmd)
	if attempts != 2 || !m.Completed() {
		t.Fatalf("attempts = %d, completed = %t", attempts, m.Completed())
	}
}

func TestCompletionSignalsOwnerWithoutQuittingProgram(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	m.attempt = 1
	m.phase = phaseComplete
	m.completed = true
	_, cmd := m.Update(completionPauseMsg{attempt: 1})
	if cmd == nil {
		t.Fatal("completion returned no command")
	}
	if msg := cmd(); msg != (CompletedMsg{}) {
		t.Fatalf("completion command = %T, want CompletedMsg", msg)
	}
}

func TestControlCAfterCompletedLoginExitsWithoutReportingCancellation(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	m.phase = phaseComplete
	m.completed = true
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("control-C returned no quit command")
	}
	if m.Canceled() {
		t.Fatal("persisted successful login was reported as canceled")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("control-C command = %T, want tea.QuitMsg", cmd())
	}
}

func TestEscapeCancelsWaitingAttemptAndIgnoresLateResult(t *testing.T) {
	finished := make(chan struct{})
	m := newModel(func(ctx context.Context, _ string) error {
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("start command result = %T, want batch", cmd())
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- batch[0]() }()

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("login context was not canceled")
	}
	msg := <-result
	_, _ = m.Update(msg)
	if m.phase != phaseSelect || m.Completed() {
		t.Fatalf("stale result changed phase=%v completed=%t", m.phase, m.Completed())
	}
}

func TestEscapeFromPickerCancelsStartup(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.Canceled() || cmd == nil {
		t.Fatalf("canceled = %t, cmd = %v", m.Canceled(), cmd)
	}
}

func TestBackgroundColorAppliesCompleteSharedTheme(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#FFFFFF")})
	want := tuistyles.Default(false)
	if m.theme.IsDark {
		t.Fatal("theme remained dark")
	}
	if got := m.theme.Panel.Frame.GetBorderTopForeground(); got != want.Panel.Frame.GetBorderTopForeground() {
		t.Errorf("panel border = %v, want %v", got, want.Panel.Frame.GetBorderTopForeground())
	}
	if got := m.theme.Selector.Selected.GetForeground(); got != want.Selector.Selected.GetForeground() {
		t.Errorf("selector accent = %v, want %v", got, want.Selector.Selected.GetForeground())
	}
	if got := m.custom.Styles().Cursor.Color; got != want.TextInput.Cursor.Color {
		t.Errorf("text input cursor = %v, want %v", got, want.TextInput.Cursor.Color)
	}
}

func TestViewContainsVisualHierarchyAndFits(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View().Content
	plain := ansi.Strip(view)
	for _, want := range []string{"Choose your Datadog site", "esc ×", "US1", "Enter another domain", "↑/↓ navigate"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q:\n%s", want, plain)
		}
	}
	for _, domain := range []string{"app.datadoghq.com", "us3.datadoghq.com", "us5.datadoghq.com", "app.datadoghq.eu", "ap1.datadoghq.com", "ap2.datadoghq.com", "uk1.datadoghq.com"} {
		if got := strings.Count(plain, domain); got != 1 {
			t.Errorf("domain %q appears %d times, want one physical option row", domain, got)
		}
	}
	for lineNo, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > 80 {
			t.Fatalf("view line %d width = %d, want <= 80", lineNo+1, got)
		}
	}
}

func TestBrowserStatusCanAddURLThenReportLauncherFailure(t *testing.T) {
	reported := make(chan struct{})
	release := make(chan struct{})
	m := New(context.Background(), func(_ context.Context, _ string, report func(BrowserStatus)) error {
		report(BrowserStatus{AuthorizationURL: "https://app.datadoghq.com/oauth2/v1/authorize?client_id=bits"})
		report(BrowserStatus{
			AuthorizationURL: "https://app.datadoghq.com/oauth2/v1/authorize?client_id=bits",
			OpenError:        errors.New("browser unavailable"),
		})
		close(reported)
		<-release
		return nil
	})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	batch := cmd().(tea.BatchMsg)
	finished := make(chan tea.Msg, 1)
	go func() { finished <- batch[0]() }()
	<-reported

	_, next := m.Update(batch[1]())
	if m.authorizationURL == "" || m.browserOpenErr != nil || next == nil {
		t.Fatalf("initial browser status URL=%q error=%v next=%v", m.authorizationURL, m.browserOpenErr, next != nil)
	}
	_, _ = m.Update(next())
	if m.browserOpenErr == nil {
		t.Fatal("follow-up browser launch error was not applied")
	}
	close(release)
	_, _ = m.Update(<-finished)
}

func TestBrowserOpenFailureShowsManualURL(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = m.Update(browserStatusMsg{
		attempt: m.attempt,
		status: BrowserStatus{
			AuthorizationURL: "https://app.datadoghq.com/oauth2/v1/authorize?client_id=bits",
			OpenError:        errors.New("browser unavailable"),
		},
	})
	view := m.View().Content
	plain := ansi.Strip(view)
	for _, want := range []string{"couldn't open a browser", "browser unavailable", "Open Datadog login manually"} {
		if !strings.Contains(plain, want) {
			t.Errorf("browser fallback view missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(view, m.authorizationURL) {
		t.Fatal("manual login hyperlink does not retain the copy-safe authorization URL")
	}
}

func TestWaitingViewAlwaysProvidesManualBrowserLink(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = m.Update(browserStatusMsg{
		attempt: m.attempt,
		status:  BrowserStatus{AuthorizationURL: "https://app.datadoghq.com/oauth2/v1/authorize?client_id=bits"},
	})
	view := m.View().Content
	if !strings.Contains(ansi.Strip(view), "Open Datadog login manually") || !strings.Contains(view, m.authorizationURL) {
		t.Fatalf("waiting view missing manual hyperlink: %q", ansi.Strip(view))
	}
}

func TestCompactWaitingViewRetainsManualBrowserLink(t *testing.T) {
	m := newModel(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = m.Update(browserStatusMsg{
		attempt: m.attempt,
		status:  BrowserStatus{AuthorizationURL: "https://app.datadoghq.com/oauth2/v1/authorize?client_id=bits"},
	})
	view := m.View().Content
	if !strings.Contains(ansi.Strip(view), "Open Datadog login") || !strings.Contains(view, m.authorizationURL) {
		t.Fatalf("compact waiting view missing manual hyperlink: %q", ansi.Strip(view))
	}
}

func TestSmallTerminalUsesBoundedResizePrompt(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 16}, {36, 16}, {24, 8}, {10, 2}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := newModel(func(context.Context, string) error { return nil })
			_, _ = m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			view := m.View().Content
			lines := strings.Split(view, "\n")
			if len(lines) > size.height {
				t.Fatalf("small view height = %d, want <= %d", len(lines), size.height)
			}
			for lineNo, line := range lines {
				if got := ansi.StringWidth(line); got > size.width {
					t.Fatalf("small view line %d width = %d, want <= %d", lineNo+1, got, size.width)
				}
			}
			if size.width == 10 && !strings.Contains(ansi.Strip(view), "Resize") {
				t.Fatalf("small view = %q", ansi.Strip(view))
			}
		})
	}
}

func TestNormalizeCustomSite(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "app.datadoghq.com", want: "https://app.datadoghq.com"},
		{raw: "https://app.datadoghq.eu/", want: "https://app.datadoghq.eu"},
		{raw: "ACME.US5.DATADOGHQ.COM", want: "https://acme.us5.datadoghq.com"},
	} {
		got, err := normalizeCustomSite(test.raw)
		if err != nil || got != test.want {
			t.Errorf("normalizeCustomSite(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}

	for _, raw := range []string{"", "http://app.datadoghq.com", "example.com", "app.datadoghq.com/path", "app.ddog-gov.com"} {
		if _, err := normalizeCustomSite(raw); err == nil {
			t.Errorf("normalizeCustomSite(%q) succeeded", raw)
		}
	}

	for _, raw := range []string{"api.us3.datadoghq.com", "https://api.datadoghq.eu"} {
		if _, err := normalizeCustomSite(raw); err == nil || !strings.Contains(err.Error(), "not its API endpoint") {
			t.Errorf("normalizeCustomSite(%q) error = %v; want API endpoint rejection", raw, err)
		}
	}
}

func newModel(login func(context.Context, string) error) *Model {
	return New(context.Background(), func(ctx context.Context, site string, _ func(BrowserStatus)) error {
		return login(ctx, site)
	})
}

func runBatch(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command result = %T, want tea.BatchMsg", msg)
	}
	for _, child := range batch {
		msg := child()
		if _, ok := msg.(spinnerTickMsg); ok {
			continue
		}
		_, _ = m.Update(msg)
	}
}
