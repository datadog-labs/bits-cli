package assistant

import "encoding/json"

// Role is the message author. The wire form is the string Message.Role
// ("user" / "assistant"); Role is the typed value the rest of the app uses.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleSystem
)

// RoleOf maps a wire role string to a Role. Unknown values fall back to
// RoleAssistant.
func RoleOf(s string) Role {
	switch s {
	case "user":
		return RoleUser
	case "system":
		return RoleSystem
	default:
		return RoleAssistant
	}
}

// SendOptions configures a single call to Send.
type SendOptions struct {
	// ConversationID resumes an existing conversation. Empty starts a new one;
	// the server-generated id is available via the streamed responses.
	ConversationID string
	// Model optionally overrides the model (e.g. "claude-sonnet-4-6").
	Model string
	// InferenceMode optionally requests the backend's fast or deep inference mode.
	InferenceMode string
	// Referrer is the Datadog page URL the user was on, used for context.
	Referrer string
	// ClientTools are client-side tools the caller can execute. Required to
	// participate in the client-tool / approval flow. They must be resent on
	// every request in the conversation, including tool-result follow-ups.
	ClientTools []ClientTool
	// SkillOverrides flip per-skill default-enabled state for the request.
	// Must be resent on every turn.
	SkillOverrides []SkillOverride
	// Context puts Datadog objects (dashboards, monitors, services...) in scope
	// for one backend request. Agent loops must resend a turn's context on tool
	// continuations and must not carry it to the next independent turn.
	Context *AssistantContext
	// Profile selects the server-side surface preset.
	Profile Profile
	// CustomUserContext is freeform text injected into the prompt as extra
	// context for the assistant's response.
	CustomUserContext string
	// EnableDebugMode asks the server for a detailed internal response; when the
	// org's debug-mode feature flag is on, the system prompt is echoed back in
	// ResponseAttributes.Prompt.
	EnableDebugMode bool
	// DebugTag tags the request in server traces.
	DebugTag string
	// MessageHistory injects an explicit conversation history, bypassing the
	// server's chat store. Each entry is a raw AssistantConversationMessage
	// object (role/message_id/agent_id/content/...); pass pre-formed JSON since
	// Message does not round-trip through Go marshaling.
	MessageHistory []json.RawMessage
	// ExperimentalToolOverrides overrides experimental tool feature flags for the
	// turn (e.g. {"user_memory": true} or {"mcp_tool__ask_widget_expert": true}).
	// Values are usually bool; external MCP session flags carry string values.
	ExperimentalToolOverrides map[string]any
}

// Request is the JSON:API-style envelope for POST /api/v2/assistant.
type Request struct {
	Data RequestData `json:"data"`
}

// RequestData is the inner "data" object of a Request.
type RequestData struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Attributes RequestAttributes `json:"attributes"`
}

// RequestAttributes holds the actual request fields.
//
// Message is either a plain string (a user message) or a list of
// ClientToolResponse objects (answering a client-tool / approval request).
type RequestAttributes struct {
	Message                   any               `json:"message"`
	ConversationID            string            `json:"conversation_id,omitempty"`
	Model                     string            `json:"model,omitempty"`
	InferenceMode             string            `json:"inference_mode,omitempty"`
	Referrer                  string            `json:"referrer,omitempty"`
	ClientTools               []ClientTool      `json:"client_tools,omitempty"`
	SkillOverrides            []SkillOverride   `json:"skill_overrides,omitempty"`
	Context                   *AssistantContext `json:"context,omitempty"`
	Profile                   Profile           `json:"profile,omitempty"`
	CustomUserContext         string            `json:"custom_user_context,omitempty"`
	EnableDebugMode           bool              `json:"enable_debug_mode,omitempty"`
	DebugTag                  string            `json:"debug_tag,omitempty"`
	MessageHistory            []json.RawMessage `json:"message_history,omitempty"`
	ExperimentalToolOverrides map[string]any    `json:"experimental_tool_overrides,omitempty"`
}

