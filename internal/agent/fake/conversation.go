package fake

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.starlark.net/starlark"

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

// scriptTurn is everything needed to re-execute a turn: its source, or the
// function push_conversation() received, its index in the conversation
// (random()'s default seed), the tool responses received for each round so
// far, the conversations it pushed, the turn's replay-stable file snapshot,
// and where its breakpoints look for continue files.
type scriptTurn struct {
	src         string
	fn          starlark.Callable
	index       int
	results     [][]assistant.ClientToolResponse
	pushed      []string
	snapshot    *sourceSnapshot
	continueDir string
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

// startTurn records the user's script and stores it for continuation. A
// non-nil fn is run instead of text, which then only describes it.
func (f *Fake) startTurn(c *conversation, text string, fn starlark.Callable) scriptTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendLocked(c, assistant.Message{Role: "user", MessageID: f.nextID(), Content: assistant.TextContent(text)})
	if c.title == "" {
		c.title = truncateRunes(strings.TrimSpace(text), titleRunes)
	}
	c.turn = &scriptTurn{
		src:         strings.TrimSpace(text),
		fn:          fn,
		index:       c.turns,
		snapshot:    newSourceSnapshot(f.scriptRoot()),
		continueDir: cmp.Or(f.ContinueDir, defaultContinueDir()),
	}
	c.turns++
	return *c.turn
}

// scriptRoot returns the fixed root for a new turn. Capturing the current
// directory here, rather than while a module is loaded, keeps relative paths
// stable if the process changes directory during the turn.
func (f *Fake) scriptRoot() string {
	root := f.ScriptRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	if root == "" {
		return ""
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return absolute
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
	turn := *c.turn
	turn.results = slices.Clone(turn.results)
	turn.pushed = slices.Clone(turn.pushed)
	return turn, true
}

// rememberPush records a conversation pushed by c's current turn, so a
// replayed round returns its id instead of pushing it again.
func (f *Fake) rememberPush(c *conversation, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.turn != nil {
		c.turn.pushed = append(c.turn.pushed, id)
	}
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
	// The history resource has its own id, distinct from the conversation's,
	// as the real API's contract says (see the assistant client e2e test).
	r.Data.ID = "fake-history-" + strconv.FormatInt(f.seq.Add(1), 10)
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
