package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	multiAgentRunsPath             = "/api/unstable/assistant/multi-agent/runs"
	multiAgentConversationPath     = "/api/unstable/assistant/multi-agent/conversation/"
	multiAgentConversationIDHeader = "X-Assistant-Conversation-ID"
	multiAgentRunIDHeader          = "X-Assistant-Multi-Agent-Run-ID"
	multiAgentRootIDHeader         = "X-Assistant-Multi-Agent-Root-Agent-ID"
)

// ErrNoActiveMultiAgentRun can occur when a late tool result races stream closure.
var ErrNoActiveMultiAgentRun = errors.New("no multi-agent run is waiting for client-tool results")

// MultiAgentClient reads a run's SSE stream continuously while independent
// SubmitToolResults calls resolve client tools from any agent.
type MultiAgentClient struct {
	*Client
	mu     sync.Mutex
	active *multiAgentRun
}

type multiAgentRun struct {
	id                 string
	conversationID     string
	rootAgentID        string
	response           *http.Response
	scanner            *bufio.Scanner
	closeOnce          sync.Once
	terminal           bool
	lastSequence       int
	messageIDs         map[string]string
	requestedTools     map[multiAgentToolKey]multiAgentToolRequested
	pendingTools       map[string]multiAgentToolCorrelation
	toolsMu            sync.Mutex
	toolIDs            map[multiAgentToolKey]string
	unbatchedToolIDs   map[multiAgentToolKey]string
	thinkingFragmented bool
}

type multiAgentToolKey struct {
	agentID    string
	toolCallID string
}

type multiAgentToolCorrelation struct {
	agentID        string
	generation     int
	wireToolCallID string
}

type multiAgentStartRequest struct {
	Prompt                    string            `json:"prompt"`
	Profile                   Profile           `json:"profile"`
	Model                     string            `json:"model,omitempty"`
	Context                   *AssistantContext `json:"context,omitempty"`
	CustomUserContext         string            `json:"custom_user_context,omitempty"`
	ClientTools               []ClientTool      `json:"client_tools,omitempty"`
	SkillOverrides            []SkillOverride   `json:"skill_overrides,omitempty"`
	EnableDebugMode           bool              `json:"enable_debug_mode,omitempty"`
	DebugTag                  string            `json:"debug_tag,omitempty"`
	Referrer                  string            `json:"referrer,omitempty"`
	ExperimentalToolOverrides map[string]any    `json:"experimental_tool_overrides,omitempty"`
}

type multiAgentToolResultsRequest struct {
	Results []multiAgentToolResult `json:"results"`
}