// updateConversationRequest is the JSON:API body for renaming a conversation
// (PUT /api/v2/assistant/user-conversations/{id}/title).
type updateConversationRequest struct {
	Data updateConversationData `json:"data"`
}

type updateConversationData struct {
	Type       string                       `json:"type"`
	Attributes updateConversationAttributes `json:"attributes"`
}

type updateConversationAttributes struct {
	Title string `json:"title"`
}

// updateSharingRequest is the (non-JSON:API) body for toggling conversation
// sharing (PUT /api/v2/assistant/conversation/{id}/is_shared). TTLDays maps to
// the server's ttl_days alias; omit it (nil) to use the server default.
type updateSharingRequest struct {
	IsShared bool `json:"is_shared"`
	TTLDays  *int `json:"ttl_days,omitempty"`
}

// Profile selects a server-side surface preset that controls the system
// prompt's surface section, tool availability, and the conversation's
// ChatStore namespace. The server accepts other values, so this is an open set.
type Profile string

// Server-side surface profiles.
const (
	ProfileWebUI Profile = "web_ui" // bare persona, UI-oriented (widgets, relative links)
	ProfileCLI   Profile = "cli"
)

// DefaultProfile is applied by Send when SendOptions.Profile is empty. It
// identifies requests from this terminal client unless explicitly overridden.
const DefaultProfile = ProfileCLI

// AssistantContext is the request-level `context`: Datadog objects in scope for
// the turn. The server fetches each entity's details and injects them into the
// prompt. It must be resent on every request that belongs to the same user
// turn, including client-tool continuations.
type AssistantContext struct {
	Entities []ContextEntity `json:"entities,omitempty"`
	// Resources is the sibling `resources` list. Its wire shape is not modeled
	// here (the UI sends product-specific payloads); pass raw JSON objects if
	// you need it.
	Resources []json.RawMessage `json:"resources,omitempty"`
}

// ContextEntity references one Datadog object to put in scope. Type is one of
// the EntityType* constants (unknown types are still accepted — the server
// falls back to a generic "type: id" description). ID is the object's native
// identifier (dashboard slug, stringified monitor id, service name, incident
// id, profiling-view URL, …). Label is an optional human name for the UI; the
// server ignores it for prompt-building.
type ContextEntity struct {
	Type  EntityType `json:"type"`
	ID    string     `json:"id"`
	Label string     `json:"label,omitempty"`
	// Definition is the entity's definition object. Optional; the server
	// normally fetches details itself, so it can be left nil.
	Definition map[string]any `json:"definition,omitempty"`
}

// EntityType identifies the kind of Datadog object a ContextEntity references.
// The server enriches the known types below with fetched details and accepts
// any other value (described generically), so this is an open set.
type EntityType string

const (
	EntityDashboard          EntityType = "dashboard"
	EntityMonitor            EntityType = "monitor"
	EntityService            EntityType = "service"
	EntityIncident           EntityType = "incident"
	EntityProfile            EntityType = "profile"
	EntityImageURL           EntityType = "image_url"
	EntityErrorTrackingIssue EntityType = "error_tracking_issue"
	EntitySpreadsheet        EntityType = "spreadsheet"
)

// SkillOverride flips a skill's default-enabled state for a request. Name and
// Source identify the skill (matching a Skill from Client.ListSkills); Enabled
// turns it on or off. Overrides for skills not in the org's available set are
// silently ignored by the server. Like ClientTools, overrides should be resent
// on every request in the conversation.
type SkillOverride struct {
	Name    string      `json:"name"`
	Source  SkillSource `json:"source"`
	Enabled bool        `json:"enabled"`
}

// SkillSource identifies where a skill originates. Matches the server's
// SkillSource literal (assistant | datadog_mcp | skills_library).
type SkillSource string

