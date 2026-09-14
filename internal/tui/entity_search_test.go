package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/editor"
)

type blockingEntitySearcher struct {
	started  chan assistant.SearchEntitiesInput
	canceled chan struct{}
}

func (s *blockingEntitySearcher) SearchEntities(ctx context.Context, input assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error) {
	s.started <- input
	<-ctx.Done()
	close(s.canceled)
	return assistant.SearchEntitiesResponse{}, ctx.Err()
}

type staticEntitySearcher struct {
	response assistant.SearchEntitiesResponse
	err      error
	inputs   []assistant.SearchEntitiesInput
}

func (s *staticEntitySearcher) SearchEntities(_ context.Context, input assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error) {
	s.inputs = append(s.inputs, input)
	return s.response, s.err
}

type acceptingBackend struct {
	requests chan assistant.SendOptions
}

func (b *acceptingBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.requests <- opts
	return "conversation-1", nil
}

func TestEntitySearchDebouncesThenCancelsSupersededRequest(t *testing.T) {
	searcher := &blockingEntitySearcher{started: make(chan assistant.SearchEntitiesInput, 1), canceled: make(chan struct{})}
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{EntitySearcher: searcher})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@checkout api"})
	debounce := m.syncEntitySearch()
	if debounce == nil {
		t.Fatal("query did not arm debounce")
	}
	message, ok := debounce().(entitySearchDebounceMsg)
	if !ok || message.query != "checkout api" {
		t.Fatalf("debounce message = %#v", message)
	}
	request := m.beginEntitySearch(message)
	result := make(chan tea.Msg, 1)
	go func() { result <- request() }()
	select {
	case input := <-searcher.started:
		if input.RawQuery != "checkout api" {
			t.Fatalf("raw query = %q", input.RawQuery)
		}
	case <-time.After(time.Second):
		t.Fatal("search did not start")
	}

	m.editor.Reset()
	m.editor.Update(tea.PasteMsg{Content: "@payments"})
	if next := m.syncEntitySearch(); next == nil {
		t.Fatal("new query did not arm another debounce")
	}
	select {
	case <-searcher.canceled:
	case <-time.After(time.Second):
		t.Fatal("superseded request was not canceled")
	}
	<-result
}

func TestEntitySearchTypePrefixNarrowsRequest(t *testing.T) {
	searcher := &staticEntitySearcher{}
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{EntitySearcher: searcher})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: `@service:"Assistant API"`})
	debounce := m.syncEntitySearch()
	message := debounce().(entitySearchDebounceMsg)
	_ = m.beginEntitySearch(message)()

	if len(searcher.inputs) != 1 {
		t.Fatalf("search requests = %d, want 1", len(searcher.inputs))
	}
	input := searcher.inputs[0]
	if input.RawQuery != "Assistant API" {
		t.Fatalf("raw query = %q, want %q", input.RawQuery, "Assistant API")
	}
	if !reflect.DeepEqual(input.SuggestionGroups, []string{"service"}) {
		t.Fatalf("suggestion groups = %#v, want service only", input.SuggestionGroups)
	}
}

func TestQuotedEditorQueryReachesSearchIntact(t *testing.T) {
	searcher := &staticEntitySearcher{}
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{EntitySearcher: searcher})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: `@service:"API \"東京\" @prod"`})
	debounce := m.syncEntitySearch()
	_ = m.beginEntitySearch(debounce().(entitySearchDebounceMsg))()
	if len(searcher.inputs) != 1 {
		t.Fatalf("search requests = %d, want 1", len(searcher.inputs))
	}
	input := searcher.inputs[0]
	if input.RawQuery != `API "東京" @prod` || !reflect.DeepEqual(input.SuggestionGroups, []string{"service"}) {
		t.Fatalf("quoted editor query = %#v", input)
	}
}

func TestEntitySearchBlockedByLogoutState(t *testing.T) {
	for _, state := range []string{"pending", "running", "completed"} {
		t.Run(state, func(t *testing.T) {
			searcher := &staticEntitySearcher{}
			m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{EntitySearcher: searcher})
			m.editor.Focus()
			m.editor.Update(tea.PasteMsg{Content: "@check"})
			_ = m.syncEntitySearch()
			message := entitySearchDebounceMsg{generation: m.entitySearchGeneration, query: "check"}
			m.pendingLogout = state == "pending"
			m.logoutRunning = state == "running"
			m.loggedOut = state == "completed"
			if cmd := m.beginEntitySearch(message); cmd != nil {
				t.Fatal("logout allowed queued search to start")
			}
			m.applyEntitySearchResult(entitySearchResultMsg{generation: message.generation, query: message.query})
			if len(m.entitySearchCache) != 0 {
				t.Fatal("logout accepted search results")
			}
			if cmd := m.syncEntitySearch(); cmd != nil || m.entitySearchActive {
				t.Fatal("logout scheduled a search")
			}
		})
	}
}

