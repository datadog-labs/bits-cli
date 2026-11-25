package acp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"image"
	imagepng "image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	// withdrawn receives each request whose context the agent cancelled:
	// the client learns it from $/cancel_request, in its own time.
	withdrawn chan acp.ToolCallId

	updated  chan struct{}                         // signaled after each update
	untagged int                                   // message chunks that name no message
	images   int                                   // replayed prompt images
	statuses map[acp.ToolCallId]acp.ToolCallStatus // each card's, as last sent
	// diffs and texts hold each card's diffs and text as last sent.
	diffs map[acp.ToolCallId][]*acp.ToolCallContentDiff
	texts map[acp.ToolCallId]string
	// decide answers a permission request; nil waits until it is withdrawn.
	decide func(acp.RequestPermissionRequest) *acp.PermissionOptionId
	form   func(acp.CreateElicitationRequest) acp.CreateElicitationResponse
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
	case u.ToolCall != nil:
		c.statuses[u.ToolCall.ToolCallId] = u.ToolCall.Status
		c.diff(u.ToolCall.ToolCallId, u.ToolCall.Content)
	case u.ToolCallUpdate != nil:
		if status, ok := u.ToolCallUpdate.Status.Value(); ok {
			c.statuses[u.ToolCallUpdate.ToolCallId] = status
		}
		if content, ok := u.ToolCallUpdate.Content.Value(); ok {
			c.diff(u.ToolCallUpdate.ToolCallId, content)
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

// awaitWithdrawn returns once a permission request is withdrawn; a request
// never withdrawn fails the test instead of hanging it.
func (c *testClient) awaitWithdrawn(t *testing.T) acp.ToolCallId {
	t.Helper()
	select {
	case id := <-c.withdrawn:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("no permission request was withdrawn")
		return ""
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

func (c *testClient) diff(id acp.ToolCallId, content []acp.ToolCallContent) {
	var diffs []*acp.ToolCallContentDiff
	var texts []string
	for _, item := range content {
		switch {
		case item.Diff != nil:
			diffs = append(diffs, item.Diff)
		case item.Content != nil && item.Content.Content.Text != nil:
			texts = append(texts, item.Content.Content.Text.Text)
		}
	}
	c.diffs[id], c.texts[id] = diffs, strings.Join(texts, "\n")
}

func (c *testClient) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	choice := c.decide(p)
	if choice == nil {
		<-ctx.Done()
		c.withdrawn <- p.ToolCall.ToolCallId
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(*choice)}, nil
}

func (c *testClient) CreateElicitation(ctx context.Context, p acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	return c.form(p), nil
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
	open := func(ctx context.Context, id, cwd string, ui *tools.UI) (Env, error) {
		ws, err := workspace.Open(cwd)
		if err != nil {
			return Env{}, err
		}
		commands := exectool.NewExecService(ws.DefaultShellPath())
		list := tools.NewClientTools(ws, commands)
		if ui != nil {
			list = append(list, tools.NewAskUserQuestionTool(ui))
		}
		set, err := agent.NewToolSet(list...)
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
	c := &testClient{statuses: map[acp.ToolCallId]acp.ToolCallStatus{}, diffs: map[acp.ToolCallId][]*acp.ToolCallContentDiff{}, texts: map[acp.ToolCallId]string{}, updated: make(chan struct{}, 1), withdrawn: make(chan acp.ToolCallId, 8)}
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

// forms is a client that renders form elicitations, as Zed does.
var forms = acp.ClientCapabilities{Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}}

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

func choose(id acp.PermissionOptionId) func(acp.RequestPermissionRequest) *acp.PermissionOptionId {
	return func(acp.RequestPermissionRequest) *acp.PermissionOptionId { return &id }
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
	open := func(ctx context.Context, id, cwd string, ui *tools.UI) (Env, error) {
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
	open := func(ctx context.Context, id, cwd string, ui *tools.UI) (Env, error) {
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
		"tool":      `say("before "); r = call("read_file", {"path": "go.mod"}); say("after: " + r.output.splitlines()[0])`,
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
			h.client.decide = choose(optionAllowOnce)
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

func TestPermissions(t *testing.T) {
	write := `r = call("write_file", {"path": "a.txt", "content": "x\n"}); say("status=" + r.status)`
	batch := `rs = call([("write_file", {"path": "a.txt", "content": "a\n"}), ("edit_file", {"path": "b.txt", "edits": [{"old_text": "b", "new_text": "B"}]})]); say(",".join([r.status for r in rs]))`
	// The grant answers a.txt only once b.txt was asked too: a grant first
	// would release b.txt before it was ever asked, so none is withdrawn.
	secondAsked := make(chan struct{})
	cases := []struct {
		name, script string
		decide       func(acp.RequestPermissionRequest) *acp.PermissionOptionId
		wantText     string
		wantA        string // a.txt afterwards, empty for absent
		withdrawn    int
	}{
		{"allow once", write, choose(optionAllowOnce), "status=success", "x\n", 0},
		{"reject", write, choose(optionReject), "status=error", "", 0},
		{"unknown option denies", write, choose("nonsense"), "status=error", "", 0},
		// The first answer grants workspace writes, which releases the
		// second call: its request is withdrawn.
		{"allow always withdraws covered requests", batch, func(p acp.RequestPermissionRequest) *acp.PermissionOptionId {
			if title, _ := p.ToolCall.Title.Value(); strings.Contains(title, "a.txt") {
				select {
				case <-secondAsked:
				case <-time.After(5 * time.Second):
					t.Error("b.txt was never asked")
				}
				id := optionAllowAlways
				return &id
			}
			close(secondAsked)
			return nil
		}, "success,success", "a\n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h := connect(t, &fake.Fake{}, dir, acp.ClientCapabilities{})
			h.client.decide = c.decide
			if reason := h.prompt(h.newSession().SessionId, c.script); reason != acp.StopReasonEndTurn {
				t.Fatalf("stop reason %q", reason)
			}
			if !strings.Contains(h.client.agentText(), c.wantText) {
				t.Fatalf("text = %q, want %q", h.client.agentText(), c.wantText)
			}
			got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if string(got) != c.wantA {
				t.Fatalf("a.txt = %q, want %q", got, c.wantA)
			}
			for range c.withdrawn {
				h.client.awaitWithdrawn(t)
			}
			if extra := len(h.client.withdrawn); extra > 0 {
				t.Fatalf("%d more requests withdrawn than %d", extra, c.withdrawn)
			}
		})
	}
}

func TestCancelWithdrawsThePermission(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{})
	id := h.newSession().SessionId
	h.client.decide = func(acp.RequestPermissionRequest) *acp.PermissionOptionId {
		// As Zed does: cancel the prompt while its permission is shown.
		if err := h.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: id}); err != nil {
			t.Error(err)
		}
		return nil
	}
	if reason := h.prompt(id, `call("write_file", {"path": "a.txt", "content": "x"}); say("after")`); reason != acp.StopReasonCancelled {
		t.Fatalf("stop reason %q", reason)
	}
	h.client.awaitWithdrawn(t)
	if reason := h.prompt(id, `say("next")`); reason != acp.StopReasonEndTurn {
		t.Fatalf("next prompt: stop reason %q", reason)
	}
}

func TestQuestionsAskAForm(t *testing.T) {
	str := acp.NewElicitationContentValueString
	cases := []struct {
		name     string
		response acp.CreateElicitationResponse
		want     []string
	}{
		{"choices", acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: map[string]acp.ElicitationContentValue{
			"q0": str("EU"), "q1": str("API"),
		}}}, []string{"A: EU", "A: API"}},
		{"other answer wins", acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: map[string]acp.ElicitationContentValue{
			"q0": str("EU"), "q1": str("API"), "q1_other": str("Billing"),
		}}}, []string{"A: EU", "A: Billing"}},
		{"missing answer dismisses", acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: map[string]acp.ElicitationContentValue{
			"q0": str("EU"),
		}}}, []string{"without answers"}},
		{"decline dismisses", acp.CreateElicitationResponse{Decline: &acp.CreateElicitationDecline{Action: "decline"}}, []string{"without answers"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := connect(t, &fake.Fake{BundledFixtures: true}, t.TempDir(), forms)
			h.client.form = func(acp.CreateElicitationRequest) acp.CreateElicitationResponse { return c.response }
			if reason := h.prompt(h.newSession().SessionId, `load("questions.star", "ask_user_question"); ask_user_question()`); reason != acp.StopReasonEndTurn {
				t.Fatalf("stop reason %q", reason)
			}
			for _, want := range c.want {
				if !strings.Contains(h.client.agentText(), want) {
					t.Fatalf("text = %q, want %q", h.client.agentText(), want)
				}
			}
		})
	}
}

