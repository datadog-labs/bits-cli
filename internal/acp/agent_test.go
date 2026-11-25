package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	imagepng "image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// testClient is an editor: it records what the agent tells it, in order.
type testClient struct {
	acp.Client // unimplemented methods panic

	mu      sync.Mutex
	text    strings.Builder
	thought strings.Builder
	user    strings.Builder
	title   string

	updated  chan struct{} // signaled after each update
	untagged int           // message chunks that name no message
	images   int           // replayed prompt images
}

func (c *testClient) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		select {
		case c.updated <- struct{}{}:
		default:
		}
	}()
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		c.text.WriteString(u.AgentMessageChunk.Content.Text.Text)
		c.tag(u.AgentMessageChunk.MessageId)
	case u.AgentThoughtChunk != nil:
		c.thought.WriteString(u.AgentThoughtChunk.Content.Text.Text)
		c.tag(u.AgentThoughtChunk.MessageId)
	case u.UserMessageChunk != nil:
		c.tag(u.UserMessageChunk.MessageId)
		if u.UserMessageChunk.Content.Image != nil {
			c.images++
		} else {
			c.user.WriteString(u.UserMessageChunk.Content.Text.Text)
		}
	case u.SessionInfoUpdate != nil:
		c.title, _ = u.SessionInfoUpdate.Title.Value()
	}
	return nil
}

// awaitText returns the agent's text once it is want: a background task
// streams after the prompt that started it returns.
func (c *testClient) awaitText(t *testing.T, want string) string {
	t.Helper()
	for {
		got := c.agentText()
		if got == want {
			return got
		}
		select {
		case <-c.updated:
		case <-time.After(5 * time.Second):
			t.Fatalf("text %q never became %q", got, want)
		}
	}
}

func (c *testClient) agentText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.String()
}

func (c *testClient) tag(id *acp.MessageId) {
	if id == nil {
		c.untagged++
	}
}

type harness struct {
	t      *testing.T
	client *testClient
	conn   *acp.ClientSideConnection
	dir    string
}

// connect serves a new Agent over pipes against backend, as `bits acp` does
// on stdio, with sessions built as the command builds them.
func connect(t *testing.T, backend agent.Backend, dir string, caps acp.ClientCapabilities) *harness {
	t.Helper()
	return connectWith(t, backend, dir, caps, Config{})
}

// connectWith serves an agent whose config adds to base.
func connectWith(t *testing.T, backend agent.Backend, dir string, caps acp.ClientCapabilities, base Config) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	open := func(ctx context.Context, id, cwd string) (Env, error) {
		ws, err := workspace.Open(cwd)
		if err != nil {
			return Env{}, err
		}
		commands := exectool.NewExecService(ws.DefaultShellPath())
		set, err := agent.NewToolSet(tools.NewClientTools(ws, commands)...)
		if err != nil {
			return Env{}, err
		}
		engine := agent.New(backend, agent.Config{ConversationID: id})
		return Env{Engine: engine, Tools: set, Close: func() {
			commands.Close()
			if err := ws.Close(); err != nil {
				t.Error(err)
			}
		}}, nil
	}
	base.Open, base.Log, base.Version = open, log, "test"
	a := NewAgent(base)
	c := &testClient{updated: make(chan struct{}, 1)}
	a2cR, a2cW := io.Pipe()
	c2aR, c2aW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- a.Serve(context.Background(), a2cW, c2aR) }()
	cc := acp.NewClientSideConnection(c, c2aW, a2cR)
	cc.SetLogger(log)
	t.Cleanup(func() {
		// Closing the client's side ends the connection, which closes
		// every session.
		if err := c2aW.Close(); err != nil {
			t.Error(err)
		}
		if err := <-served; err != nil {
			t.Error(err)
		}
		if err := a2cW.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := cc.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientCapabilities: caps}); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, client: c, conn: cc, dir: dir}
}