const (
	SkillSourceAssistant     SkillSource = "assistant"
	SkillSourceDatadogMCP    SkillSource = "datadog_mcp"
	SkillSourceSkillsLibrary SkillSource = "skills_library"
)

// ClientTool is a client-side tool definition sent in client_tools. The
// backend does not whitelist these: any tool defined here is offered to the
// model, which invokes it via a client_tool_call the caller must execute and
// answer with a ClientToolResponse. The schema is Anthropic-style (top-level
// name/description/input_schema), not OpenAI function-wrapping.
type ClientTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is a JSON Schema object describing the tool's arguments.
	InputSchema map[string]any `json:"input_schema"`
	// StreamInput allows provisional tool_call_started and tool_call_input_delta
	// events for this tool. The final tool call remains authoritative.
	StreamInput bool `json:"stream_input,omitempty"`
	// IsDeferred marks the tool for deferred (search-based) loading rather than
	// eager exposure.
	IsDeferred bool `json:"is_deferred,omitempty"`
	// IsAvailable reports whether the tool is usable on the current page. Defaults
	// to true server-side; set the pointer to send false explicitly.
	IsAvailable *bool `json:"is_available,omitempty"`

	// RequiresApproval is intentionally not modeled: the server emits its
	// approval gate as the protocol-level approval_request client tool call.
	//
	// RequiresApproval ToolApproval `json:"requires_approval,omitempty"`
}

// MarkdownContent is a nested markdown display block ({type, content}). Used as
// the optional display body on a ClientToolResponse and on tool call/response
// content.
type MarkdownContent struct {
	Type    string `json:"type"` // "markdown_fragment"
	Content string `json:"content"`
}

// ClientToolResponse is one entry in the message list used to answer a
// client-side tool call (including the approval_request flow).
type ClientToolResponse struct {
	Type       string     `json:"type"` // "client_tool_response"
	ToolCallID string     `json:"tool_call_id"`
	Title      string     `json:"title,omitempty"`
	Status     ToolStatus `json:"status,omitempty"`
	// Content is an optional nested markdown block describing the response for
	// display in the UI. The output passed back to the model lives in Metadata.
	Content  *MarkdownContent   `json:"content,omitempty"`
	Metadata ClientToolMetadata `json:"metadata"`
}

// ToolStatus is the outcome of a client tool execution, echoed back to the
// model in a ClientToolResponse.
type ToolStatus string

const (
	ToolStatusSuccess ToolStatus = "success"
	ToolStatusError   ToolStatus = "error"
)

// ClientToolMetadata carries the tool identity plus its (JSON-string) input
// and output for a ClientToolResponse.
type ClientToolMetadata struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

// AssistantResponse is a single newline-delimited line from the POST stream.
type AssistantResponse struct {
	Data struct {
		ID         string             `json:"id"`
		Type       string             `json:"type"` // "assistant-response"
		Attributes ResponseAttributes `json:"attributes"`
	} `json:"data"`
	// Errors is set only for an in-band error line, which the server emits on
	// the streaming 200 as a JSON:API error document ({"errors":[...]}) in place
	// of a data envelope. A normal response line leaves it nil.
	Errors []apiErrorItem `json:"errors,omitempty"`
}

// ResponseAttributes is the payload of one streamed response line. Mirrors the
// server's AssistantResponse: conversation_id, structured_message, and (debug
// only) prompt. Token usage is NOT here — it rides on the message itself
// (StructuredMessage.Results), matching the server's AssistantMessage.results.
type ResponseAttributes struct {
	ConversationID    string  `json:"conversation_id"`
	StructuredMessage Message `json:"structured_message"`
	Prompt            string  `json:"prompt,omitempty"` // only set when debug mode is enabled
}

// Results carries per-message bookkeeping such as token usage (the server's
// LLMCallResults). It appears on the message, not the response envelope.
type Results struct {
	Usage *Usage `json:"usage,omitempty"`
}

