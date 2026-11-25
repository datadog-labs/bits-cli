package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/auth"
)

type acpClient struct {
	acpsdk.Client // unimplemented methods panic
	mu            sync.Mutex
	text          strings.Builder
}

func (c *acpClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if chunk := n.Update.AgentMessageChunk; chunk != nil {
		c.text.WriteString(chunk.Content.Text.Text)
	}
	return nil
}

func (c *acpClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected(p.Options[0].OptionId)}, nil
}

// serveACP runs the command's server on pipes and returns a client of it,
// initialized.
func serveACP(t *testing.T, store auth.CredentialStore, site string, caps acpsdk.ClientCapabilities) (*acpsdk.ClientSideConnection, *acpClient, acpsdk.InitializeResponse) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- runACP(context.Background(), store, site, inR, outW, io.Discard) }()
	client := &acpClient{}
	conn := acpsdk.NewClientSideConnection(client, inW, outR)
	t.Cleanup(func() {
		if err := inW.Close(); err != nil {
			t.Error(err)
		}
		if err := <-served; err != nil {
			t.Error(err)
		}
		if err := outW.Close(); err != nil {
			t.Error(err)
		}
	})
	init, err := conn.Initialize(context.Background(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber, ClientCapabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	return conn, client, init
}

func TestACPLoginSignsInToTheSite(t *testing.T) {
	for name, terminal := range map[string]bool{"terminal auth": true, "no terminal auth": false} {
		t.Run(name, func(t *testing.T) {
			_, _, init := serveACP(t, nil, "datadoghq.eu", acpsdk.ClientCapabilities{Auth: acpsdk.AuthCapabilities{Terminal: terminal}})
			if len(init.AuthMethods) != 1 {
				t.Fatalf("auth methods = %+v", init.AuthMethods)
			}
			method := init.AuthMethods[0]
			meta := acpsdk.Meta(nil)
			switch {
			case terminal && method.Terminal != nil:
				// The editor appends these to the agent's own arguments,
				// which name the site.
				if want := []string{"--login"}; !slices.Equal(method.Terminal.Args, want) {
					t.Fatalf("terminal args = %q, want %q", method.Terminal.Args, want)
				}
				meta = method.Terminal.Meta
			case !terminal && method.Agent != nil:
				meta = method.Agent.Meta
			default:
				t.Fatalf("auth method = %+v", method)
			}
			// Zed runs this command unless its beta flag enables terminal
			// methods.
			var login struct{ Args []string }
			if err := meta.Get("terminal-auth", &login); err != nil {
				t.Fatal(err)
			}
			if want := []string{"login", "--site", "datadoghq.eu"}; !slices.Equal(login.Args, want) {
				t.Fatalf("login args = %q, want %q", login.Args, want)
			}
		})
	}
}

func TestACPServesSessionsAtTheirCwd(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	// Before the server, so its cleanup runs after the server stops:
	// Windows can't remove a directory the session still uses.
	dir := t.TempDir()
	conn, client, _ := serveACP(t, nil, auth.DefaultSite, acpsdk.ClientCapabilities{})
	// The file is only in the session's cwd, not the test's.
	if err := os.WriteFile(filepath.Join(dir, "only-here.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: dir, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := conn.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: session.SessionId, Prompt: []acpsdk.ContentBlock{
		acpsdk.TextBlock(`r = call("read_file", {"path": "only-here.txt"}); say("read " + r.status)`),
	}})
	if err != nil || response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("prompt = %+v, %v", response, err)
	}
	// The fake keeps every session's conversation, so another session
	// loads it.
	if _, err := conn.LoadSession(context.Background(), acpsdk.LoadSessionRequest{SessionId: session.SessionId, Cwd: dir, McpServers: []acpsdk.McpServer{}}); err != nil {
		t.Fatal(err)
	}
	if got := client.text.String(); got != "read successread success" {
		t.Fatalf("text = %q, want the turn, then its replay", got)
	}
}

func TestACPErrorAsksForLogin(t *testing.T) {
	for _, tc := range []struct {
		err   error
		login bool
		says  string // in the error
	}{
		{fmt.Errorf("refresh: %w", auth.ErrReauthRequired), true, ""},
		// Signing in again would replace the login once more.
		{fmt.Errorf("refresh: %w", auth.ErrSessionReplaced), false, "reopen the thread"},
		{auth.ErrNoSession, true, ""},
		{auth.ErrSessionCorrupt, true, ""},
		{errors.New("connection reset"), false, "connection reset"},
	} {
		err := acpError(tc.err)
		var request *acpsdk.RequestError
		if got := errors.As(err, &request) && request.Code == acpsdk.NewAuthRequired(nil).Code; got != tc.login || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("acpError(%v) = %v, want auth required %v, saying %q", tc.err, err, tc.login, tc.says)
		}
	}
}

func TestACPNewSessionAsksForLogin(t *testing.T) {
	clearStagingEnv(t)
	t.Setenv("BITS_FAKE_BACKEND", "")
	conn, _, _ := serveACP(t, &observedStore{err: auth.ErrNoSession}, auth.DefaultSite, acpsdk.ClientCapabilities{})
	_, err := conn.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	var request *acpsdk.RequestError
	if !errors.As(err, &request) || request.Code != acpsdk.NewAuthRequired(nil).Code {
		t.Fatalf("error = %v, want auth required", err)
	}
}

func TestACPSignInKeepsAStoredLogin(t *testing.T) {
	// As after a terminal login: no browser opens.
	store := &observedStore{session: auth.Session{Site: auth.DefaultSite}}
	if err := signIn(context.Background(), store, auth.DefaultSite, io.Discard); err != nil {
		t.Fatal(err)
	}
	if store.loads != 1 || store.saves != 0 {
		t.Fatalf("loads %d, saves %d", store.loads, store.saves)
	}
}