// load restores session id in the harness's dir.
func (h *harness) load(id acp.SessionId) {
	h.t.Helper()
	if _, err := h.conn.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: id, Cwd: h.dir, McpServers: []acp.McpServer{}}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) newSession() acp.NewSessionResponse {
	h.t.Helper()
	r, err := h.conn.NewSession(context.Background(), acp.NewSessionRequest{Cwd: h.dir, McpServers: []acp.McpServer{}})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) prompt(id acp.SessionId, text string) acp.StopReason {
	h.t.Helper()
	r, err := h.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	if err != nil {
		h.t.Fatal(err)
	}
	return r.StopReason
}

// recorder keeps the history of every conversation, so a session loads what
// another one prompted, and records the options of each request.
type recorder struct {
	*fake.Fake
	mu   sync.Mutex
	sent []assistant.SendOptions
}

func (r *recorder) Send(ctx context.Context, message any, opts assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	r.mu.Lock()
	r.sent = append(r.sent, opts)
	r.mu.Unlock()
	return r.Fake.Send(ctx, message, opts, fn)
}

func TestSessionOpenedAfterCloseIsClosed(t *testing.T) {
	opening, release := make(chan struct{}), make(chan struct{})
	closed := make(chan struct{}, 1)
	open := func(ctx context.Context, id, cwd string) (Env, error) {
		close(opening)
		<-release
		return Env{Engine: agent.New(&fake.Fake{}, agent.Config{ConversationID: id}), Close: func() { closed <- struct{}{} }}, nil
	}
	a := NewAgent(Config{Open: open, Log: slog.New(slog.DiscardHandler)})
	started := make(chan error, 1)
	go func() {
		_, err := a.start(context.Background(), "00000000-0000-4000-8000-000000000001", t.TempDir())
		started <- err
	}()
	<-opening
	a.Close() // the connection ended while the session was opening
	close(release)
	if err := <-started; err == nil {
		t.Fatal("opened a session after the agent closed")
	}
	select {
	case <-closed:
	default:
		t.Fatal("the late session was not closed")
	}
}

func TestReopeningASessionClosesTheOpenOne(t *testing.T) {
	closed := make(map[int]int) // environment -> closes
	opened := 0
	open := func(ctx context.Context, id, cwd string) (Env, error) {
		env := opened
		opened++
		return Env{Engine: agent.New(&fake.Fake{}, agent.Config{ConversationID: id}), Close: func() { closed[env]++ }}, nil
	}
	a := NewAgent(Config{Open: open, Log: slog.New(slog.DiscardHandler)})
	const id = "00000000-0000-4000-8000-000000000002"
	// An editor loads a thread it already has open, as on reconnecting.
	for range 2 {
		if _, err := a.start(context.Background(), id, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	if closed[0] != 1 || closed[1] != 0 {
		t.Fatalf("closes = %v, want the first session closed once", closed)
	}
	a.Close()
	if closed[1] != 1 {
		t.Fatalf("closes = %v, want the second closed with the agent", closed)
	}
}

func TestPromptStreamsTheTranscript(t *testing.T) {
	for name, script := range map[string]string{
		"text":      `think("let me think"); say("hello "); say("world")`,
		"long":      `think("a" * 3000); say(random(seed=1, size=20000))`,
		"multibyte": `say("héllo " * 500); think("日本語" * 300)`,
		"task":      `task("Investigate", ["Findings so far"], every="0s"); say("started")`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			backend := &recorder{Fake: &fake.Fake{}}
			h := connect(t, backend, dir, acp.ClientCapabilities{})
			id := h.newSession().SessionId
			if reason := h.prompt(id, script); reason != acp.StopReasonEndTurn {
				t.Fatalf("stop reason %q", reason)
			}
			// Load it back: the replay is the transcript the engine folded.
			replay := connect(t, backend, dir, acp.ClientCapabilities{})
			replay.load(id)
			want := replay.client.agentText()
			got := h.client.agentText()
			if name == "task" {
				if !strings.Contains(want, "Findings so far") {
					t.Fatalf("task output missing from %q", want)
				}
				got = h.client.awaitText(t, want)
			}
			if got != want || got == "" {
				t.Fatalf("streamed text %q, history %q", got, want)
			}
			if got, want := h.client.thought.String(), replay.client.thought.String(); got != want {
				t.Fatalf("streamed %d bytes of thought, history %d", len(got), len(want))
			}
			if got := replay.client.user.String(); got != script {
				t.Fatalf("replayed prompt %q", got)
			}
			if h.client.untagged+replay.client.untagged != 0 {
				t.Fatalf("%d chunks name no message", h.client.untagged+replay.client.untagged)
			}
			if h.client.user.String() != "" {
				t.Fatalf("live prompt echoed: %q", h.client.user.String())
			}
			if h.client.title == "" || h.client.title != replay.client.title {
				t.Fatalf("title %q, loaded %q", h.client.title, replay.client.title)
			}
		})
	}
}