func TestLogoutCancelsEntitySearch(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "pending turn drain"}[pending], func(t *testing.T) {
			searcher := &blockingEntitySearcher{started: make(chan assistant.SearchEntitiesInput, 1), canceled: make(chan struct{})}
			m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{
				EntitySearcher: searcher,
				Logout:         func(context.Context) (bool, error, error) { return true, nil, nil },
			})
			t.Cleanup(m.stopEntitySearch)
			m.editor.Focus()
			m.editor.Update(tea.PasteMsg{Content: "@check"})
			_ = m.syncEntitySearch()
			message := entitySearchDebounceMsg{generation: m.entitySearchGeneration, query: "check"}
			request := m.beginEntitySearch(message)
			result := make(chan tea.Msg, 1)
			go func() { result <- request() }()
			select {
			case <-searcher.started:
			case <-time.After(time.Second):
				t.Fatal("search did not start")
			}
			if pending {
				m.requestLogout()
			} else {
				m.startLogout()
				t.Cleanup(m.logoutCancel)
			}
			select {
			case <-searcher.canceled:
			case <-time.After(time.Second):
				t.Fatal("logout did not cancel search")
			}
			m.applyEntitySearchResult((<-result).(entitySearchResultMsg))
			if m.entitySearchActive || m.editor.MenuOpen() || len(m.entitySearchCache) != 0 {
				t.Fatal("search state survived logout transition")
			}
			if cmd := m.beginEntitySearch(message); cmd != nil {
				t.Fatal("logout did not invalidate debounce")
			}
			m.editor.Update(tea.PasteMsg{Content: "out"})
			if cmd := m.syncEntitySearch(); cmd != nil {
				t.Fatal("typing during logout started a search")
			}
		})
	}
}

func TestLogoutInvalidatesQueuedSearchAndFailureAllowsRetry(t *testing.T) {
	searcher := &staticEntitySearcher{}
	logoutErr := errors.New("credential store unavailable")
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}), Config{
		EntitySearcher: searcher,
		Logout:         func(context.Context) (bool, error, error) { return true, nil, logoutErr },
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@check"})
	_ = m.syncEntitySearch()
	message := entitySearchDebounceMsg{generation: m.entitySearchGeneration, query: "check"}
	queued := m.beginEntitySearch(message)
	logout := m.startLogout()
	result := queued().(entitySearchResultMsg)
	if !errors.Is(result.err, context.Canceled) || len(searcher.inputs) != 0 {
		t.Fatal("queued command called searcher after logout started")
	}
	m.applyLogoutResult(logout().(logoutResultMsg))
	if m.beginEntitySearch(message) != nil {
		t.Fatal("failed logout revived a stale debounce")
	}
	debounce := m.syncEntitySearch()
	if debounce == nil {
		t.Fatal("search did not resume after failed logout")
	}
	m.applyEntitySearchResult(m.beginEntitySearch(debounce().(entitySearchDebounceMsg))().(entitySearchResultMsg))
	if len(searcher.inputs) != 1 {
		t.Fatal("retried search did not reach backend")
	}
	logoutErr = nil
	logout = m.startLogout()
	m.applyLogoutResult(logout().(logoutResultMsg))
	if m.entitySearcher != nil || m.entitySearchCancel != nil || m.syncEntitySearch() != nil {
		t.Fatal("successful logout retained authenticated search")
	}
}

func TestEntitySearchQuerySyntaxDistinguishesTypedAndLiteralSearch(t *testing.T) {
	tests := []struct {
		query  string
		raw    string
		groups []string
	}{
		{query: "service:assistant", raw: "assistant", groups: []string{"service"}},
		{query: `service:"Assistant API"`, raw: "Assistant API", groups: []string{"service"}},
		{query: "service: Assistant API", raw: "service: Assistant API", groups: assistant.DefaultEntitySuggestionGroups},
		{query: `"service: Assistant API"`, raw: "service: Assistant API", groups: assistant.DefaultEntitySuggestionGroups},
		{query: "unknown:assistant", raw: "unknown:assistant", groups: assistant.DefaultEntitySuggestionGroups},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			raw, groups := parseEntitySearchQuery(test.query)
			if raw != test.raw || !reflect.DeepEqual(groups, test.groups) {
				t.Fatalf("parseEntitySearchQuery(%q) = (%q, %#v), want (%q, %#v)", test.query, raw, groups, test.raw, test.groups)
			}
		})
	}
}

