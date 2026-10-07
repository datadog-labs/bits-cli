package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/cmd"
	"github.com/datadog-labs/bits-cli/internal/site"
	"github.com/datadog-labs/bits-cli/internal/startup"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// clearStagingEnv clears inherited staging configuration for one test; empty
// values are equivalent to unset for the staging loader.
func clearStagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "")
	t.Setenv(site.EnvStagingDomain, "")
	t.Setenv(site.EnvStagingClientID, "")
}

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

// recordingCredentialStore counts mutations so a test can prove a failing
// startup left the stored credential untouched.
type recordingCredentialStore struct {
	session auth.Session
	err     error
	loads   int
	saves   int
	deletes int
}

func (s *recordingCredentialStore) Load() (auth.Session, error) {
	s.loads++
	return s.session, s.err
}

func (s *recordingCredentialStore) Save(session auth.Session) error {
	s.saves++
	s.session = session
	return nil
}

func (s *recordingCredentialStore) Delete() error {
	s.deletes++
	s.session = auth.Session{}
	s.err = auth.ErrNoSession
	return nil
}

func validOAuthSession() auth.Session {
	return auth.Session{
		Site:         "https://api.datadoghq.com",
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
	clearStagingEnv(t)
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
	clearStagingEnv(t)
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
	clearStagingEnv(t)
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

// A stored staging session whose environment is missing or names a different
// domain fails startup with the actionable configuration error: the site
// picker is never entered to hide it, the stored credential is not mutated,
// and no network fallback runs.
func TestStartupStagingConfigFailureDoesNotEnterLoginPicker(t *testing.T) {
	clearStagingEnv(t)
	t.Setenv("BITS_FAKE_BACKEND", "")
	workspace := testWorkspace(t)
	for name, env := range map[string]struct{ site, domain, clientID string }{
		"missing staging configuration": {site: "", domain: "", clientID: ""},
		"mismatched staging domain":     {site: "https://login.other.test", domain: "other.test", clientID: "other-client"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(site.EnvStagingSite, env.site)
			t.Setenv(site.EnvStagingDomain, env.domain)
			t.Setenv(site.EnvStagingClientID, env.clientID)
			stored := validOAuthSession()
			stored.Site = "https://api.staging.test"
			stored.ClientID = "saved-staging-client"
			store := &recordingCredentialStore{session: stored}

			model, err := startupModelWithStore(
				context.Background(),
				cmd.ChatOptions{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual},
				store,
				workspace,
			)
			if err == nil {
				t.Fatal("startup succeeded with an unusable stored staging session")
			}
			if model != nil {
				t.Fatal("startup returned a model; the login picker would hide the configuration failure")
			}
			if errors.Is(err, startup.ErrLoginRequired) {
				t.Fatalf("startup error = %v, must not be login-required", err)
			}
			if !errors.Is(err, auth.ErrSiteConfiguration) {
				t.Fatalf("startup error = %v, want auth.ErrSiteConfiguration", err)
			}
			for _, want := range []string{site.EnvStagingSite, site.EnvStagingDomain} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("startup error = %v, want it to name %q", err, want)
				}
			}
			if store.loads != 1 || store.saves != 0 || store.deletes != 0 {
				t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
			}
			if store.session != stored {
				t.Fatalf("stored session was mutated: %+v", store.session)
			}
		})
	}
}

func TestStartupStoredOAuthEntersChatWithConversation(t *testing.T) {
	clearStagingEnv(t)
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