func TestQuestionBesideAPermission(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), forms)
	asked := make(chan struct{}, 1)
	h.client.decide = func(acp.RequestPermissionRequest) *acp.PermissionOptionId {
		asked <- struct{}{}
		return acp.Ptr(optionAllowOnce)
	}
	// Both are shown at once, as Zed shows them: the form must not hold
	// back the permission request of the call beside it.
	h.client.form = func(acp.CreateElicitationRequest) acp.CreateElicitationResponse {
		select {
		case <-asked:
		case <-time.After(5 * time.Second):
			t.Error("no permission request while the form was open")
		}
		return acp.CreateElicitationResponse{Decline: &acp.CreateElicitationDecline{Action: "decline"}}
	}
	h.prompt(h.newSession().SessionId, `call([("ask_user_question", {"questions": [{"question": "Q?", "options": [{"label": "A", "description": "a"}, {"label": "B", "description": "b"}]}]}), ("write_file", {"path": "a.txt", "content": "x"})])`)
}

// A client without forms gets no questions tool, so the model cannot wait
// on an answer nobody can give.
func TestQuestionsNeedForms(t *testing.T) {
	h := connect(t, &fake.Fake{BundledFixtures: true}, t.TempDir(), acp.ClientCapabilities{})
	h.prompt(h.newSession().SessionId, `load("questions.star", "ask_user_question"); ask_user_question()`)
	if !strings.Contains(h.client.agentText(), "without answers") {
		t.Fatalf("text = %q", h.client.agentText())
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

func TestLoadSettlesUnansweredCalls(t *testing.T) {
	backend := &recorder{Fake: &fake.Fake{}}
	h := connect(t, backend, t.TempDir(), acp.ClientCapabilities{})
	id := h.newSession().SessionId
	// The prompt is cancelled while its call awaits approval, which leaves
	// the call unanswered in history.
	h.client.decide = func(acp.RequestPermissionRequest) *acp.PermissionOptionId {
		if err := h.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: id}); err != nil {
			t.Error(err)
		}
		return nil
	}
	if reason := h.prompt(id, `call("write_file", {"path": "a.txt", "content": "x"})`); reason != acp.StopReasonCancelled {
		t.Fatalf("stop reason %q", reason)
	}
	replay := connect(t, backend, h.dir, acp.ClientCapabilities{})
	replay.load(id)
	replay.client.mu.Lock()
	defer replay.client.mu.Unlock()
	if len(replay.client.statuses) == 0 {
		t.Fatal("no card replayed")
	}
	for call, status := range replay.client.statuses {
		if status != acp.ToolCallStatusFailed && status != acp.ToolCallStatusCompleted {
			t.Fatalf("card %s loaded %s, want it settled", call, status)
		}
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

func TestConfigOptionsApplyToLaterPrompts(t *testing.T) {
	backend := &recorder{Fake: &fake.Fake{}}
	h := connect(t, backend, t.TempDir(), acp.ClientCapabilities{})
	session := h.newSession()
	current := func(options []acp.SessionConfigOption, id acp.SessionConfigId) acp.SessionConfigValueId {
		i := slices.IndexFunc(options, func(o acp.SessionConfigOption) bool { return o.Select.Id == id })
		return options[i].Select.CurrentValue
	}
	if got := current(session.ConfigOptions, configReasoning); got != "default" {
		t.Fatalf("reasoning = %q", got)
	}
	for _, set := range []struct {
		id    acp.SessionConfigId
		value acp.SessionConfigValueId
	}{{configReasoning, "deep"}, {configPermissions, acp.SessionConfigValueId(agent.ModeSkipPermissions)}} {
		r, err := h.conn.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: session.SessionId, ConfigId: set.id, Value: set.value,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got := current(r.ConfigOptions, set.id); got != set.value {
			t.Fatalf("%s = %q, want %q", set.id, got, set.value)
		}
	}
	// Full access runs the write without asking.
	h.prompt(session.SessionId, `r = call("write_file", {"path": "a.txt", "content": "x"}); say(r.status)`)
	if got := h.client.agentText(); got != "success" {
		t.Fatalf("text = %q", got)
	}
	if got := backend.sent[0].InferenceMode; got != "deep" {
		t.Fatalf("inference mode = %q", got)
	}
	if _, err := h.conn.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: session.SessionId, ConfigId: configReasoning, Value: "turbo",
	}}); err == nil {
		t.Fatal("accepted an unknown value")
	}
}