func TestEntityMentionQueryRoundTrip(t *testing.T) {
	for _, label := range []string{`A "quoted" widget`, `path\to\service`, `trailing\`, `東京 \"quoted\"`} {
		t.Run(label, func(t *testing.T) {
			query := strings.TrimPrefix(entityMention("service", label), "@")
			raw, groups := parseEntitySearchQuery(query)
			if raw != label || !reflect.DeepEqual(groups, []string{"service"}) {
				t.Fatalf("parseEntitySearchQuery(%q) = (%q, %v), want (%q, [service])", query, raw, groups, label)
			}
		})
	}
}

func TestQuotedEntityQueryAcceptsIncompleteAndLiteralEscapes(t *testing.T) {
	for _, test := range []struct{ query, want string }{
		{`"A \"quoted\" widget"`, `A "quoted" widget`},
		{`"A \"quoted\"`, `A "quoted"`}, // escaped final quote is not a closing delimiter
		{`"path\\`, `path\`},
		{`"path\`, `path\`},      // incomplete escape
		{`"path\to"`, `path\to`}, // unknown escapes remain literal
		{`service:path\to`, `path\to`},
		{`service:name"`, `name"`}, // an unquoted trailing quote is literal
	} {
		t.Run(test.query, func(t *testing.T) {
			raw, _ := parseEntitySearchQuery(test.query)
			if raw != test.want {
				t.Fatalf("parseEntitySearchQuery(%q) = %q, want %q", test.query, raw, test.want)
			}
		})
	}
}

func TestEntitySearchRejectsStaleResponse(t *testing.T) {
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}))
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@new"})
	_ = m.syncEntitySearch()
	currentGeneration := m.entitySearchGeneration
	m.applyEntitySearchResult(entitySearchResultMsg{
		generation: currentGeneration - 1,
		query:      "old",
		response: assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
			CandidateID: "old", EntityID: "old", EntityType: "service", Name: "stale-service",
		}}},
	})
	if strings.Contains(m.editor.MenuView(), "stale-service") {
		t.Fatal("stale response replaced the current menu")
	}
}

func TestEntitySearchUsesFreshBoundedCache(t *testing.T) {
	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}))
	m.editor.Focus()
	response := assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
		CandidateID: "cached", EntityID: "checkout", EntityType: "service", Name: "checkout",
	}}}
	m.storeEntitySearch("check", response, time.Now())
	m.editor.Update(tea.PasteMsg{Content: "@check"})
	if command := m.syncEntitySearch(); command != nil {
		t.Fatal("fresh cache entry unexpectedly armed a request")
	}
	if !strings.Contains(m.editor.MenuView(), "◇ [Service] checkout") {
		t.Fatalf("cached menu = %q", m.editor.MenuView())
	}
	for i := range entitySearchCacheMax + 5 {
		m.storeEntitySearch(string(rune('a'+i)), response, time.Now())
	}
	if len(m.entitySearchCache) > entitySearchCacheMax {
		t.Fatalf("cache size = %d", len(m.entitySearchCache))
	}
}

func TestSearchFailureLeavesSubmissionUsable(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	searcher := &staticEntitySearcher{err: errors.New("search unavailable")}
	m := New(agent.New(backend, assistant.SendOptions{}), Config{EntitySearcher: searcher})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@missing entity"})
	debounce := m.syncEntitySearch()
	debounceMessage := debounce().(entitySearchDebounceMsg)
	result := m.beginEntitySearch(debounceMessage)().(entitySearchResultMsg)
	m.applyEntitySearchResult(result)
	if !strings.Contains(m.editor.MenuView(), "Entity search unavailable") {
		t.Fatalf("error menu = %q", m.editor.MenuView())
	}

	_, wait := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if wait == nil {
		t.Fatal("Enter did not submit from an error-only menu")
	}
	select {
	case <-backend.requests:
	case <-time.After(time.Second):
		t.Fatal("prompt submission did not reach backend")
	}
}

