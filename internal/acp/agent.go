// Package acp serves the engine over the Agent Client Protocol, so editors
// embed bits as an agent on stdio. Each session is one engine at the
// session's cwd, and the session id is the conversation id.
package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"uuid"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
	"github.com/datadog-labs/bits-cli/internal/tools"
)

// Open builds the environment of a session at cwd bound to conversationID.
// ui is nil when the client cannot answer forms; the questions tool is then
// not registered.
type Open func(ctx context.Context, conversationID, cwd string, ui *tools.UI) (Env, error)

// Config is what an Agent needs from its command.
type Config struct {
	Open    Open
	Log     *slog.Logger
	Version string
	// Login is the command line that signs in, for clients that run a
	// terminal login from an explicit command.
	Login []string
	// LoginArgs, appended to the agent's own command line, sign in instead
	// of serving the protocol.
	LoginArgs []string
	// SignIn signs in unless already signed in, for a client that asks the
	// agent to.
	SignIn func(context.Context) error
	// Error, if set, translates an error opening a session or running a
	// prompt for the client: one that needs a login becomes
	// acp.NewAuthRequired, which makes the client offer to sign in.
	Error func(error) error
}

// Agent is the acp.Agent of one connection.
type Agent struct {
	config Config

	// mu guards the fields below: the SDK runs requests concurrently.
	// Sessions do their own work on their pump, never under mu.
	mu sync.Mutex
	// conn is set under mu before any request can read it: the connection
	// reads requests as soon as it exists.
	conn  *acp.AgentSideConnection
	forms bool // the client renders form elicitations
	// sessions is nil once the connection ended: a session still opening
	// then closes itself.
	sessions map[acp.SessionId]*session
}

var (
	_ acp.Agent       = (*Agent)(nil)
	_ acp.AgentLoader = (*Agent)(nil)
)

func NewAgent(config Config) *Agent {
	return &Agent{config: config, sessions: make(map[acp.SessionId]*session)}
}

// Serve answers the client on in and out until the connection or ctx ends,
// then closes every session.
func (a *Agent) Serve(ctx context.Context, out io.Writer, in io.Reader) error {
	a.mu.Lock()
	conn := acp.NewAgentSideConnection(a, out, in)
	conn.SetLogger(a.config.Log)
	a.conn = conn
	a.mu.Unlock()
	defer a.Close()
	select {
	case <-conn.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const loginMethod acp.AuthMethodId = "login"

func (a *Agent) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	caps := params.ClientCapabilities
	a.mu.Lock()
	a.forms = caps.Elicitation != nil && caps.Elicitation.Form != nil
	a.mu.Unlock()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "bits", Title: acp.Ptr("Bits"), Version: a.config.Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			SessionCapabilities: acp.SessionCapabilities{
				Resume: &acp.SessionResumeCapabilities{}, Close: &acp.SessionCloseCapabilities{},
			},
		},
		AuthMethods: a.authMethods(caps.Auth.Terminal),
	}, nil
}

// authMethods offers the terminal login. Both forms carry the earlier _meta
// form, which names the whole command: Zed runs a terminal method only
// behind a beta flag, and otherwise runs that command.
func (a *Agent) authMethods(terminal bool) []acp.AuthMethod {
	const name = "Log in to Datadog"
	var meta acp.Meta
	if len(a.config.Login) > 0 {
		login, err := json.Marshal(map[string]any{"label": "bits login", "command": a.config.Login[0], "args": a.config.Login[1:]})
		if err != nil {
			a.config.Log.Error("auth method", "err", err)
		} else {
			meta = acp.Meta{"terminal-auth": login}
		}
	}
	if terminal {
		return []acp.AuthMethod{{Terminal: &acp.AuthMethodTerminalInline{Type: "terminal", Id: loginMethod, Name: name, Args: a.config.LoginArgs, Meta: meta}}}
	}
	if meta == nil && a.config.SignIn == nil {
		return []acp.AuthMethod{}
	}
	return []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{Id: loginMethod, Name: name, Meta: meta}}}
}

// Authenticate signs in, which does nothing once a terminal login the
// client ran stored the credentials, and otherwise signs in from here.
func (a *Agent) Authenticate(ctx context.Context, params acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if params.MethodId != loginMethod {
		return acp.AuthenticateResponse{}, acp.NewInvalidParams(map[string]any{"methodId": params.MethodId})
	}
	if a.config.SignIn == nil {
		return acp.AuthenticateResponse{}, nil
	}
	return acp.AuthenticateResponse{}, a.config.SignIn(ctx)
}

func (a *Agent) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// NewSession opens a new conversation under a new id, as the engine mints
// one: the Assistant API starts a conversation under an id it has not seen,
// so the session id an editor keeps loads the conversation in a later
// process. NOTE: the client's MCP servers are ignored; bits has no MCP
// client.
func (a *Agent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	s, err := a.start(ctx, acp.SessionId(uuid.NewV4().String()), params.Cwd)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: s.id, ConfigOptions: configOptions(s.env.Engine)}, nil
}

// LoadSession restores the conversation and replays it as updates before
// answering, as the protocol asks.
// NOTE: tool calls history left unanswered are not resumed: they are shown
// cancelled, as the TUI shows calls it cannot resume.
func (a *Agent) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	s, err := a.start(ctx, params.SessionId, params.Cwd)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	run, err := s.env.Engine.Restore(ctx)
	if err == nil {
		err = s.wait(run).Err
	}
	// A session never prompted has no history on the server: it loads empty.
	if err != nil && !notFound(err) {
		a.drop(s)
		return acp.LoadSessionResponse{}, a.clientError(err)
	}
	s.env.Engine.SettleRestoredTools(nil)
	s.synced(s.env.Engine.State().Version)
	return acp.LoadSessionResponse{ConfigOptions: configOptions(s.env.Engine)}, nil
}

