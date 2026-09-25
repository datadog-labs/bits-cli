// Package fake provides an offline agent.Backend. Every user message is a
// Starlark script whose built-ins emit exactly the requested wire output,
// including deterministic pseudo-random answers through random() (see
// script.go). It satisfies the same contract as
// *assistant.Client, including conversation history and listing, so it plugs
// into the engine at the agent.Backend seam. Select it with
// BITS_FAKE_BACKEND=1; it drives the real engine/classify/transcript/render
// pipeline, so only the network is faked.
package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

var (
	_ agent.Backend                 = (*Fake)(nil)
	_ agent.HistoryBackend          = (*Fake)(nil)
	_ agent.ConversationListBackend = (*Fake)(nil)
	_ agent.CurrentUserBackend      = (*Fake)(nil)
)

// Fake implements agent.Backend and its optional capabilities offline.
type Fake struct {
	// Delay is the pause before each emitted message, simulating a live
	// stream. Zero (the default in tests) streams instantly.
	Delay time.Duration

	// ScriptRoot is the directory from which Starlark load() resolves module
	// paths. An empty root uses the process's current directory when a turn
	// starts.
	ScriptRoot string

	// ContinueDir holds breakpoint continue files: creating ContinueDir/<name>
	// continues a script stopped at breakpoint(name). Empty means
	// $TMPDIR/bits-fake/continue. Tests set a t.TempDir() so parallel runs
	// never continue each other.
	ContinueDir string

	// seq hands out message, tool call, and conversation ids. It must not
	// derive from the message: identical prompts would then collide and
	// clobber earlier transcript items (keyed by message id).
	seq atomic.Int64

	mu    sync.Mutex               // guards convs and every *conversation
	convs map[string]*conversation // created lazily so the zero Fake works
}

// New returns a Fake with a small inter-fragment delay so interactive use looks
// like a real stream.
func New() *Fake { return &Fake{Delay: 10 * time.Millisecond} }

// Send runs a user message as a script, or resumes the current script with
// the tool responses of its last round. The returned id is the
// conversation's canonical id. It honors ctx so Esc/Ctrl+C interrupt a turn.
func (f *Fake) Send(ctx context.Context, message any, opts assistant.SendOptions,
	fn func(assistant.AssistantResponse) error,
) (string, error) {
	c := f.conversation(opts.ConversationID)
	out := &emitter{
		ctx: ctx, delay: f.Delay, convID: c.id, fn: fn,
		record: func(m assistant.Message) { f.record(c, m) },
		nextID: f.nextID,
	}
	switch m := message.(type) {
	case string:
		source := m
		if strings.TrimSpace(m) == "test ask_user_question" {
			source = questionDemoSource(opts)
		}
		return c.id, runScript(out, opts, f.startTurn(c, m, source))
	case []assistant.ClientToolResponse:
		turn, ok := f.resumeTurn(c, m)
		if !ok {
			return c.id, errors.New("fake: tool responses without a turn")
		}
		return c.id, runScript(out, opts, turn)
	default:
		return c.id, fmt.Errorf("fake: unsupported message type %T", message)
	}
}

// nextID hands out a fresh, globally unique message id.
func (f *Fake) nextID() string { return "fake-msg-" + strconv.FormatInt(f.seq.Add(1), 10) }

// BackendStatus identifies the local fake transport without implying that a
// Datadog principal or remote site is active.
func (*Fake) BackendStatus() assistant.BackendStatus {
	return assistant.BackendStatus{
		AuthenticationMode:  "none (fake backend)",
		AuthenticationState: "unauthenticated",
	}
}

// SearchEntities keeps demo mode useful without credentials or network access.
// The fixed mixed catalog exercises the same labels and attachment path as the
// real suggestions endpoint.
func (*Fake) SearchEntities(ctx context.Context, in assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error) {
	if err := ctx.Err(); err != nil {
		return assistant.SearchEntitiesResponse{}, err
	}
	installed := true
	serviceAccount := false
	catalog := []assistant.SearchEntity{
		{CandidateID: "fake-dashboard", EntityID: "abc-def", EntityType: "dashboard", Title: "API overview", AuthorName: "Demo User"},
		{CandidateID: "fake-monitor", EntityID: "12345", EntityType: "monitor", Name: "API latency is high", CreatorName: "Demo User"},
		{CandidateID: "fake-service", EntityID: "checkout-api", EntityType: "service", Name: "checkout-api", ProductAreas: []string{"APM"}},
		{CandidateID: "fake-incident", EntityID: "42", EntityType: "incident", Title: "Checkout errors", Severity: "SEV-2", State: "active"},
		{CandidateID: "fake-notebook", EntityID: "notebook-1", EntityType: "notebook", Name: "Incident notes", AuthorName: "Demo User"},
		{CandidateID: "fake-team", EntityID: "team-1", EntityType: "team", Name: "Checkout"},
		{CandidateID: "fake-user", EntityID: "user-1", EntityType: "user", Name: "Demo User", Email: "demo@example.com", IsServiceAccount: &serviceAccount},
		{CandidateID: "fake-integration", EntityID: "github", EntityType: "integration", Name: "GitHub", IsInstalled: &installed},
		{CandidateID: "fake-workflow", EntityID: "workflow-1", EntityType: "workflow", Name: "Deploy checkout"},
		{CandidateID: "fake-synthetic", EntityID: "synthetic-1", EntityType: "synthetic_test", Name: "Checkout availability"},
		{CandidateID: "fake-resource", EntityID: "resource-1", EntityType: "resource", Name: "GET /checkout", URL: "/apm/resource/checkout"},
		{CandidateID: "fake-sheet", EntityID: "sheet-1", EntityType: "spreadsheet", Title: "Checkout metrics", AuthorName: "Demo User"},
		{CandidateID: "fake-app", EntityID: "app-1", EntityType: "app", Name: "Incident helper"},
	}
	groups := in.SuggestionGroups
	if groups == nil {
		groups = assistant.DefaultEntitySuggestionGroups
	}
	query := strings.ToLower(strings.TrimSpace(in.RawQuery))
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	entities := make([]assistant.SearchEntity, 0, min(limit, len(catalog)))
	for _, entity := range catalog {
		if !slices.Contains(groups, string(entity.EntityType)) {
			continue
		}
		haystack := strings.ToLower(entity.DisplayLabel() + " " + entity.DisplayDetail())
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		entities = append(entities, entity)
		if len(entities) == limit {
			break
		}
	}
	return assistant.SearchEntitiesResponse{
		SearchFlowID:           "fake-search-flow",
		Entities:               entities,
		ActiveSuggestionGroups: slices.Clone(groups),
	}, nil
}
