// Package assistant is a bare-bones client for the Datadog Bits AI (CMD-I)
// Assistant HTTP API. It targets the same API the Datadog UI uses when you
// chat with Bits, and is intended to drive a remote agent loop.
//
// The main endpoint (POST /api/v2/assistant) returns a stream of
// newline-delimited JSON, where each line is a complete AssistantResponse.
// See scripts/explore.sh for the raw protocol exploration this was built from.
package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the Datadog staging site (org 2). dd-auth --domain
// dd.datad0g.com produces credentials valid here.
const DefaultBaseURL = "https://dd.datad0g.com"

// Client talks to the Bits AI assistant API over HTTP.
type Client struct {
	BaseURL    string
	APIKey     string
	AppKey     string
	HTTPClient *http.Client
}

// NewClient builds a Client from DD_API_KEY / DD_APP_KEY in the environment
// (as populated by dd-auth). BaseURL defaults to staging.
func NewClient() (*Client, error) {
	apiKey := os.Getenv("DD_API_KEY")
	appKey := os.Getenv("DD_APP_KEY")
	if apiKey == "" || appKey == "" {
		return nil, fmt.Errorf("DD_API_KEY and DD_APP_KEY must be set (run under: dd-auth --domain dd.datad0g.com -- ...)")
	}
	base := os.Getenv("DD_SITE_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(base, "/"),
		APIKey:     apiKey,
		AppKey:     appKey,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", c.APIKey)
	req.Header.Set("DD-APPLICATION-KEY", c.AppKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

// SendOptions configures a single call to Send.
type SendOptions struct {
	// ConversationID resumes an existing conversation. Empty starts a new one;
	// the server-generated id is available via the streamed responses.
	ConversationID string
	// Model optionally overrides the model (e.g. "claude-sonnet-4-6").
	Model string
	// Referrer is the Datadog page URL the user was on, used for context.
	Referrer string
	// ClientTools are client-side tools the caller can execute. Required to
	// participate in the client-tool / approval flow. They must be resent on
	// every request in the conversation, including tool-result follow-ups.
	ClientTools []ClientTool
	// SkillOverrides flip per-skill default-enabled state for the request
	// (see Client.ListSkills). Like ClientTools, they must be resent on every
	// request in the conversation. RunTools carries them across turns.
	SkillOverrides []SkillOverride
	// Context puts Datadog objects (dashboards, monitors, services, …) in
	// scope for the turn; the server fetches their details into the prompt.
	// Like ClientTools, it must be resent on every request in the
	// conversation. RunTools carries it across turns.
	Context *AssistantContext
	// Profile selects the server-side surface preset (see the Profile*
	// constants) that controls the system prompt's surface section, tool
	// availability, and conversation namespace. Empty uses DefaultProfile.
	// An unknown profile is rejected by the server with HTTP 400.
	Profile string
	// MaxTurns caps the RunTools agent loop. Zero uses DefaultMaxTurns.
	// Ignored by Send.
	MaxTurns int
}

// Send posts a user message and invokes fn for every streamed AssistantResponse
// line until the stream ends. It returns the conversation id observed in the
// stream (useful when the server generated a new one).
func (c *Client) Send(ctx context.Context, message any, opts SendOptions, fn func(AssistantResponse) error) (string, error) {
	profile := opts.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	attrs := RequestAttributes{
		Message:        message,
		ConversationID: opts.ConversationID,
		Model:          opts.Model,
		Referrer:       opts.Referrer,
		ClientTools:    opts.ClientTools,
		SkillOverrides: opts.SkillOverrides,
		Context:        opts.Context,
		Profile:        profile,
	}
	reqBody := Request{Data: RequestData{
		Type:       "assistant-request",
		ID:         "assistant-request",
		Attributes: attrs,
	}}

	resp, err := c.do(ctx, http.MethodPost, "/api/v2/assistant", reqBody)
	if err != nil {
		return opts.ConversationID, err
	}
	defer func() { _ = resp.Body.Close() }()

	conversationID := opts.ConversationID
	scanner := bufio.NewScanner(resp.Body)
	// Individual lines can be large (tool outputs), so grow the buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ar AssistantResponse
		if err := json.Unmarshal(line, &ar); err != nil {
			return conversationID, fmt.Errorf("decode stream line: %w: %s", err, line)
		}
		if id := ar.Data.Attributes.ConversationID; id != "" {
			conversationID = id
		}
		if fn != nil {
			if err := fn(ar); err != nil {
				return conversationID, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return conversationID, fmt.Errorf("read stream: %w", err)
	}
	return conversationID, nil
}

// ToolExecutor runs a client tool given its arguments as the raw JSON string
// the model produced (metadata.input), and returns the tool output that is
// echoed back to the model. A non-nil error is reported to the model as a
// failed tool result rather than aborting the loop.
type ToolExecutor func(ctx context.Context, input string) (output string, err error)

// Tool bundles a client-side tool definition with the function that executes
// it locally.
type Tool struct {
	ClientTool
	Run ToolExecutor
}

// DefaultMaxTurns caps RunTools to avoid an unbounded remote agent loop.
const DefaultMaxTurns = 20

// RunTools drives a full remote agent loop. It sends message, then for each
// turn executes any client_tool_call locally with the matching Tool, posts the
// results back (resending the tool definitions, as the server requires), and
// repeats until a turn emits no client tool calls or MaxTurns is reached. fn
// observes every streamed line across all turns.
//
// opts.MaxTurns overrides DefaultMaxTurns. opts.ClientTools is ignored; the
// definitions come from tools.
func (c *Client) RunTools(ctx context.Context, message string, tools []Tool, opts SendOptions, fn func(AssistantResponse) error) (string, error) {
	byName := make(map[string]Tool, len(tools))
	defs := make([]ClientTool, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		defs = append(defs, t.ClientTool)
	}
	opts.ClientTools = defs

	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}

	var next any = message
	convID := opts.ConversationID
	for range maxTurns {
		var calls []Content
		id, err := c.Send(ctx, next, opts, func(ar AssistantResponse) error {
			if ar.Data.Attributes.StructuredMessage.Content.Type == ContentClientToolCall {
				calls = append(calls, ar.Data.Attributes.StructuredMessage.Content)
			}
			if fn != nil {
				return fn(ar)
			}
			return nil
		})
		if err != nil {
			return convID, err
		}
		convID = id
		opts.ConversationID = convID

		if len(calls) == 0 {
			return convID, nil // turn finished with no client tool calls
		}

		responses := make([]ClientToolResponse, 0, len(calls))
		for _, call := range calls {
			name := ""
			input := ""
			if call.Metadata != nil {
				name, input = call.Metadata.Name, call.Metadata.Input
			}
			resp := ClientToolResponse{
				Type:       "client_tool_response",
				ToolCallID: call.ToolCallID,
				Status:     "success",
				Metadata:   ClientToolMetadata{Name: name, Input: input},
			}
			tool, ok := byName[name]
			if !ok {
				resp.Status = "error"
				resp.Title = "Unknown tool"
				resp.Metadata.Output = fmt.Sprintf("no client tool named %q is registered", name)
			} else {
				out, runErr := tool.Run(ctx, input)
				if runErr != nil {
					resp.Status = "error"
					resp.Title = "Tool error"
					resp.Metadata.Output = runErr.Error()
				} else {
					resp.Title = "Ran " + name
					resp.Metadata.Output = out
				}
			}
			responses = append(responses, resp)
		}
		next = responses
	}
	return convID, fmt.Errorf("exceeded MaxTurns (%d) without completing", maxTurns)
}

// ConversationHistory fetches the full message history for a conversation.
func (c *Client) ConversationHistory(ctx context.Context, conversationID string) (*ConversationHistoryResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/conversation-history/"+conversationID, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out ConversationHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	return &out, nil
}

// UserConversations lists all conversations for the current user.
func (c *Client) UserConversations(ctx context.Context) (*UserConversationsResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/user-conversations", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out UserConversationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode conversations: %w", err)
	}
	return &out, nil
}

// ListSkills fetches the skills available to the caller's org from
// GET /api/v2/assistant/skills. Enable one for a turn via
// SendOptions.SkillOverrides.
func (c *Client) ListSkills(ctx context.Context) ([]Skill, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/skills", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out SkillsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode skills: %w", err)
	}
	return out.Data.Attributes.Skills, nil
}

// ExperimentalToolFlags reports which feature flags are active for the org.
func (c *Client) ExperimentalToolFlags(ctx context.Context) (map[string]bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/experimental-tool-flags", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out ExperimentalToolFlagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode flags: %w", err)
	}
	return out.Data.Attributes.Flags, nil
}

// DeleteConversation deletes a conversation by id.
func (c *Client) DeleteConversation(ctx context.Context, conversationID string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/v2/assistant/conversation/"+conversationID, nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
