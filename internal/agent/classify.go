package agent

import (
	"strconv"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// classify maps one streamed response line to an Event and, when the line is a
// client tool call the caller must answer, the originating Content. The switch
// is exhaustive over ContentKind so a new kind must be handled here.
func classify(ar assistant.AssistantResponse) (Event, *assistant.Content) {
	msg := ar.Data.Attributes.StructuredMessage
	c := msg.Content

	if u := usageOf(msg); u != nil {
		return Event{Kind: EventUsage, Usage: u}, nil
	}

	switch c.Kind() {
	case assistant.KindText, assistant.KindReasoning:
		if c.Content == "" {
			return Event{Kind: EventNone}, nil
		}
		return Event{
			Kind:    EventDelta,
			ItemID:  itemID(c, msg),
			Role:    assistant.RoleOf(msg.Role),
			Content: c.Kind(),
			Text:    c.Content,
		}, nil
	case assistant.KindToolCall:
		var call *assistant.Content
		if c.Type == assistant.ContentClientToolCall {
			call = &c
		}
		return Event{Kind: EventTool, ItemID: itemID(c, msg), Tool: toolCall(c)}, call
	case assistant.KindToolResult:
		return Event{Kind: EventTool, ItemID: itemID(c, msg), Tool: toolCall(c)}, nil
	case assistant.KindWidget, assistant.KindDashboard, assistant.KindProgress,
		assistant.KindTurnMarker, assistant.KindStop, assistant.KindInternal,
		assistant.KindUnknown:
		return Event{Kind: EventNone}, nil // deferred/observe-only for the MVP
	}
	return Event{Kind: EventNone}, nil
}

// itemID is the stable transcript key: composite (MessageID, kind) for
// text/reasoning so a message that emits reasoning then answer yields two
// blocks; shared ToolCallID for a tool call and its result.
func itemID(c assistant.Content, msg assistant.Message) string {
	switch c.Kind() {
	case assistant.KindToolCall, assistant.KindToolResult:
		if c.ToolCallID != "" {
			return "tool:" + c.ToolCallID
		}
		return "tool:" + msg.MessageID
	default:
		return "msg:" + msg.MessageID + "#" + strconv.Itoa(int(c.Kind()))
	}
}

func usageOf(msg assistant.Message) *assistant.Usage {
	if msg.Results != nil {
		return msg.Results.Usage
	}
	return nil
}

func toolCall(c assistant.Content) ToolCall {
	tc := ToolCall{Status: c.Status}
	if c.Metadata != nil {
		tc.Name, tc.Input, tc.Output = c.Metadata.Name, c.Metadata.Input, c.Metadata.Output
	}
	return tc
}
