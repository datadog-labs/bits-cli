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
	Message        any               `json:"message"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Model          string            `json:"model,omitempty"`
	Referrer       string            `json:"referrer,omitempty"`
	ClientTools    []ClientTool      `json:"client_tools,omitempty"`
	SkillOverrides []SkillOverride   `json:"skill_overrides,omitempty"`
	Context        *AssistantContext `json:"context,omitempty"`
	Profile        string            `json:"profile,omitempty"`
}

// Server-side surface profiles. Each selects a preset that controls the system
// prompt's surface section, tool availability, and the conversation's
// ChatStore namespace.
const (
	ProfileWebUI = "web_ui" // default; bare persona, UI-oriented (widgets, relative links)
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
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// Entity types the server's EntitiesProvider enriches with fetched details
// (KnownContextEntityType). Any other type string is accepted but only
// described generically.
const (
	EntityDashboard = "dashboard"
	EntityMonitor   = "monitor"
	EntityService   = "service"
	EntityIncident  = "incident"
	EntityProfile   = "profile"
	EntityImageURL  = "image_url"
)

// SkillOverride flips a skill's default-enabled state for a request. Name and
// Source identify the skill (matching a Skill from Client.ListSkills); Enabled
// turns it on or off. Overrides for skills not in the org's available set are
// silently ignored by the server. Like ClientTools, overrides should be resent
// on every request in the conversation.
type SkillOverride struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
}

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
}

// ClientToolResponse is one entry in the message list used to answer a
// client-side tool call (including the approval_request flow).
type ClientToolResponse struct {
	Type       string             `json:"type"` // "client_tool_response"
	ToolCallID string             `json:"tool_call_id"`
	Title      string             `json:"title,omitempty"`
	Status     string             `json:"status,omitempty"` // "success" / "error"
	Metadata   ClientToolMetadata `json:"metadata"`
}

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

// Usage reports token consumption. The server also sends input_tokens,
// output_tokens, and time_to_first_chunk_ms; we model the two headline fields.
type Usage struct {
	TokensUsed int `json:"tokens_used"`
	MaxTokens  int `json:"max_tokens"`
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

	// widget_def: the Datadog widget definition object (nil when absent).
	WidgetDef *WidgetDefinition `json:"widget_def,omitempty"`
	// widget (persisted/rendered form): the raw tile definition.
	TileDef json.RawMessage `json:"tile_def,omitempty"`
	// dashboard: the raw widgets array (for summarizing widget count).
	Widgets json.RawMessage `json:"widgets,omitempty"`

	// background_task_update: which phase this update is ("progress"/"final").
	// The human-readable body is in Content. turn_status uses Status
	// ("started"/"ended"); user_stop's marker text is in Content.
	EventType string `json:"event_type,omitempty"`

	// Nested content: for tool_call / tool_response this is a
	// markdown_fragment describing the call. For text content this is the
	// raw string. Kept as RawMessage so Content stays one flat type.
	nested json.RawMessage
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
		Type             string            `json:"type"`
		Content          json.RawMessage   `json:"content"`
		EncryptedContent string            `json:"encrypted_content"`
		Redacted         bool              `json:"redacted"`
		ToolCallID       string            `json:"tool_call_id"`
		Title            string            `json:"title"`
		Status           string            `json:"status"`
		Metadata         *ToolMetadata     `json:"metadata"`
		WidgetDef        *WidgetDefinition `json:"widget_def"`
		TileDef          json.RawMessage   `json:"tile_def"`
		Widgets          json.RawMessage   `json:"widgets"`
		EventType        string            `json:"event_type"`
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
	c.Metadata = a.Metadata
	c.WidgetDef = a.WidgetDef
	c.TileDef = a.TileDef
	c.Widgets = a.Widgets
	c.EventType = a.EventType

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

// ConversationHistoryResponse is returned by ConversationHistory.
type ConversationHistoryResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Title    string    `json:"title"`
			Messages []Message `json:"messages"`
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
