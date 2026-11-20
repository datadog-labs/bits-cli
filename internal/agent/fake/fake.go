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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

var (
	_ agent.Backend                 = (*Fake)(nil)
	_ agent.HistoryBackend          = (*Fake)(nil)
	_ agent.ConversationListBackend = (*Fake)(nil)
	_ agent.CurrentUserBackend      = (*Fake)(nil)
	_ agent.SkillsBackend           = (*Fake)(nil)
	_ agent.TaskBackend             = (*Fake)(nil)
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

	// BundledFixtures lets top-level load() find scripts from testdata by
	// filename when no file with that name exists under ScriptRoot.
	BundledFixtures bool

	// ContinueDir holds breakpoint continue files: creating ContinueDir/<name>
	// continues a script stopped at breakpoint(name). Empty means
	// $TMPDIR/bits-fake/continue. Tests set a t.TempDir() so parallel runs
	// never continue each other.
	ContinueDir string

	// seq hands out message, tool call, and conversation ids. It must not
	// derive from the message: identical prompts would then collide and
	// clobber earlier transcript items (keyed by message id).
	seq atomic.Int64
	// liveSeq names live-only messages, see liveID.
	liveSeq atomic.Int64

	// IdleClose closes a subscription that has been silent this long, as the
	// service may. Zero keeps it open.
	IdleClose time.Duration

	mu    sync.Mutex               // guards convs, every *conversation, and offlineUntil
	shown *sync.Cond               // on mu: a subscription handled a message
	convs map[string]*conversation // created lazily so the zero Fake works
	// offlineUntil fails every request until then, as a lost network would.
	offlineUntil time.Time
}

// New returns a Fake with a small inter-fragment delay so interactive use looks
// like a real stream.
func New() *Fake {
	return &Fake{Delay: 10 * time.Millisecond, BundledFixtures: true, IdleClose: 2 * time.Minute}
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