// ResumeSession binds a session to its conversation without replaying it:
// the server holds the history the next prompt continues.
// NOTE: background tasks already running in the conversation are not
// followed: only a restore finds them, and its history would replay.
func (a *Agent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	s, err := a.start(ctx, params.SessionId, params.Cwd)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{ConfigOptions: configOptions(s.env.Engine)}, nil
}

// notFound is a conversation the server never saw.
func notFound(err error) bool {
	var api *assistant.APIError
	return errors.As(err, &api) && api.StatusCode == http.StatusNotFound
}

// start opens a session, replacing one the client opened before under the
// same id.
func (a *Agent) start(ctx context.Context, id acp.SessionId, cwd string) (*session, error) {
	if !agent.ValidConversationID(string(id)) {
		return nil, acp.NewInvalidParams(map[string]any{"sessionId": id, "error": "not a conversation id"})
	}
	a.mu.Lock()
	conn, forms := a.conn, a.forms
	a.mu.Unlock()
	var ui *tools.UI
	if forms {
		ui = tools.NewUI()
	}
	env, err := a.config.Open(ctx, string(id), cwd, ui)
	if err != nil {
		return nil, a.clientError(err)
	}
	s := newSession(id, conn, env, ui, cwd, a.config.Log)
	a.mu.Lock()
	closed := a.sessions == nil
	old := a.sessions[id]
	if !closed {
		a.sessions[id] = s
	}
	a.mu.Unlock()
	if closed {
		s.close()
		return nil, errors.New("connection closed")
	}
	if old != nil {
		old.close()
	}
	return s, nil
}

func (a *Agent) session(id acp.SessionId) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		return s, nil
	}
	return nil, acp.NewRequestError(-32002, "Resource not found", map[string]any{"sessionId": id})
}

// drop closes s unless another request already took it out of the table:
// that request closes it.
func (a *Agent) drop(s *session) {
	a.mu.Lock()
	owned := a.sessions[s.id] == s
	if owned {
		delete(a.sessions, s.id)
	}
	a.mu.Unlock()
	if owned {
		s.close()
	}
}

func (a *Agent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	s, err := a.session(params.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	in, err := turnInput(params.Prompt)
	if err != nil {
		return acp.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	in.Tools, in.OnDeny = s.env.Tools, agent.DenyContinue
	// The SDK cancels ctx on session/cancel, which cancels the turn.
	reason, err := s.prompt(ctx, in)
	return acp.PromptResponse{StopReason: reason}, a.clientError(err)
}

func (a *Agent) clientError(err error) error {
	if err == nil || a.config.Error == nil {
		return err
	}
	return a.config.Error(err)
}

// Cancel has nothing to do: the SDK cancels the prompt's context, which is
// the turn's. It must not block either: the connection waits for it.
func (a *Agent) Cancel(ctx context.Context, params acp.CancelNotification) error { return nil }

// CloseSession is idempotent, as the protocol asks: a session the agent
// does not have is already closed.
func (a *Agent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if s, err := a.session(params.SessionId); err == nil {
		a.drop(s)
	}
	return acp.CloseSessionResponse{}, nil
}

// NOTE: the server stores no cwd per conversation, so sessions cannot be
// listed by cwd.
func (a *Agent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *Agent) DeleteSession(ctx context.Context, params acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	return acp.DeleteSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionDelete)
}

// SetSessionMode is unused: the permissions mode is a config option, which
// clients prefer to modes.
func (a *Agent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// Close ends every session, for the connection's end.
func (a *Agent) Close() {
	a.mu.Lock()
	sessions := a.sessions
	a.sessions = nil
	a.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

// turnInput maps prompt content: text is the message, images are attached
// as data URLs, and resources are quoted into the message. NOTE: a quoted
// resource is history's text, so a loaded session replays it whole.
func turnInput(blocks []acp.ContentBlock) (agent.TurnInput, error) {
	var text []string
	var context assistant.AssistantContext
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			text = append(text, block.Text.Text)
		case block.Image != nil:
			data, err := base64.StdEncoding.DecodeString(block.Image.Data)
			if err != nil {
				return agent.TurnInput{}, fmt.Errorf("image: %w", err)
			}
			// The editor's bytes are checked, converted and resized as a
			// dropped file is. NOTE: the engine tells the model only about
			// files it resized, so this resize goes unmentioned.
			img, err := imageinput.Prepare("image", data, imageinput.DefaultLimits)
			if err != nil {
				return agent.TurnInput{}, err
			}
			context.Entities = append(context.Entities, assistant.ImageEntity(string(img.MediaType), img.Data, ""))
		case block.ResourceLink != nil:
			text = append(text, fmt.Sprintf("<resource_link name=%q uri=%q />", block.ResourceLink.Name, block.ResourceLink.Uri))
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			r := block.Resource.Resource.TextResourceContents
			text = append(text, fmt.Sprintf("<resource uri=%q>\n%s\n</resource>", r.Uri, r.Text))
		default:
			return agent.TurnInput{}, errors.New("unsupported content block")
		}
	}
	// Editors split text around a mention, spaces included.
	in := agent.TurnInput{Message: strings.Join(text, "")}
	if len(context.Entities) > 0 {
		in.Context = &context
	}
	return in, nil
}