// Usage reports token consumption for a model call.
type Usage struct {
	TokensUsed         int  `json:"tokens_used"`
	MaxTokens          int  `json:"max_tokens"`
	InputTokens        *int `json:"input_tokens,omitempty"`
	OutputTokens       *int `json:"output_tokens,omitempty"`
	TimeToFirstChunkMs *int `json:"time_to_first_chunk_ms,omitempty"`
}

// Message is one message in a conversation, streamed or from history.
//
// Content is polymorphic; use Content.Type to discriminate. During streaming,
// text content (markdown_fragment, thinking) arrives as many small fragments
// sharing one MessageID that the caller concatenates.
type Message struct {
	Role             string          `json:"role"` // "user" / "assistant"
	MessageID        string          `json:"message_id"`
	AgentID          string          `json:"agent_id"` // e.g. "command"
	Content          Content         `json:"content"`
	CreatedAt        int64           `json:"created_at,omitempty"`
	Results          *Results        `json:"results,omitempty"`
	BackgroundTaskID *string         `json:"background_task_id,omitempty"`
	ContextEntities  json.RawMessage `json:"context_entities,omitempty"`
	ContextResources json.RawMessage `json:"context_resources,omitempty"`
}

// Content type discriminators: the full AssistantContent union from the server
// (domains/assistant/libs/py/messages/models.py). Only a subset reaches a
// given client transport; Content.Kind buckets them for rendering, and the
// per-constant comments below note where each actually appears.
const (
	// --- Always on the POST /assistant stream ---

	ContentMarkdownFragment = "markdown_fragment" // answer text (also carries usage on the empty final fragment)
	ContentThinking         = "thinking"          // model reasoning (send_to_user gated by profile)
	ContentToolCall         = "tool_call"         // server-side tool call (observe only)
	ContentToolResponse     = "tool_response"     // server-side tool result
	// ContentClientToolCall is a *client-side* tool invocation: the stream
	// pauses until the caller replies with a ClientToolResponse. Only received
	// for tools we declare in client_tools, plus the server-injected
	// approval_request gate. Distinct from ContentToolCall (server-side).
	ContentClientToolCall = "client_tool_call"
	// ContentClientToolResponse is the persisted result of a client-side tool
	// call, echoed into conversation history (role user).
	ContentClientToolResponse = "client_tool_response"
)

// ApprovalRequestTool is the name of the server-injected client_tool_call that
// gates a backend write. It is never advertised in client_tools; the caller
// answers it directly according to its own approval policy.
const ApprovalRequestTool = "approval_request"

const (
	// ContentWidgetDef carries a Datadog dashboard widget definition (in the
	// WidgetDef field) that the UI renders as a rich visualization. The
	// definition is a query spec + styling; it contains no data. It is decoded
	// generically into a map for now; running the widget's queries is out of scope.
	ContentWidgetDef = "widget_def"
	// ContentDashboard is a whole generated dashboard (title + widgets).
	ContentDashboard = "dashboard"

	// --- Provisional POST events for tools with stream_input enabled ---

	ContentToolCallStarted    = "tool_call_started"     // a tool call begins (before input arrives)
	ContentToolCallInputDelta = "tool_call_input_delta" // streamed fragment of a tool call's input JSON

	// --- conversation-history / persistence only (not the live POST stream) ---

	ContentWidget   = "widget"    // persisted/rendered form of a widget (tile_def + timeframe)
	ContentUserStop = "user_stop" // marker persisted when a turn was interrupted

	// --- background-task follow streams (/subscribe, subtask stream) ---

	ContentBackgroundTaskUpdate = "background_task_update" // async investigation/agent progress + final
	ContentTurnStatus           = "turn_status"            // turn started/ended marker (skipped on POST responses)

	// --- never streamed to clients (backend LLM-context optimization) ---

	ContentProviderCompaction = "provider_compaction"
)