func TestEditsShowTheirDiff(t *testing.T) {
	const before = "one\ntwo\nthree\n"
	for name, tc := range map[string]struct {
		script   string
		old      *string
		new      string
		filename string
		file     string // a.txt before, if not before
	}{
		"edit":      {`call("edit_file", {"path": "a.txt", "edits": [{"old_text": "two", "new_text": "2"}]})`, acp.Ptr(before), "one\n2\nthree\n", "a.txt", ""},
		"overwrite": {`call("write_file", {"path": "a.txt", "content": "new\n"})`, acp.Ptr(before), "new\n", "a.txt", ""},
		"create":    {`call("write_file", {"path": "b.txt", "content": "new\n"})`, nil, "new\n", "b.txt", ""},
		// The tool refuses mixed line endings, so the card shows only the
		// replacement it was asked for.
		"unappliable edit": {`call("edit_file", {"path": "a.txt", "edits": [{"old_text": "two", "new_text": "2"}]})`, acp.Ptr("two"), "2", "a.txt", "one\r\ntwo\nthree\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(cmp.Or(tc.file, before)), 0o600); err != nil {
				t.Fatal(err)
			}
			h := connect(t, &fake.Fake{}, dir, acp.ClientCapabilities{})
			h.client.decide = choose(optionAllowOnce)
			if reason := h.prompt(h.newSession().SessionId, tc.script); reason != acp.StopReasonEndTurn {
				t.Fatalf("stop reason %q", reason)
			}
			h.client.mu.Lock()
			defer h.client.mu.Unlock()
			if len(h.client.diffs) != 1 {
				t.Fatalf("diffs of %d cards", len(h.client.diffs))
			}
			for _, diffs := range h.client.diffs {
				if len(diffs) != 1 {
					t.Fatalf("%d diffs on the card", len(diffs))
				}
				d := diffs[0]
				if d.Path != filepath.Join(dir, tc.filename) || d.NewText != tc.new || !reflect.DeepEqual(d.OldText, tc.old) {
					t.Fatalf("diff %s: %q -> %q", d.Path, ptrText(d.OldText), d.NewText)
				}
			}
		})
	}
}

func TestFailedEditSaysWhy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := connect(t, &fake.Fake{}, dir, acp.ClientCapabilities{})
	h.client.decide = choose(optionAllowOnce)
	h.prompt(h.newSession().SessionId, `call("edit_file", {"path": "a.txt", "edits": [{"old_text": "missing", "new_text": "x"}]})`)
	h.client.mu.Lock()
	defer h.client.mu.Unlock()
	for id, text := range h.client.texts {
		if !strings.Contains(text, "missing") && !strings.Contains(text, "not found") {
			t.Fatalf("card %s says %q", id, text)
		}
		return
	}
	t.Fatal("no card")
}

func ptrText(s *string) string {
	if s == nil {
		return "<none>"
	}
	return *s
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
