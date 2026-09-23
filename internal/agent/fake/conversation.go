package fake

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// titleRunes bounds a conversation title derived from its first message.
const titleRunes = 60

// conversation is one fake conversation. All fields are guarded by Fake.mu.
type conversation struct {
	id       string
	title    string // first user message, cut to titleRunes
	updated  int64  // Unix milliseconds of the last recorded message
	messages []assistant.Message
	turns    int         // user turns started, which index the next turn
	turn     *scriptTurn // the latest turn; nil before the first
}

// scriptTurn is everything needed to re-execute a turn: its source, its
// index in the conversation (random()'s default seed), and the tool
// responses received for each round so far.
type scriptTurn struct {
	src     string
	index   int
	results [][]assistant.ClientToolResponse
}

// conversation returns the conversation for id, creating it when unknown. An
// empty id starts a new conversation with a canonical lowercase UUID, because
// the engine only lists and switches to such ids.
func (f *Fake) conversation(id string) *conversation {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.convs[id]; ok {
		return c
	}
	if id == "" {
		id = fmt.Sprintf("00000000-0000-4000-8000-%012x", f.seq.Add(1))
	}
	if f.convs == nil {
		f.convs = make(map[string]*conversation)
	}
	c := &conversation{id: id}
	f.convs[id] = c
	return c
}

// startTurn records the user message, sets the title if unset, and replaces
// the pending turn with one whose script is the message.
func (f *Fake) startTurn(c *conversation, text string) scriptTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendLocked(c, assistant.Message{Role: "user", MessageID: f.nextID(), Content: assistant.TextContent(text)})
	if c.title == "" {
		c.title = truncateRunes(strings.TrimSpace(text), titleRunes)
	}
	c.turn = &scriptTurn{src: strings.TrimSpace(text), index: c.turns}
	c.turns++
	return *c.turn
}

// resumeTurn records the responses as client_tool_response history messages,
// appends them as the next round, and returns a copy of the turn.
func (f *Fake) resumeTurn(c *conversation, rs []assistant.ClientToolResponse) (scriptTurn, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rs {
		f.appendLocked(c, toolResponseMessage(f.nextID(), r))
	}
	if c.turn == nil {
		return scriptTurn{}, false
	}
	c.turn.results = append(c.turn.results, rs)
	return scriptTurn{src: c.turn.src, index: c.turn.index, results: slices.Clone(c.turn.results)}, true
}

// record appends a delivered message to the conversation history.
func (f *Fake) record(c *conversation, msg assistant.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendLocked(c, msg)
}

func (f *Fake) appendLocked(c *conversation, msg assistant.Message) {
	c.messages = append(c.messages, msg)
	c.updated = time.Now().UnixMilli()
}

// toolResponseMessage is the persisted form of a client tool response, which
// the server's history echoes with role user.
func toolResponseMessage(id string, r assistant.ClientToolResponse) assistant.Message {
	payload := &assistant.ToolPayload{
		ToolCallID: r.ToolCallID,
		Title:      r.Title,
		Status:     string(r.Status),
		Metadata:   &assistant.ToolMetadata{Name: r.Metadata.Name, Input: r.Metadata.Input, Output: r.Metadata.Output},
	}
	if r.Content != nil {
		payload.Detail = &assistant.MarkdownPayload{Content: r.Content.Content}
	}
	return assistant.Message{
		Role:      "user",
		MessageID: id,
		Content:   assistant.Content{Type: assistant.ContentClientToolResponse, Tool: payload},
	}
}

// ConversationHistory returns a recorded conversation, or a 404 APIError.
func (f *Fake) ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.convs[in.ConversationID]
	if !ok {
		return nil, &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found"}
	}
	var r assistant.ConversationHistoryResponse
	r.Data.ID = c.id
	r.Data.Type = "conversation-history-response"
	r.Data.Attributes.Title = c.title
	r.Data.Attributes.Messages = slices.Clone(c.messages)
	return &r, nil
}

// UserConversations lists conversations with at least one message, newest
// first.
func (f *Fake) UserConversations(ctx context.Context) (*assistant.UserConversationsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var r assistant.UserConversationsResponse
	r.Data.Type = "user-conversations-response"
	for _, c := range f.convs {
		if len(c.messages) == 0 {
			continue
		}
		r.Data.Attributes.Conversations = append(r.Data.Attributes.Conversations, assistant.ConversationSummary{
			ID: c.id, ConversationID: c.id, UpdatedAt: c.updated, Title: c.title,
		})
	}
	slices.SortFunc(r.Data.Attributes.Conversations, func(a, b assistant.ConversationSummary) int {
		return cmp.Or(cmp.Compare(b.UpdatedAt, a.UpdatedAt), strings.Compare(b.ID, a.ID))
	})
	return &r, nil
}

// CurrentUser returns a fixed demo identity.
func (*Fake) CurrentUser(ctx context.Context) (assistant.CurrentUser, error) {
	if err := ctx.Err(); err != nil {
		return assistant.CurrentUser{}, err
	}
	return assistant.CurrentUser{
		Name:           "Demo User",
		Handle:         "demo@example.com",
		Email:          "demo@example.com",
		Organization:   "Demo Org",
		OrganizationID: "demo-org",
	}, nil
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