// WidgetDefinition is a Datadog dashboard widget definition (the widget_def
// payload). It is decoded generically for now; used via a pointer so callers can
// distinguish absent (nil) from an empty object.
type WidgetDefinition map[string]any

// MarkdownPayload carries the text fragment of a markdown_fragment content.
type MarkdownPayload struct {
	Content string
}

// ThinkingPayload carries a thinking (reasoning) fragment plus its provider
// signature.
type ThinkingPayload struct {
	Content          string
	EncryptedContent string
	Redacted         bool
}

// StopPayload carries the marker text of a user_stop content.
type StopPayload struct {
	Content string
}

// TurnStatusPayload carries the turn_status marker ("started" / "ended").
type TurnStatusPayload struct {
	Status string
}

// ToolPayload carries fields for all tool-related content types: tool_call,
// client_tool_call, tool_call_started, tool_call_input_delta, tool_response,
// client_tool_response.
type ToolPayload struct {
	ToolCallID   string
	Title        string
	Status       string // tool_response: "success" / "error"
	Metadata     *ToolMetadata
	ToolName     string // tool_call_started
	IsClientSide bool   // tool_call_started
	// HasClientSide distinguishes an omitted wire field from explicit false.
	// Streaming starts can be normalized from the registered client tools only
	// when the provider did not state an identity.
	HasClientSide bool
	PartialJSON   string // tool_call_input_delta
	// Detail is the nested display markdown ("content" on the wire), shown in the
	// UI beside the call. Always a markdown fragment; nil when absent. The data
	// passed back to the model lives in Metadata.
	Detail *MarkdownPayload
}

// WidgetPayload carries fields for the widget_def and widget content types.
type WidgetPayload struct {
	Title     string
	WidgetDef *WidgetDefinition
	TileDef   json.RawMessage
	Timeframe *Timeframe
}

// DashboardPayload carries fields for the dashboard content type.
type DashboardPayload struct {
	Title             string
	Widgets           json.RawMessage
	Description       string
	LayoutType        string
	ReflowType        string
	TemplateVariables json.RawMessage
}

// ProgressPayload carries fields for the background_task_update content type;
// Content holds the human-readable body.
type ProgressPayload struct {
	Content      string
	EventType    string // "progress" / "final"
	TaskID       string
	Sequence     int
	TaskMetadata map[string]string
}

// CompactionPayload carries fields for the provider_compaction content type.
type CompactionPayload struct {
	Provider      string
	VendorPayload string
	Summary       string
}

// Content is a polymorphic message body: Type is always set, and exactly one
// matching payload pointer is non-nil (all nil for types this client does not
// model). It mirrors the server's AssistantContent discriminated union, so
// distinct variants — text and a widget, say — never coexist on one Content.
type Content struct {
	Type string `json:"type"`

	Markdown   *MarkdownPayload   // markdown_fragment
	Thinking   *ThinkingPayload   // thinking
	Tool       *ToolPayload       // tool_call, client_tool_call, tool_call_started, tool_call_input_delta, tool_response, client_tool_response
	Widget     *WidgetPayload     // widget_def, widget
	Dashboard  *DashboardPayload  // dashboard
	Progress   *ProgressPayload   // background_task_update
	Stop       *StopPayload       // user_stop
	TurnStatus *TurnStatusPayload // turn_status
	Compaction *CompactionPayload // provider_compaction
}

// TextBody returns the human-readable text of the text-bearing variants
// (markdown_fragment, thinking, user_stop, background_task_update) and "" for
// every other type.
func (c *Content) TextBody() string {
	switch {
	case c.Markdown != nil:
		return c.Markdown.Content
	case c.Thinking != nil:
		return c.Thinking.Content
	case c.Stop != nil:
		return c.Stop.Content
	case c.Progress != nil:
		return c.Progress.Content
	}
	return ""
}

// The constructors below build the Content variants a client produces rather
// than decodes (replayed history, a fake backend, tests), keeping the "Type
// matches the one non-nil payload" invariant in one place. Fields they omit can
// be set on the returned payload.