func TestPromptErrorsGoThroughConfig(t *testing.T) {
	// As a login revoked mid-session: the command makes it auth_required.
	authRequired := func(err error) error { return acp.NewAuthRequired(map[string]any{"message": err.Error()}) }
	h := connectWith(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{}, Config{Error: authRequired})
	_, err := h.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: h.newSession().SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(`fail("net")`)}})
	var request *acp.RequestError
	if !errors.As(err, &request) || request.Code != acp.NewAuthRequired(nil).Code {
		t.Fatalf("prompt error = %v, want auth required", err)
	}
}

func TestAuthenticateSignsIn(t *testing.T) {
	for name, tc := range map[string]struct {
		method acp.AuthMethodId
		signIn error
		want   string // in the error, if any
		calls  int
	}{
		"signed in":      {loginMethod, nil, "", 1},
		"sign-in failed": {loginMethod, errors.New("browser closed"), "browser closed", 1},
		"unknown method": {"other", nil, "Invalid params", 0},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			signIn := func(context.Context) error { calls++; return tc.signIn }
			h := connectWith(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{}, Config{SignIn: signIn})
			init, err := h.conn.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
			if err != nil {
				t.Fatal(err)
			}
			// A client with no terminal still gets a method to call.
			if len(init.AuthMethods) != 1 || init.AuthMethods[0].Agent == nil {
				t.Fatalf("auth methods = %+v", init.AuthMethods)
			}
			_, err = h.conn.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: tc.method})
			got := ""
			if err != nil {
				got = err.Error()
			}
			if calls != tc.calls || (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("authenticate = %q after %d sign-ins, want %q after %d", got, calls, tc.want, tc.calls)
			}
		})
	}
}

func TestCloseSessionIsIdempotent(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{})
	id := h.newSession().SessionId
	for range 2 {
		if _, err := h.conn.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: id}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadUnpromptedSessionIsEmpty(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{})
	id := acp.SessionId(uuid.NewV4().String())
	h.load(id)
	if reason := h.prompt(id, `say("first")`); reason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason %q", reason)
	}
}

func TestPromptJoinsTextAroundMentions(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{})
	// The fake runs the message as a script, so a stray separator breaks it.
	r, err := h.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: h.newSession().SessionId, Prompt: []acp.ContentBlock{
		acp.TextBlock(`say('''see `), acp.ResourceLinkBlock("a.go", "file:///a.go"), acp.TextBlock(` now''')`),
	}})
	if err != nil || r.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("prompt: %v %v", r.StopReason, err)
	}
	if got := h.client.agentText(); !strings.HasPrefix(got, "see <resource_link") || !strings.HasSuffix(got, "/> now") {
		t.Fatalf("text %q", got)
	}
}

func TestPromptImages(t *testing.T) {
	var png bytes.Buffer
	if err := imagepng.Encode(&png, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	backend := &recorder{Fake: &fake.Fake{}}
	h := connect(t, backend, t.TempDir(), acp.ClientCapabilities{})
	id := h.newSession().SessionId
	send := func(data []byte) error {
		_, err := h.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{
			acp.TextBlock(`say("seen")`), acp.ImageBlock(base64.StdEncoding.EncodeToString(data), "image/png"),
		}})
		return err
	}
	if err := send([]byte("not an image")); err == nil {
		t.Fatal("an invalid image was sent")
	}
	if err := send(png.Bytes()); err != nil {
		t.Fatal(err)
	}
	replay := connect(t, backend, h.dir, acp.ClientCapabilities{})
	replay.load(id)
	if replay.client.images != 1 {
		t.Fatalf("replayed %d images", replay.client.images)
	}
}