func TestSelectedEntityContextIsSentOnceAndClearedAfterSubmit(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 2)}
	m := New(agent.New(backend, assistant.SendOptions{}))
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@check"})
	_ = m.syncEntitySearch()
	m.applyEntitySearchResult(entitySearchResultMsg{
		generation: m.entitySearchGeneration,
		query:      "check",
		response: assistant.SearchEntitiesResponse{
			SearchFlowID: "flow-1",
			Entities: []assistant.SearchEntity{{
				CandidateID: "candidate-1", EntityID: "checkout-api",
				EntityType: "service", Name: "checkout-api",
			}},
		},
	})
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.editor.Attachments()) != 1 {
		t.Fatalf("selection did not attach entity: %#v", m.editor.Attachments())
	}
	m.editor.Update(tea.PasteMsg{Content: " inspect this"})
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	first := <-backend.requests
	if first.Context == nil || len(first.Context.Entities) != 1 {
		t.Fatalf("first request context = %#v", first.Context)
	}
	entity := first.Context.Entities[0]
	if entity.Type != "service" || entity.ID != "checkout-api" || entity.Label != "checkout-api" {
		t.Fatalf("first request entity = %#v", entity)
	}
	if len(m.editor.Attachments()) != 0 {
		t.Fatalf("attachments survived submit: %#v", m.editor.Attachments())
	}
	for range m.turnEvents {
	}
	_, _ = m.handleTurnClosed(turnClosedMsg{generation: m.turnGen})

	m.editor.Update(tea.PasteMsg{Content: "next independent turn"})
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	second := <-backend.requests
	if second.Context != nil {
		t.Fatalf("next turn reused context: %#v", second.Context)
	}
}

func TestEditingSelectedMentionRemovesStructuredContext(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	m := New(agent.New(backend, assistant.SendOptions{}))
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@check"})
	_ = m.syncEntitySearch()
	m.applyEntitySearchResult(entitySearchResultMsg{
		generation: m.entitySearchGeneration,
		query:      "check",
		response: assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
			CandidateID: "candidate-1", EntityID: "dashboard-1",
			EntityType: "dashboard", Title: "Test Dashboard",
		}}},
	})
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.editor.Value(); got != `@dashboard:"Test Dashboard" ` {
		t.Fatalf("selected mention = %q", got)
	}

	// Remove the trailing space and then change the quoted mention itself.
	m.editor.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.editor.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(m.editor.Attachments()) != 0 {
		t.Fatalf("edited mention retained context: %#v", m.editor.Attachments())
	}
	m.editor.CloseMenu()
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	request := <-backend.requests
	if request.Context != nil {
		t.Fatalf("edited mention sent structured context: %#v", request.Context)
	}
}

func TestEntityCandidatePresentationAndQuotedMention(t *testing.T) {
	response := assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
		CandidateID: "candidate-1", EntityID: "dashboard-1", EntityType: "dashboard",
		Title: "Test Dashboard", AuthorName: "Test User",
	}}}
	candidates := entityCandidates(response)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if got, want := candidates[0].Label, "◇ [Dashboard] Test Dashboard"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := candidates[0].Detail, ""; got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
	if got, want := candidates[0].Insert, `@dashboard:"Test Dashboard"`; got != want {
		t.Fatalf("insert = %q, want %q", got, want)
	}
}

func TestUnknownEntityCandidatePresentationEscapesQuotedMention(t *testing.T) {
	response := assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
		CandidateID: "candidate-1", EntityID: "custom-1", EntityType: "custom_widget",
		Name: `A "quoted" widget`,
	}}}
	candidate := entityCandidates(response)[0]
	if got, want := candidate.Label, `◇ [Custom Widget] A "quoted" widget`; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := candidate.Insert, `@custom_widget:"A \"quoted\" widget"`; got != want {
		t.Fatalf("insert = %q, want %q", got, want)
	}
}

func TestEntityCandidateSanitizesTerminalControls(t *testing.T) {
	unsafeLabel := "dashboard\x1b]52;c;Y2xpcGJvYXJk\a\nforged row"
	unsafeType := assistant.EntityType("dashboard\x1b[31m")
	candidate := entityCandidates(assistant.SearchEntitiesResponse{Entities: []assistant.SearchEntity{{
		CandidateID: "candidate-1", EntityID: "dashboard-1",
		EntityType: unsafeType, Title: unsafeLabel,
	}}})[0]

	for field, value := range map[string]string{"label": candidate.Label, "insert": candidate.Insert} {
		if strings.ContainsAny(value, "\x1b\a\n\r") {
			t.Fatalf("%s retained terminal controls: %q", field, value)
		}
	}
	if candidate.Attachment == nil || candidate.Attachment.ID != "dashboard-1" || candidate.Attachment.Type != string(unsafeType) {
		t.Fatalf("sanitization changed canonical identity: %#v", candidate.Attachment)
	}

	m := New(agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}))
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@dash"})
	m.editor.SetEntityResults("dash", editor.RemoteReady, []editor.Candidate{candidate})
	menu := m.editor.MenuView()
	if strings.Contains(menu, "\x1b]52;") || strings.Contains(ansi.Strip(menu), "\nforged row") {
		t.Fatalf("menu rendered untrusted control sequence: %q", menu)
	}
}
