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
	// for the turn. Must be resent on every turn.
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
	// StreamToolCallInput opts into streamed tool-call input: the server emits a
	// tool_call_started followed by tool_call_input_delta fragments before the
	// final tool_call / client_tool_call. Off by default; without it those two
	// content types are never sent.
	StreamToolCallInput bool
	// MaxTurns caps the RunTools agent loop. Zero uses DefaultMaxTurns.
	// Ignored by Send.
	MaxTurns int
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
	Message                   any                  `json:"message"`
	ConversationID            string               `json:"conversation_id,omitempty"`
	Model                     string               `json:"model,omitempty"`
	Referrer                  string               `json:"referrer,omitempty"`
	ClientTools               []ClientTool         `json:"client_tools,omitempty"`
	SkillOverrides            []SkillOverride      `json:"skill_overrides,omitempty"`
	Context                   *AssistantContext    `json:"context,omitempty"`
	Profile                   Profile              `json:"profile,omitempty"`
	CustomUserContext         string               `json:"custom_user_context,omitempty"`
	EnableDebugMode           bool                 `json:"enable_debug_mode,omitempty"`
	DebugTag                  string               `json:"debug_tag,omitempty"`
	MessageHistory            []json.RawMessage    `json:"message_history,omitempty"`
	ExperimentalToolOverrides map[string]any       `json:"experimental_tool_overrides,omitempty"`
	Capabilities              *RequestCapabilities `json:"capabilities,omitempty"`
}

// RequestCapabilities declares optional response behaviors the client can
// handle. Each capability defaults off server-side, so a behavior is only
// enabled once explicitly requested.
type RequestCapabilities struct {
	// StreamToolCallInput requests streamed tool-call input (tool_call_started +
	// tool_call_input_delta) ahead of the authoritative final tool call.
	StreamToolCallInput bool `json:"stream_tool_call_input,omitempty"`
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
	ProfileWebUI Profile = "web_ui" // default; bare persona, UI-oriented (widgets, relative links)
)

// DefaultProfile is applied by Send when SendOptions.Profile is empty. It
// matches the server's own default so behavior is unchanged unless overridden.
const DefaultProfile = ProfileWebUI

// AssistantContext is the request-level `context` object: the Datadog objects
// the user has in scope for the turn. The server's EntitiesProvider fetches
// each entity's details (dashboard title/widgets, monitor query, incident, …)
// and injects them into the system prompt, so attaching an entity is the
// programmatic equivalent of the UI's "@dashboard" context chips.
//
// Entities must be resent on every request in the conversation (like
// ClientTools); RunTools carries them across turns via SendOptions.
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

// Entity types the server's EntitiesProvider enriches with fetched details
// (KnownContextEntityType). Any other type string is accepted but only
// described generically.
// EntityType identifies the kind of Datadog object a ContextEntity references.
// The server accepts unknown values (described generically), so this is an open
// set; the Entity* constants are the types it enriches with fetched details.
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
	// IsDeferred marks the tool for deferred (search-based) loading rather than
	// eager exposure.
	IsDeferred bool `json:"is_deferred,omitempty"`
	// IsAvailable reports whether the tool is usable on the current page. Defaults
	// to true server-side; set the pointer to send false explicitly.
	IsAvailable *bool `json:"is_available,omitempty"`
	// RequiresApproval gates execution behind a user approval prompt. One of the
	// ToolApproval* constants; empty means the server default (ToolApprovalNo).
	RequiresApproval ToolApproval `json:"requires_approval,omitempty"`
}

// ToolApproval controls whether a client tool requires user approval before
// execution.
type ToolApproval string

const (
	ToolApprovalNo      ToolApproval = "no"      // never prompt
	ToolApprovalYes     ToolApproval = "yes"     // always prompt before running
	ToolApprovalRuntime ToolApproval = "runtime" // decide per-invocation from the input
)

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
// text-bearing content (markdown_fragment, thinking) arrives as many small
// fragments sharing one MessageID that the caller concatenates.
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
	// ContentWidgetDef carries a Datadog dashboard widget definition (in the
	// WidgetDef field) that the UI renders as a rich visualization. The
	// definition is a query spec + styling; it contains no data. It is decoded
	// generically into a map for now; running the widget's queries is out of scope.
	ContentWidgetDef = "widget_def"
	// ContentDashboard is a whole generated dashboard (title + widgets).
	ContentDashboard = "dashboard"

	// --- On the POST stream only if capabilities.stream_tool_call_input is set ---

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

