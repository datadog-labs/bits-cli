package fake

import (
	"context"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// emitter delivers one Send's messages: it paces them, stops on
// cancellation, and records delivered messages into the conversation.
type emitter struct {
	ctx    context.Context
	delay  time.Duration
	convID string
	fn     func(assistant.AssistantResponse) error
	record func(assistant.Message)
	nextID func() string
}

// emit delivers one message after the pacing delay. Streamed-input messages
// are not recorded: history keeps final forms only, like the server's store.
func (e *emitter) emit(msg assistant.Message) error {
	if e.delay > 0 {
		t := time.NewTimer(e.delay)
		defer t.Stop()
		select {
		case <-e.ctx.Done():
			return e.ctx.Err()
		case <-t.C:
		}
	} else if err := e.ctx.Err(); err != nil {
		return err
	}
	if err := e.fn(resp(e.convID, msg)); err != nil {
		return err
	}
	switch msg.Content.Type {
	case assistant.ContentToolCallStarted, assistant.ContentToolCallInputDelta:
	default:
		e.record(msg)
	}
	return nil
}

// text streams s as one message, split after whitespace so the fragments
// reassemble byte-identically to s. typ is markdown_fragment or thinking.
func (e *emitter) text(typ, s string) error {
	id := e.nextID()
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isSpaceByte(s[j]) {
			j++
		}
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		if err := e.emit(assistant.AssistantMessage(id, textContent(typ, s[i:j]))); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// resp wraps one message in the response envelope the stream delivers.
func resp(convID string, msg assistant.Message) assistant.AssistantResponse {
	var ar assistant.AssistantResponse
	ar.Data.Type = "assistant-response"
	ar.Data.Attributes.ConversationID = convID
	ar.Data.Attributes.StructuredMessage = msg
	return ar
}

// textContent builds a text fragment of the given streamed type.
func textContent(typ, s string) assistant.Content {
	if typ == assistant.ContentThinking {
		return assistant.ThinkingContent(s)
	}
	return assistant.TextContent(s)
}

// clientToolCall builds a client_tool_call, which pauses the real stream.
func clientToolCall(id, name, input string) assistant.Content {
	c := assistant.ToolCallContent(id, name, input)
	c.Type = assistant.ContentClientToolCall
	return c
}

// toolCallStarted opens a streamed tool call before its input arrives.
func toolCallStarted(id, name string, client bool) assistant.Content {
	return assistant.Content{
		Type: assistant.ContentToolCallStarted,
		Tool: &assistant.ToolPayload{ToolCallID: id, ToolName: name, IsClientSide: client},
	}
}

// toolCallInputDelta carries one fragment of a streamed tool call's input.
func toolCallInputDelta(id, partial string) assistant.Content {
	return assistant.Content{
		Type: assistant.ContentToolCallInputDelta,
		Tool: &assistant.ToolPayload{ToolCallID: id, PartialJSON: partial},
	}
}

// usageMessage reports token usage scaled by the words a turn produced.
func usageMessage(id string, words int) assistant.Message {
	msg := assistant.AssistantMessage(id, assistant.Content{})
	msg.Results = &assistant.Results{
		Usage: &assistant.Usage{TokensUsed: 50 + words*3, MaxTokens: 8000},
	}
	return msg
}
