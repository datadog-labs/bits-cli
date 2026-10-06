package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func TestPrintLoggedOutUsesOneSuccessMessage(t *testing.T) {
	var out bytes.Buffer
	if err := printLoggedOut(&out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Logged out of Bits.\n"; got != want {
		t.Fatalf("logout output = %q, want %q", got, want)
	}
}

type stubCredentialStore struct {
	session auth.Session
	err     error
}

func (s stubCredentialStore) Load() (auth.Session, error) { return s.session, s.err }
func (stubCredentialStore) Save(auth.Session) error       { return nil }
func (stubCredentialStore) Delete() error                 { return nil }

func validOAuthSession() auth.Session {
	return auth.Session{
		Site:         "https://api.datad0g.com",
		ClientID:     "oauth-client",
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
}

func testWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	workspace, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace
}

func TestStartupExplicitAPIKeyModeDoesNotFallThroughToFakeBackend(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	_, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAPIKey, Site: "api.datadoghq.com", PermissionsMode: agent.ModeManual},
		stubCredentialStore{session: validOAuthSession()},
		testWorkspace(t),
	)
	if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY must both be set") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestStartupMissingOAuthSelectsInteractiveLogin(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	model, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual},
		stubCredentialStore{err: auth.ErrNoSession},
		testWorkspace(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := updated.View().Content; !strings.Contains(got, "Choose your Datadog site") {
		t.Fatalf("startup view did not enter login:\n%s", got)
	}
}

func TestStartupCredentialStoreFailureDoesNotSelectLogin(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	storeErr := errors.New("keyring unavailable")
	_, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual},
		stubCredentialStore{err: storeErr},
		testWorkspace(t),
	)
	if !errors.Is(err, storeErr) {
		t.Fatalf("startup error = %v, want keyring failure", err)
	}
}

func TestStartupStoredOAuthEntersChatWithConversation(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	model, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAuto, ConversationID: "conversation-1", PermissionsMode: agent.ModeManual},
		stubCredentialStore{session: validOAuthSession()},
		testWorkspace(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := model.ConversationID(); got != "conversation-1" {
		t.Fatalf("conversation ID = %q", got)
	}
	if got := model.View().Content; strings.Contains(got, "Choose your Datadog site") {
		t.Fatalf("stored OAuth unexpectedly entered login:\n%s", got)
	}
}

func TestPrintResumeHintOnlyForValidConversationID(t *testing.T) {
	var out bytes.Buffer
	valid := "3f0d2b1c-9a4e-4f8b-b7c2-1d2e3f4a5b6c"
	if err := printResumeHint(&out, valid); err != nil {
		t.Fatal(err)
	}
	if want := "Resume this conversation with: bits --conversation " + valid + "\n"; out.String() != want {
		t.Fatalf("hint = %q, want %q", out.String(), want)
	}

	for name, id := range map[string]string{
		"escape sequence": valid[:35] + "\x1b[2J",
		"osc title":       "\x1b]0;pwned\x07" + valid,
		"empty":           "",
		"not a uuid":      "conversation-1",
	} {
		out.Reset()
		if err := printResumeHint(&out, id); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: hint = %q, want no output", name, out.String())
		}
	}
}
