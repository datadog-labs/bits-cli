package login

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSitePickerNavigatesAndStartsSelectedSite(t *testing.T) {
	called := make(chan string, 1)
	m := New(func(_ context.Context, site string) error {
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
	m := New(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.selected != customOptionIndex {
		t.Fatalf("up from first selected %d, want custom", m.selected)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selected != 0 {
		t.Fatalf("down from custom selected %d, want first", m.selected)
	}
}

func TestCustomDomainValidationAndLogin(t *testing.T) {
	called := make(chan string, 1)
	m := New(func(_ context.Context, site string) error {
		called <- site
		return nil
	})
	m.selected = customOptionIndex
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
	m := New(func(context.Context, string) error {
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

func TestEscapeCancelsWaitingAttemptAndIgnoresLateResult(t *testing.T) {
	finished := make(chan struct{})
	m := New(func(ctx context.Context, _ string) error {
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
	m := New(func(context.Context, string) error { return nil })
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.Canceled() || cmd == nil {
		t.Fatalf("canceled = %t, cmd = %v", m.Canceled(), cmd)
	}
}

func TestViewContainsVisualHierarchyAndFits(t *testing.T) {
	m := New(func(context.Context, string) error { return nil })
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View().Content
	plain := ansi.Strip(view)
	for _, want := range []string{"DATADOG", "Sign in to Bits", "US1", "Custom domain", "↑/↓ navigate"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q:\n%s", want, plain)
		}
	}
	for lineNo, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > 80 {
			t.Fatalf("view line %d width = %d, want <= 80", lineNo+1, got)
		}
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

	for _, raw := range []string{"", "http://app.datadoghq.com", "example.com", "app.datadoghq.com/path"} {
		if _, err := normalizeCustomSite(raw); err == nil {
			t.Errorf("normalizeCustomSite(%q) succeeded", raw)
		}
	}
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