// Content is a polymorphic message body. Only the fields relevant to its Type
// are populated.
type Content struct {
	Type string `json:"type"`

	// markdown_fragment / thinking: the text fragment.
	Content string `json:"-"`

	// thinking extras.
	EncryptedContent string `json:"encrypted_content,omitempty"`
	Redacted         bool   `json:"redacted,omitempty"`

	// tool_call / tool_response.
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Title      string        `json:"title,omitempty"`
	Status     string        `json:"status,omitempty"` // tool_response only
	Metadata   *ToolMetadata `json:"metadata,omitempty"`

	// tool_call_started (only when StreamToolCallInput is set): the tool being
	// invoked, sent before its input streams in.
	ToolName     string `json:"tool_name,omitempty"`
	IsClientSide bool   `json:"is_client_side,omitempty"`

	// tool_call_input_delta (only when StreamToolCallInput is set): one ordered
	// fragment of the tool call's input JSON.
	PartialJSON string `json:"partial_json,omitempty"`

	// widget_def: the Datadog widget definition object (nil when absent).
	WidgetDef *WidgetDefinition `json:"widget_def,omitempty"`
	// widget (persisted/rendered form): the raw tile definition and its timeframe.
	TileDef   json.RawMessage `json:"tile_def,omitempty"`
	Timeframe *Timeframe      `json:"timeframe,omitempty"`
	// dashboard: the raw widgets array (for summarizing widget count) plus the
	// surrounding dashboard metadata.
	Widgets           json.RawMessage `json:"widgets,omitempty"`
	Description       string          `json:"description,omitempty"`
	LayoutType        string          `json:"layout_type,omitempty"`
	ReflowType        string          `json:"reflow_type,omitempty"`
	TemplateVariables json.RawMessage `json:"template_variables,omitempty"`

	// background_task_update: which phase this update is ("progress"/"final").
	// The human-readable body is in Content. turn_status uses Status
	// ("started"/"ended"); user_stop's marker text is in Content.
	EventType string `json:"event_type,omitempty"`
	// background_task_update: task identity, ordering, and free-form metadata.
	TaskID       string            `json:"task_id,omitempty"`
	Sequence     int               `json:"sequence,omitempty"`
	TaskMetadata map[string]string `json:"-"`

	// provider_compaction (never streamed to clients): opaque vendor payload the
	// backend echoes on the next request; modeled for completeness.
	Provider      string `json:"provider,omitempty"`
	VendorPayload string `json:"vendor_payload,omitempty"`
	Summary       string `json:"summary,omitempty"`

	// Nested content: for tool_call / tool_response this is a
	// markdown_fragment describing the call. For text content this is the
	// raw string. Kept as RawMessage so Content stays one flat type.
	nested json.RawMessage
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

// UnmarshalJSON handles the fact that "content" is a string for text
// fragments but a nested object for tool_call / tool_response.
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
		IsClientSide      bool              `json:"is_client_side"`
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
	c.EncryptedContent = a.EncryptedContent
	c.Redacted = a.Redacted
	c.ToolCallID = a.ToolCallID
	c.Title = a.Title
	c.Status = a.Status
	c.ToolName = a.ToolName
	c.IsClientSide = a.IsClientSide
	c.PartialJSON = a.PartialJSON
	c.WidgetDef = a.WidgetDef
	c.TileDef = a.TileDef
	c.Timeframe = a.Timeframe
	c.Widgets = a.Widgets
	c.Description = a.Description
	c.LayoutType = a.LayoutType
	c.ReflowType = a.ReflowType
	c.TemplateVariables = a.TemplateVariables
	c.EventType = a.EventType
	c.TaskID = a.TaskID
	c.Sequence = a.Sequence
	c.Provider = a.Provider
	c.VendorPayload = a.VendorPayload
	c.Summary = a.Summary

	// metadata is polymorphic: a tool metadata object for tool_call /
	// tool_response / client_tool_call, but a flat string map for
	// background_task_update. Decode by content type.
	if len(a.Metadata) > 0 {
		if a.Type == ContentBackgroundTaskUpdate {
			_ = json.Unmarshal(a.Metadata, &c.TaskMetadata)
		} else {
			_ = json.Unmarshal(a.Metadata, &c.Metadata)
		}
	}

	if len(a.Content) == 0 {
		return nil
	}
	// Try string first (text fragments); otherwise keep the nested object.
	var s string
	if err := json.Unmarshal(a.Content, &s); err == nil {
		c.Content = s
	} else {
		c.nested = a.Content
	}
	return nil
}

// Nested returns the nested descriptive Content for tool_call / tool_response
// messages (typically a markdown_fragment). ok is false when there is none.
func (c *Content) Nested() (Content, bool) {
	if len(c.nested) == 0 {
		return Content{}, false
	}
	var nc Content
	if err := json.Unmarshal(c.nested, &nc); err != nil {
		return Content{}, false
	}
	return nc, true
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
	KindToolResult                    // tool_response
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
	case ContentToolResponse:
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