// TextContent builds a markdown_fragment (answer text).
func TextContent(text string) Content {
	return Content{Type: ContentMarkdownFragment, Markdown: &MarkdownPayload{Content: text}}
}

// ThinkingContent builds a thinking (reasoning) fragment.
func ThinkingContent(text string) Content {
	return Content{Type: ContentThinking, Thinking: &ThinkingPayload{Content: text}}
}

// ToolCallContent builds a server-side tool_call. Input is JSON encoded as a
// string, as it is on the wire.
func ToolCallContent(toolCallID, name, input string) Content {
	return Content{
		Type: ContentToolCall,
		Tool: &ToolPayload{
			ToolCallID: toolCallID,
			Metadata:   &ToolMetadata{Name: name, Input: input},
		},
	}
}

// ToolResultContent builds a tool_response for the call with toolCallID. Name is
// optional: the call it merges with already carries it.
func ToolResultContent(toolCallID, name string, status ToolStatus, output string) Content {
	return Content{
		Type: ContentToolResponse,
		Tool: &ToolPayload{
			ToolCallID: toolCallID,
			Status:     string(status),
			Metadata:   &ToolMetadata{Name: name, Output: output},
		},
	}
}

// AssistantMessage wraps content in an assistant-authored Message. Streamed
// fragments that should concatenate into one block share a messageID.
func AssistantMessage(messageID string, content Content) Message {
	return Message{Role: "assistant", MessageID: messageID, Content: content}
}

// Timeframe is the time window attached to a persisted widget content block.
type Timeframe struct {
	Start  int64 `json:"start"` // start timestamp in milliseconds
	End    int64 `json:"end"`   // end timestamp in milliseconds
	Paused bool  `json:"paused"`
}

// ToolMetadata describes a server- or client-side tool call/response. Input
// and Output are JSON encoded as strings.
type ToolMetadata struct {
	Name               string  `json:"name"`
	Input              string  `json:"input,omitempty"`
	Output             string  `json:"output,omitempty"`
	Signature          *string `json:"signature,omitempty"`
	ArgumentsTruncated bool    `json:"arguments_truncated,omitempty"`
	Namespace          *string `json:"namespace,omitempty"`
}