type multiAgentToolResult struct {
	RunID      string `json:"run_id"`
	AgentID    string `json:"agent_id"`
	Generation int    `json:"generation"`
	ToolCallID string `json:"tool_call_id"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	Title      string `json:"title"`
	Content    string `json:"content,omitempty"`
}

type multiAgentEnvelope struct {
	ConversationID string          `json:"conversation_id"`
	RunID          string          `json:"run_id"`
	AgentID        string          `json:"agent_id"`
	Sequence       int             `json:"sequence"`
	Type           string          `json:"type"`
	Event          json.RawMessage `json:"event"`
}

type multiAgentToolRequested struct {
	ToolCallID         string `json:"tool_call_id"`
	ToolName           string `json:"tool_name"`
	ToolInput          string `json:"tool_input"`
	IsClientSide       *bool  `json:"is_client_side"`
	DisplayTitle       string `json:"display_title"`
	DisplayContent     string `json:"display_content"`
	ArgumentsTruncated bool   `json:"arguments_truncated"`
}

// NewMultiAgentClient returns a backend that uses the unstable multi-agent run
// endpoints while retaining the authenticated client's transport and headers.
func NewMultiAgentClient(client *Client) *MultiAgentClient {
	return &MultiAgentClient{Client: client}
}

// CanStream preserves regular-API capabilities outside the bounded run contract.
// The engine routes these turns through Send, including their tool continuations.
// Shipped tool definitions are checked against the run contract by tools tests.
func (c *MultiAgentClient) CanStream(prompt string, opts SendOptions) bool {
	if opts.InferenceMode != "" || len(opts.MessageHistory) != 0 || utf8.RuneCountInString(prompt) > 32_000 || utf8.RuneCountInString(opts.CustomUserContext) > 32_000 {
		return false
	}
	if utf8.RuneCountInString(opts.Model) > 128 || len(opts.SkillOverrides) > 64 {
		return false
	}
	contextJSON, err := json.Marshal(opts.Context)
	if err != nil || len(contextJSON) > 128_000 {
		return false
	}
	body, err := json.Marshal(multiAgentRequest(prompt, opts))
	return err == nil && len(body) <= 1_000_000
}

func multiAgentRequest(prompt string, opts SendOptions) multiAgentStartRequest {
	profile := opts.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	return multiAgentStartRequest{
		Prompt: prompt, Profile: profile, Model: opts.Model,
		Context: opts.Context, CustomUserContext: opts.CustomUserContext,
		ClientTools: opts.ClientTools, SkillOverrides: opts.SkillOverrides,
		EnableDebugMode: opts.EnableDebugMode, DebugTag: opts.DebugTag,
		Referrer: opts.Referrer, ExperimentalToolOverrides: opts.ExperimentalToolOverrides,
	}
}

// Stream owns the run until a terminal event, transport failure, or cancellation.
// Its callback is serialized; result submissions may run concurrently with it.
func (c *MultiAgentClient) Stream(ctx context.Context, prompt string, opts SendOptions, fn func(AssistantResponse) error) error {
	run, err := c.start(ctx, prompt, opts)
	if err != nil {
		return err
	}
	defer c.finish(run)
	// Publish identity before reading content, including empty and failed turns.
	if fn != nil {
		var response AssistantResponse
		response.Data.Attributes.ConversationID = run.conversationID
		err = fn(response)
	}
	if err == nil {
		err = c.scan(ctx, run, fn)
	}
	if err != nil && !run.terminal {
		// Close the stream immediately, then await one bounded cancellation
		// attempt. No detached cleanup request can outlive the command.
		run.close()
		cancelCtx, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
		defer cancel()
		_ = c.cancelRun(cancelCtx, run.id)
	}
	return err
}

// SubmitToolResults accepts individual results or subsets of pending batches.
// The server resumes each agent only once its entire generation is resolved.
func (c *MultiAgentClient) SubmitToolResults(ctx context.Context, responses []ClientToolResponse) error {
	run, err := c.activeRun()
	if err != nil {
		return err
	}
	return c.submitToolResults(ctx, run, responses)
}

func (c *MultiAgentClient) start(ctx context.Context, prompt string, opts SendOptions) (*multiAgentRun, error) {
	if opts.InferenceMode != "" || len(opts.MessageHistory) != 0 {
		return nil, errors.New("multi-agent endpoint has unsupported inference mode or raw history")
	}
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("a multi-agent run is already active")
	}
	c.mu.Unlock()

	path := multiAgentRunsPath
	if opts.ConversationID != "" {
		path = multiAgentConversationPath + url.PathEscape(opts.ConversationID)
	}
	body := multiAgentRequest(prompt, opts)

	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, c.responseError(ctx, req, resp, path)
	}
	runID := resp.Header.Get(multiAgentRunIDHeader)
	if runID == "" {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("multi-agent start response is missing %s", multiAgentRunIDHeader)
	}
	rootAgentID := resp.Header.Get(multiAgentRootIDHeader)
	if rootAgentID == "" {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("multi-agent start response is missing %s; deploy the matching assistant API changes", multiAgentRootIDHeader)
	}
	conversationID := resp.Header.Get(multiAgentConversationIDHeader)
	if conversationID == "" || (opts.ConversationID != "" && conversationID != opts.ConversationID) {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("multi-agent response has missing or mismatched %s: %q", multiAgentConversationIDHeader, conversationID)
	}
	maxLine := c.maxLineBytes()
	initial := min(64*1024, maxLine)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, initial), maxLine)
	run := &multiAgentRun{
		id:               runID,
		conversationID:   conversationID,
		rootAgentID:      rootAgentID,
		response:         resp,
		scanner:          scanner,
		messageIDs:       make(map[string]string),
		requestedTools:   make(map[multiAgentToolKey]multiAgentToolRequested),
		pendingTools:     make(map[string]multiAgentToolCorrelation),
		toolIDs:          make(map[multiAgentToolKey]string),
		unbatchedToolIDs: make(map[multiAgentToolKey]string),
	}
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		run.close()
		return nil, fmt.Errorf("a multi-agent run became active concurrently")
	}
	c.active = run
	c.mu.Unlock()
	return run, nil
}

func (c *MultiAgentClient) activeRun() (*multiAgentRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return nil, ErrNoActiveMultiAgentRun
	}
	return c.active, nil
}

func (c *MultiAgentClient) submitToolResults(
	ctx context.Context,
	run *multiAgentRun,
	responses []ClientToolResponse,
) error {
	if len(responses) == 0 {
		return errors.New("multi-agent tool results must not be empty")
	}
	results := make([]multiAgentToolResult, 0, len(responses))
	seen := make(map[string]struct{}, len(responses))
	for _, response := range responses {
		run.toolsMu.Lock()
		correlation, ok := run.pendingTools[response.ToolCallID]
		run.toolsMu.Unlock()
		if !ok {
			return fmt.Errorf("client-tool result %q does not match the pending multi-agent batch", response.ToolCallID)
		}
		if _, duplicate := seen[response.ToolCallID]; duplicate {
			return fmt.Errorf("duplicate client-tool result %q", response.ToolCallID)
		}
		seen[response.ToolCallID] = struct{}{}
		content := ""
		if response.Content != nil {
			content = response.Content.Content
		}
		results = append(results, multiAgentToolResult{
			RunID:      run.id,
			AgentID:    correlation.agentID,
			Generation: correlation.generation,
			ToolCallID: correlation.wireToolCallID,
			Status:     string(response.Status),
			Output:     response.Metadata.Output,
			Title:      response.Title,
			Content:    content,
		})
	}
	path := multiAgentRunsPath + "/" + url.PathEscape(run.id) + "/tool-results"
	body, err := encodeMultiAgentToolResults(results)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, json.RawMessage(body))
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return c.responseError(ctx, req, resp, path)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	_ = resp.Body.Close()
	run.toolsMu.Lock()
	for _, response := range responses {
		delete(run.pendingTools, response.ToolCallID)
	}
	run.toolsMu.Unlock()
	return nil
}

func (c *MultiAgentClient) scan(
	ctx context.Context,
	run *multiAgentRun,
	fn func(AssistantResponse) error,
) error {
	var idledOut atomic.Bool
	timer := time.AfterFunc(c.streamIdleTimeout(), func() {
		idledOut.Store(true)
		_ = run.response.Body.Close()
	})
	defer timer.Stop()
	for run.scanner.Scan() {
		timer.Reset(c.streamIdleTimeout())
		line := bytes.TrimSpace(run.scanner.Bytes())
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			return fmt.Errorf("invalid multi-agent SSE line: %s", line)
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		var envelope multiAgentEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("decode multi-agent SSE event: %w: %s", err, payload)
		}
		if envelope.RunID != run.id {
			return fmt.Errorf("multi-agent event run ID %q does not match %q", envelope.RunID, run.id)
		}
		if envelope.ConversationID != run.conversationID {
			return fmt.Errorf("multi-agent event conversation ID %q does not match %q", envelope.ConversationID, run.conversationID)
		}
		if envelope.Sequence <= run.lastSequence {
			return fmt.Errorf("multi-agent event sequence %d is not after %d", envelope.Sequence, run.lastSequence)
		}
		run.lastSequence = envelope.Sequence
		terminal, err := c.handleEvent(run, envelope, fn)
		if err != nil {
			return err
		}
		if terminal {
			return nil
		}
	}
	if err := run.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("multi-agent stream line exceeds max size (%d bytes): %w", c.maxLineBytes(), err)
		}
		if idledOut.Load() {
			return fmt.Errorf("%w: no data received for %s", ErrStreamIdleTimeout, c.streamIdleTimeout())
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("read multi-agent stream: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("multi-agent stream ended before a terminal event")
}

func (c *MultiAgentClient) handleEvent(
	run *multiAgentRun,
	envelope multiAgentEnvelope,
	fn func(AssistantResponse) error,
) (terminal bool, err error) {
	emit := func(message Message) error {
		if fn == nil {
			return nil
		}
		var response AssistantResponse
		response.Data.ID = message.MessageID
		response.Data.Type = "assistant-response"
		response.Data.Attributes.StructuredMessage = message
		response.Data.Attributes.ConversationID = run.conversationID
		return fn(response)
	}
	message := func(content Content) Message {
		messageID := run.messageIDs[envelope.AgentID]
		if content.Type == ContentWidgetDef || content.Type == ContentDashboard {
			// Standalone payloads must not replace siblings from the same model response.
			messageID = fmt.Sprintf("%s:%s:%d", run.id, envelope.AgentID, envelope.Sequence)
		} else if messageID == "" {
			messageID = fmt.Sprintf("%s:%s:%d", run.id, envelope.AgentID, envelope.Sequence)
			run.messageIDs[envelope.AgentID] = messageID
		}
		result := AssistantMessage(messageID, content)
		result.AgentID = envelope.AgentID
		return result
	}

	switch envelope.Type {
	case "model.response_started":
		run.messageIDs[envelope.AgentID] = fmt.Sprintf("%s:%s:%d", run.id, envelope.AgentID, envelope.Sequence)
		return false, nil
	case "content.fragment_received":
		if envelope.AgentID != run.rootAgentID {
			return false, nil
		}
		var event struct {
			Fragment string `json:"fragment"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		return false, emit(message(TextContent(event.Fragment)))
	case "tool.call_started":
		var event struct {
			ToolCallID   string `json:"tool_call_id"`
			ToolName     string `json:"tool_name"`
			IsClientSide bool   `json:"is_client_side"`
			DisplayTitle string `json:"display_title"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		key := multiAgentToolKey{agentID: envelope.AgentID, toolCallID: event.ToolCallID}
		localToolCallID := fmt.Sprintf("multi-agent:%s:stream:%d:%s", envelope.AgentID, envelope.Sequence, event.ToolCallID)
		if run.unbatchedToolIDs == nil {
			run.unbatchedToolIDs = make(map[multiAgentToolKey]string)
		}
		run.unbatchedToolIDs[key] = localToolCallID

		run.toolIDs[key] = localToolCallID
		content := Content{Type: ContentToolCallStarted, Tool: &ToolPayload{
			ToolCallID: localToolCallID, ToolName: event.ToolName, IsClientSide: event.IsClientSide, HasClientSide: true, Title: event.DisplayTitle,
		}}
		return false, emit(message(content))
	case "tool.call_requested":
		var event multiAgentToolRequested
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		key := multiAgentToolKey{agentID: envelope.AgentID, toolCallID: event.ToolCallID}
		if event.IsClientSide != nil && !*event.IsClientSide {
			localID := run.toolIDs[key]
			if localID == "" {
				localID = fmt.Sprintf("multi-agent:%s:stream:%d:%s", envelope.AgentID, envelope.Sequence, event.ToolCallID)
				run.toolIDs[key] = localID
			}
			// Approval and other deferred markers share the original server
			// call's identity. Consume it at the next batch boundary so a
			// later deferred generation still gets a fresh local identity.
			if run.unbatchedToolIDs == nil {
				run.unbatchedToolIDs = make(map[multiAgentToolKey]string)
			}
			run.unbatchedToolIDs[key] = localID
			payload := &ToolPayload{ToolCallID: localID, Title: event.DisplayTitle, Metadata: &ToolMetadata{Name: event.ToolName, Input: event.ToolInput, ArgumentsTruncated: event.ArgumentsTruncated}}
			if event.DisplayContent != "" {
				payload.Detail = &MarkdownPayload{Content: event.DisplayContent}
			}
			return false, emit(message(Content{Type: ContentToolCall, Tool: payload}))
		}
		run.requestedTools[key] = event
		return false, nil
	case "client_tool.batch_ready":
		var event struct {
			Generation  int      `json:"generation"`
			ToolCallIDs []string `json:"tool_call_ids"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		if len(event.ToolCallIDs) == 0 {
			return false, fmt.Errorf("multi-agent client-tool batch is empty")
		}
		for _, toolCallID := range event.ToolCallIDs {
			key := multiAgentToolKey{agentID: envelope.AgentID, toolCallID: toolCallID}
			request, ok := run.requestedTools[key]
			if !ok {
				return false, fmt.Errorf("multi-agent client-tool batch references unknown call %q", toolCallID)
			}
			localToolCallID := run.unbatchedToolIDs[key]
			if localToolCallID == "" {
				localToolCallID = multiAgentToolID(envelope.AgentID, event.Generation, toolCallID)
			}
			delete(run.unbatchedToolIDs, key)
			run.toolsMu.Lock()
			run.pendingTools[localToolCallID] = multiAgentToolCorrelation{
				agentID: envelope.AgentID, generation: event.Generation, wireToolCallID: toolCallID,
			}
			run.toolsMu.Unlock()
			run.toolIDs[key] = localToolCallID
			tool := request
			input, err := adaptMultiAgentToolInput(tool.ToolName, tool.ToolInput, toolCallID, localToolCallID)
			if err != nil {
				return false, err
			}
			payload := &ToolPayload{
				ToolCallID: localToolCallID,
				Title:      tool.DisplayTitle,
				Metadata: &ToolMetadata{
					Name:               tool.ToolName,
					Input:              input,
					ArgumentsTruncated: tool.ArgumentsTruncated,
				},
				IsClientSide: true,
			}
			if tool.DisplayContent != "" {
				payload.Detail = &MarkdownPayload{Content: tool.DisplayContent}
			}
			content := Content{Type: ContentClientToolCall, Tool: payload}
			toolMessage := message(content)
			toolMessage.AgentID = envelope.AgentID
			if err := emit(toolMessage); err != nil {
				return false, err
			}
			delete(run.requestedTools, key)
		}
		return false, nil
	case "tool.call_completed":
		var event struct {
			ToolCallID     string `json:"tool_call_id"`
			ToolName       string `json:"tool_name"`
			Status         string `json:"status"`
			DisplayTitle   string `json:"display_title"`
			DisplayContent string `json:"display_content"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		key := multiAgentToolKey{agentID: envelope.AgentID, toolCallID: event.ToolCallID}
		localToolCallID := run.toolIDs[key]
		if localToolCallID == "" {
			localToolCallID = multiAgentToolID(envelope.AgentID, 0, event.ToolCallID)
			run.toolIDs[key] = localToolCallID
		}
		// Display content is presentation only; keep the full locally recorded output.
		content := ToolResultContent(localToolCallID, event.ToolName, ToolStatus(event.Status), "")
		content.Tool.Title = event.DisplayTitle
		if event.DisplayContent != "" {
			content.Tool.Detail = &MarkdownPayload{Content: event.DisplayContent}
		}
		return false, emit(message(content))
	case "model.response_completed":
		if envelope.AgentID != run.rootAgentID {
			return false, nil
		}
		var event struct {
			InputTokens        int  `json:"input_tokens"`
			OutputTokens       int  `json:"output_tokens"`
			TimeToFirstChunkMs *int `json:"time_to_first_chunk_ms"`
			ContextWindow      int  `json:"context_window"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		input, output := event.InputTokens, event.OutputTokens
		usageMessage := message(TextContent(""))
		usageMessage.Results = &Results{Usage: &Usage{
			TokensUsed:         event.InputTokens + event.OutputTokens,
			MaxTokens:          event.ContextWindow,
			InputTokens:        &input,
			OutputTokens:       &output,
			TimeToFirstChunkMs: event.TimeToFirstChunkMs,
		}}
		return false, emit(usageMessage)
	case "content.widget_generated":
		var event struct {
			WidgetType    string           `json:"widget_type"`
			WidgetContent WidgetDefinition `json:"widget_content"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		content := Content{Type: ContentWidgetDef, Widget: &WidgetPayload{Title: event.WidgetType, WidgetDef: &event.WidgetContent}}
		return false, emit(message(content))
	case "content.dashboard_generated":
		var event struct {
			DashboardURL     string          `json:"dashboard_url"`
			DashboardContent json.RawMessage `json:"dashboard_content"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		payload, err := multiAgentDashboardPayload(event.DashboardContent)
		if err != nil {
			return false, err
		}
		content := Content{Type: ContentDashboard, Dashboard: payload}
		return false, emit(message(content))
	case "tool.call_input_delta":
		var event struct {
			ToolCallID  string `json:"tool_call_id"`
			PartialJSON string `json:"partial_json"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		id := run.toolIDs[multiAgentToolKey{agentID: envelope.AgentID, toolCallID: event.ToolCallID}]
		if id == "" {
			return false, nil
		}
		return false, emit(message(Content{Type: ContentToolCallInputDelta, Tool: &ToolPayload{ToolCallID: id, PartialJSON: event.PartialJSON}}))
	case "thinking.started":
		if envelope.AgentID == run.rootAgentID {
			run.thinkingFragmented = false
		}
		return false, nil
	case "thinking.fragment_received", "thinking.completed":
		if envelope.AgentID != run.rootAgentID {
			return false, nil
		}
		var event struct {
			Fragment     string `json:"fragment"`
			FullThinking string `json:"full_thinking"`
			Redacted     bool   `json:"redacted"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		text := event.Fragment
		if envelope.Type == "thinking.completed" {
			if !run.thinkingFragmented && !event.Redacted {
				text = event.FullThinking
			}
		} else {
			run.thinkingFragmented = true
		}
		content := ThinkingContent(text)
		content.Thinking.Redacted = event.Redacted
		return false, emit(message(content))
	case "debug.context":
		if envelope.AgentID != run.rootAgentID || fn == nil {
			return false, nil
		}
		var event struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		var response AssistantResponse
		response.Data.Attributes.ConversationID = run.conversationID
		response.Data.Attributes.Prompt = event.Content
		return false, fn(response)
	case "run.terminal":
		var event struct {
			Outcome string `json:"outcome"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			return false, err
		}
		if event.Outcome != "completed" {
			run.terminal = true
			return true, fmt.Errorf("multi-agent run ended with %s (%s): %s", event.Outcome, event.Code, event.Message)
		}
		run.terminal = true
		return true, nil
	default:
		return false, nil
	}
}

func multiAgentToolID(agentID string, generation int, wireToolCallID string) string {
	return fmt.Sprintf("multi-agent:%s:%d:%s", agentID, generation, wireToolCallID)
}

func (c *MultiAgentClient) cancelRun(ctx context.Context, runID string) error {
	path := multiAgentRunsPath + "/" + url.PathEscape(runID) + "/cancel"
	req, err := c.newRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return c.responseError(ctx, req, resp, path)
	}
	_ = resp.Body.Close()
	return nil
}

func (c *MultiAgentClient) responseError(
	ctx context.Context,
	req *http.Request,
	resp *http.Response,
	path string,
) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	_ = resp.Body.Close()
	apiErr := httpError(snippet, resp.StatusCode, req.Method, path)
	var rejectErr error
	if resp.StatusCode == http.StatusUnauthorized {
		rejectErr = c.rejectAccessToken(ctx, req)
	}
	return errors.Join(apiErr, rejectErr)
}

func (c *MultiAgentClient) finish(run *multiAgentRun) {
	run.close()
	c.mu.Lock()
	if c.active == run {
		c.active = nil
	}
	c.mu.Unlock()
}

func (r *multiAgentRun) close() {
	r.closeOnce.Do(func() {
		_ = r.response.Body.Close()
	})
}

var _ interface {
	Send(context.Context, any, SendOptions, func(AssistantResponse) error) (string, error)
} = (*MultiAgentClient)(nil)
