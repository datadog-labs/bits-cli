package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
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

func TestStartupExplicitAPIKeyModeDoesNotFallThroughToFakeBackend(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	_, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAPIKey, Site: "api.datadoghq.com", ApprovalMode: agent.ModeAllowAll},
		stubCredentialStore{session: validOAuthSession()},
	)
	if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY must both be set") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestStartupMissingOAuthSelectsInteractiveLogin(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	model, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAuto, ApprovalMode: agent.ModeAllowAll},
		stubCredentialStore{err: auth.ErrNoSession},
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
		cmd.ChatOptions{AuthMode: auth.ModeAuto, ApprovalMode: agent.ModeAllowAll},
		stubCredentialStore{err: storeErr},
	)
	if !errors.Is(err, storeErr) {
		t.Fatalf("startup error = %v, want keyring failure", err)
	}
}

func TestStartupStoredOAuthEntersChatWithConversation(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	model, err := startupModelWithStore(
		context.Background(),
		cmd.ChatOptions{AuthMode: auth.ModeAuto, ConversationID: "conversation-1", ApprovalMode: agent.ModeAllowAll},
		stubCredentialStore{session: validOAuthSession()},
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

func TestWorkspaceFileIndexIsSortedAndSkipsGeneratedTrees(t *testing.T) {
	root := t.TempDir()
	for path, contents := range map[string]string{
		"z.go":                     "package example\n",
		"docs/東京.md":               "hello\n",
		".git/config":              "secret-ish metadata\n",
		"vendor/example/code.go":   "package vendor\n",
		"node_modules/pkg/file.js": "module.exports = {}\n",
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"docs/東京.md", "z.go"}
	if got := workspaceFileIndex(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace files = %#v, want %#v", got, want)
	}
}