// UnmarshalJSON dispatches on the content type, populating the one matching
// payload. It also handles "content" being a plain string for text fragments
// but a nested object for tool_call / tool_response.
func (c *Content) UnmarshalJSON(data []byte) error {
	type alias struct {
		Type              string            `json:"type"`
		Content           json.RawMessage   `json:"content"`
		EncryptedContent  string            `json:"encrypted_content"`
		Redacted          bool              `json:"redacted"`
		ToolCallID        string            `json:"tool_call_id"`
		Title             string            `json:"title"`
		Status            string            `json:"status"`
		Metadata          json.RawMessage   `json:"metadata"`
		ToolName          string            `json:"tool_name"`
		IsClientSide      *bool             `json:"is_client_side"`
		PartialJSON       string            `json:"partial_json"`
		WidgetDef         *WidgetDefinition `json:"widget_def"`
		TileDef           json.RawMessage   `json:"tile_def"`
		Timeframe         *Timeframe        `json:"timeframe"`
		Widgets           json.RawMessage   `json:"widgets"`
		Description       string            `json:"description"`
		LayoutType        string            `json:"layout_type"`
		ReflowType        string            `json:"reflow_type"`
		TemplateVariables json.RawMessage   `json:"template_variables"`
		EventType         string            `json:"event_type"`
		TaskID            string            `json:"task_id"`
		Sequence          int               `json:"sequence"`
		Provider          string            `json:"provider"`
		VendorPayload     string            `json:"vendor_payload"`
		Summary           string            `json:"summary"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	c.Type = a.Type

	switch a.Type {
	case ContentMarkdownFragment:
		c.Markdown = &MarkdownPayload{Content: jsonString(a.Content)}
	case ContentThinking:
		c.Thinking = &ThinkingPayload{
			Content:          jsonString(a.Content),
			EncryptedContent: a.EncryptedContent,
			Redacted:         a.Redacted,
		}
	case ContentUserStop:
		c.Stop = &StopPayload{Content: jsonString(a.Content)}
	case ContentToolCall, ContentClientToolCall, ContentToolCallStarted, ContentToolCallInputDelta, ContentToolResponse, ContentClientToolResponse:
		tp := &ToolPayload{
			ToolCallID:    a.ToolCallID,
			Title:         a.Title,
			Status:        a.Status,
			ToolName:      a.ToolName,
			HasClientSide: a.IsClientSide != nil,
			PartialJSON:   a.PartialJSON,
		}
		if a.IsClientSide != nil {
			tp.IsClientSide = *a.IsClientSide
		}
		if len(a.Metadata) > 0 {
			_ = json.Unmarshal(a.Metadata, &tp.Metadata)
		}
		// A tool's "content" is a nested MarkdownContent (display detail), never a
		// bare string; pull out its text.
		var md struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(a.Content, &md) == nil && md.Content != "" {
			tp.Detail = &MarkdownPayload{Content: md.Content}
		}
		c.Tool = tp
	case ContentWidgetDef, ContentWidget:
		c.Widget = &WidgetPayload{
			Title:     a.Title,
			WidgetDef: a.WidgetDef,
			TileDef:   a.TileDef,
			Timeframe: a.Timeframe,
		}
	case ContentDashboard:
		c.Dashboard = &DashboardPayload{
			Title:             a.Title,
			Widgets:           a.Widgets,
			Description:       a.Description,
			LayoutType:        a.LayoutType,
			ReflowType:        a.ReflowType,
			TemplateVariables: a.TemplateVariables,
		}
	case ContentBackgroundTaskUpdate:
		pp := &ProgressPayload{
			Content:   jsonString(a.Content),
			EventType: a.EventType,
			TaskID:    a.TaskID,
			Sequence:  a.Sequence,
		}
		if len(a.Metadata) > 0 {
			_ = json.Unmarshal(a.Metadata, &pp.TaskMetadata)
		}
		c.Progress = pp
	case ContentTurnStatus:
		c.TurnStatus = &TurnStatusPayload{Status: a.Status}
	case ContentProviderCompaction:
		c.Compaction = &CompactionPayload{
			Provider:      a.Provider,
			VendorPayload: a.VendorPayload,
			Summary:       a.Summary,
		}
	}

	return nil
}

// jsonString decodes raw as a JSON string, returning "" when raw is empty or
// not a string (e.g. a nested object). The stream's "content" key is a string
// for text variants but an object for tool variants, so it must be captured as
// json.RawMessage and decoded per type.
func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ContentKind is a coarse rendering bucket for a Content. It lets a client
// switch exhaustively over a handful of behaviors instead of every wire type,
// with KindUnknown catching content types added server-side in the future.
type ContentKind int

const (
	KindUnknown    ContentKind = iota // unrecognized/future type: safe to ignore or log
	KindText                          // markdown_fragment: render as answer text
	KindReasoning                     // thinking: render dimmed/collapsible
	KindToolCall                      // tool_call / client_tool_call / tool_call_started / tool_call_input_delta
	KindToolResult                    // tool_response / client_tool_response
	KindWidget                        // widget_def / widget: rich viz, render or summarize
	KindDashboard                     // dashboard: whole dashboard, summarize
	KindProgress                      // background_task_update: async task progress/final
	KindTurnMarker                    // turn_status: turn started/ended (not seen on POST)
	KindStop                          // user_stop: interrupted-turn marker
	KindInternal                      // provider_compaction: never client-visible
)

// Kind classifies the content into a ContentKind. The switch is exhaustive
// over every AssistantContent variant; unknown/future types fall through to
// KindUnknown so callers never silently mishandle new server content.
func (c *Content) Kind() ContentKind {
	switch c.Type {
	case ContentMarkdownFragment:
		return KindText
	case ContentThinking:
		return KindReasoning
	case ContentToolCall, ContentClientToolCall, ContentToolCallStarted, ContentToolCallInputDelta:
		return KindToolCall
	case ContentToolResponse, ContentClientToolResponse:
		return KindToolResult
	case ContentWidgetDef, ContentWidget:
		return KindWidget
	case ContentDashboard:
		return KindDashboard
	case ContentBackgroundTaskUpdate:
		return KindProgress
	case ContentTurnStatus:
		return KindTurnMarker
	case ContentUserStop:
		return KindStop
	case ContentProviderCompaction:
		return KindInternal
	default:
		return KindUnknown
	}
}

// String returns a short, stable label for a ContentKind, used for logging and
// as the fallback render label. The switch is exhaustive so a new kind must be
// named here.
func (k ContentKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindReasoning:
		return "reasoning"
	case KindToolCall:
		return "tool_call"
	case KindToolResult:
		return "tool_result"
	case KindWidget:
		return "widget"
	case KindDashboard:
		return "dashboard"
	case KindProgress:
		return "progress"
	case KindTurnMarker:
		return "turn"
	case KindStop:
		return "stop"
	case KindInternal:
		return "internal"
	case KindUnknown:
		return "unknown"
	}
	return "unknown"
}

// ConversationHistoryResponse is returned by ConversationHistory.
type ConversationHistoryResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Title         string    `json:"title"`
			Messages      []Message `json:"messages"`
			OwnerUserUUID string    `json:"owner_user_uuid"`
			SharedExpires *int64    `json:"shared_expires_at"`
		} `json:"attributes"`
	} `json:"data"`
}

// ConversationSummary is one entry in UserConversationsResponse.
type ConversationSummary struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	UpdatedAt      int64  `json:"updated_at"`
	Title          string `json:"title"`
	OwnerUserUUID  string `json:"owner_user_uuid"`
	SharedExpires  *int64 `json:"shared_expires_at"`
}

// UserConversationsResponse is returned by UserConversations.
type UserConversationsResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Conversations []ConversationSummary `json:"conversations"`
		} `json:"attributes"`
	} `json:"data"`
}

// ExperimentalToolFlagsResponse is returned by ExperimentalToolFlags.
type ExperimentalToolFlagsResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Flags map[string]bool `json:"flags"`
		} `json:"attributes"`
	} `json:"data"`
}

// Skill is one entry from Client.ListSkills (GET /api/v2/assistant/skills). A
// skill bundles instructions and a tool selection for a workflow; the model
// loads an enabled skill on demand via the unified Skill tool. Enable one for a
// turn with SendOptions.SkillOverrides.
type Skill struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // "assistant" / "datadog_mcp" / "skills_library"
	Description string `json:"description"`
	// DefaultEnabled reports whether the skill is already on for the org
	// without an override.
	DefaultEnabled bool `json:"default_enabled"`
}

// SkillsListResponse is returned by ListSkills.
type SkillsListResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Skills []Skill `json:"skills"`
		} `json:"attributes"`
	} `json:"data"`
}

// ConversationHistoryInput is the input to Client.ConversationHistory.
type ConversationHistoryInput struct {
	ConversationID string
}

// DeleteConversationInput is the input to Client.DeleteConversation.
type DeleteConversationInput struct {
	ConversationID string
}

// RenameConversationInput is the input to Client.RenameConversation.
type RenameConversationInput struct {
	ConversationID string
	Title          string
}

// ShareConversationInput is the input to Client.ShareConversation. When Shared
// is false, TTLDays is ignored.
type ShareConversationInput struct {
	ConversationID string
	Shared         bool
	TTLDays        *int
}
