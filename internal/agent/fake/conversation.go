package fake

import (
	"cmp"
	"context"
	"encoding/json"
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

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// titleRunes bounds a conversation title derived from its first message.
const titleRunes = 60

// generatedTitle stands in for the title the server generates after the
// n-th conversation's first turn. Unlike a fallback title, it never matches
// the prompt.
func generatedTitle(n int) string { return "Fake generated title " + strconv.Itoa(n) }

// conversation is one fake conversation. All fields are guarded by Fake.mu.
type conversation struct {
	id      string
	title   string // first user message, cut to titleRunes
	updated int64  // Unix milliseconds of the last recorded message
	// sharedUntil is when the share expires, in Unix milliseconds, or nil
	// while the conversation is private.
	sharedUntil *int64
	messages    []assistant.Message
	turns       int         // user turns started, which index the next turn
	turn        *scriptTurn // the latest turn; nil before the first
	tasks       []*fakeTask // background tasks its turns started
}

// scriptTurn is everything needed to re-execute a turn: its source, or the
// function push_conversation() received, its index in the conversation
// (random()'s default seed), the tool responses received for each round so
// far, the ids its built-ins returned, the turn's replay-stable file snapshot,
// and where its breakpoints look for continue files.
type scriptTurn struct {
	src         string
	fn          starlark.Callable
	index       int
	results     [][]assistant.ClientToolResponse
	ids         []string
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
// non-nil fn is run instead of text, which then only describes it. Context
// entities are persisted on the user message, as the real backend does.
func (f *Fake) startTurn(c *conversation, text string, fn starlark.Callable, turnContext *assistant.AssistantContext) scriptTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	user := assistant.Message{Role: "user", MessageID: f.nextID(), Content: assistant.TextContent(text)}
	if turnContext != nil && len(turnContext.Entities) > 0 {
		user.ContextEntities, _ = json.Marshal(turnContext.Entities)
	}
	f.appendLocked(c, user)
	if c.title == "" {
		c.title = truncateRunes(strings.TrimSpace(text), titleRunes)
	}
	c.turn = &scriptTurn{
		src:         strings.TrimSpace(text),
		fn:          fn,
		index:       c.turns,
		snapshot:    newSourceSnapshot(f.scriptRoot(), f.BundledFixtures),
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
	turn.ids = slices.Clone(turn.ids)
	return turn, true
}

// rememberID records an id that push_conversation() or task() returned in
// c's current turn, so a replayed round returns it instead of acting again.
func (f *Fake) rememberID(c *conversation, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.turn != nil {
		c.turn.ids = append(c.turn.ids, id)
	}
}

// record appends a delivered message to the conversation history.
func (f *Fake) record(c *conversation, msg assistant.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendLocked(c, msg)
}

// appendLocked persists msg with its time, as history does even for a message
// streamed without one.
func (f *Fake) appendLocked(c *conversation, msg assistant.Message) {
	c.updated = time.Now().UnixMilli()
	if msg.CreatedAt == 0 {
		msg.CreatedAt = c.updated
	}
	c.messages = append(c.messages, msg)
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
	r.Data.Attributes.SharedExpires = c.sharedUntil
	r.Data.Attributes.Messages = append(slices.Clone(c.messages), taskHistoryLocked(c, time.Now())...)
	return &r, nil
}

// UserConversations lists conversations with at least one message, newest
// first, keeping the first in.PageSize when it is set.
func (f *Fake) UserConversations(ctx context.Context, in assistant.UserConversationsInput) (*assistant.UserConversationsResponse, error) {
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
	if convs := r.Data.Attributes.Conversations; in.PageSize > 0 && len(convs) > in.PageSize {
		r.Data.Attributes.Conversations = convs[:in.PageSize]
	}
	return &r, nil
}

// RenameConversation sets a conversation's title.
func (f *Fake) RenameConversation(ctx context.Context, in assistant.RenameConversationInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.convs[in.ConversationID]
	if !ok {
		return &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found", Input: in}
	}
	c.title = in.Title
	return nil
}

// defaultShareDays is the server's share lifetime when a request names none.
const defaultShareDays = 456

// ShareConversation shares a conversation until now plus in.TTLDays, or the
// server default, or makes it private. Sharing again restarts the lifetime.
func (f *Fake) ShareConversation(ctx context.Context, in assistant.ShareConversationInput) (*assistant.ConversationSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.convs[in.ConversationID]
	if !ok {
		return nil, &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found", Input: in}
	}
	c.sharedUntil = nil
	if in.Shared {
		days := defaultShareDays
		if in.TTLDays != nil {
			days = *in.TTLDays
		}
		until := time.Now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
		c.sharedUntil = &until
	}
	return &assistant.ConversationSummary{
		ID: c.id, ConversationID: c.id, UpdatedAt: c.updated, Title: c.title, SharedExpires: c.sharedUntil,
	}, nil
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

// ListSkills returns a fixed catalog
func (*Fake) ListSkills(ctx context.Context) ([]assistant.Skill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []assistant.Skill{
		{Name: "coding", Source: "assistant", Description: "Write and review code."},
		{Name: "code-search", Source: "datadog_mcp", Description: "Search code across repositories."},
		{Name: "skills-library/clear-writing", Source: "skills_library", Description: "Rewrite text so it is short and plain."},
		{Name: "skills-library/incident-retro", Source: "skills_library", Description: "Draft an incident retrospective from a timeline."},
		{Name: "skills-library/slo-review", Source: "skills_library", Description: "Review SLO targets and burn rates."},
		{Name: "skills-library/dashboard-polish", Source: "skills_library", Description: "Tidy dashboard layouts and titles."},
		{Name: "skills-library/monitor-tuning-for-noisy-high-cardinality-alerts", Source: "skills_library", Description: "Reduce alert noise on high-cardinality monitors."},
		{Name: "skills-library/onboarding-guide", Source: "skills_library", Description: "Explain a service to a new teammate with kubernetes context."},
		{Name: "skills-library/log-pipeline-audit", Source: "skills_library", Description: "Find unused or expensive log processors."},
	}, nil
}
