package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSearchEntitiesRequestContractAndAPIKeyAuth(t *testing.T) {
	var got entitySearchRequest
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != entitiesSuggestionsPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("X-Datadog-Bits-Surface"); got != "cli" {
			t.Fatalf("surface = %q", got)
		}
		if r.Header.Get("DD-API-KEY") != "api" || r.Header.Get("DD-APPLICATION-KEY") != "app" {
			t.Fatalf("API/app key headers missing")
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("API/app-key request carried a bearer token")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"data":{"id":"flow","type":"future_response_type","attributes":{"entities":[],"activeSuggestionGroups":["service"]}}}`)
	})

	response, err := c.SearchEntities(context.Background(), SearchEntitiesInput{
		SearchSessionID: "session-uuid", RawQuery: "checkout api",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []string{
		"dashboard", "incident", "monitor", "notebook", "team", "user",
		"service", "integration", "workflow", "synthetic_test", "resource",
		"spreadsheet", "app",
	}
	want := entitySearchRequest{Data: entitySearchRequestData{
		Type: "entities_search_request",
		Attributes: entitySearchRequestAttributes{
			ID: "session-uuid", RawQuery: "checkout api", Limit: 10,
			Product: "assistant", Context: "input-text-autocomplete",
			SuggestionGroups: wantGroups,
			UserContext:      entitySearchUserContext{RecentSearches: []any{}},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
	if response.SearchFlowID != "flow" || !reflect.DeepEqual(response.ActiveSuggestionGroups, []string{"service"}) {
		t.Fatalf("response metadata = %#v", response)
	}
}

func TestSearchEntitiesUsesOAuthBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.Header.Get("DD-API-KEY") != "" || r.Header.Get("DD-APPLICATION-KEY") != "" {
			t.Fatal("OAuth request carried API/app keys")
		}
		_, _ = fmt.Fprint(w, `{"data":{"attributes":{"entities":[]}}}`)
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL, TokenSource: staticAccessToken("oauth-token"), HTTPClient: server.Client()}
	if _, err := c.SearchEntities(context.Background(), SearchEntitiesInput{}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchEntitiesDecodesMixedAndUnknownEntities(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"data":{"id":"flow-1","type":"not_assumed","attributes":{"entities":[
				{"id":"candidate-dashboard","entityId":"dash-1","entityType":"dashboard","title":"Checkout overview","authorName":"Ada","url":"/dashboard/dash-1","rankScore":7.5},
				{"id":"candidate-monitor","entityId":"123","entityType":"monitor","name":"Checkout latency","creatorName":"Lin"},
				{"id":"candidate-future","entityId":"future-9","entityType":"new_hotness","text":"Future object","specialField":{"nested":true}}
			],"activeSuggestionGroups":["dashboard","monitor"],"trackingAttributes":{"model":"ranker-v2"}}}
		}`)
	})
	response, err := c.SearchEntities(context.Background(), SearchEntitiesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Entities) != 3 {
		t.Fatalf("entities = %#v", response.Entities)
	}
	unknown := response.Entities[2]
	if unknown.EntityType != "new_hotness" || unknown.Rank != 2 || unknown.DisplayLabel() != "Future object" || unknown.DisplayDetail() != "new hotness" {
		t.Fatalf("unknown entity = %#v label=%q detail=%q", unknown, unknown.DisplayLabel(), unknown.DisplayDetail())
	}
	if _, ok := unknown.Fields["specialField"]; !ok {
		t.Fatal("unknown type-specific field was not preserved")
	}
	if response.TrackingAttributes["model"] != "ranker-v2" {
		t.Fatalf("tracking attributes = %#v", response.TrackingAttributes)
	}
}

func TestSearchEntityDisplayLabelsAndDetails(t *testing.T) {
	installed := true
	serviceAccount := true
	tests := []struct {
		entity     SearchEntity
		wantLabel  string
		wantDetail string
	}{
		{SearchEntity{EntityType: "incident", EntityID: "42", Title: "Checkout down", Severity: "SEV-1", State: "active"}, "IR-42: Checkout down", "incident · SEV-1 · active"},
		{SearchEntity{EntityType: "monitor", EntityID: "7", Name: "Latency", CreatorName: "Ada"}, "Latency", "monitor · Ada"},
		{SearchEntity{EntityType: "service", EntityID: "checkout", Name: "checkout", ProductAreas: []string{"APM", "Logs"}}, "checkout", "service · APM, Logs"},
		{SearchEntity{EntityType: "spreadsheet", EntityID: "s1", Title: "SLOs", AuthorName: "Lin"}, "SLOs", "spreadsheet · Lin"},
		{SearchEntity{EntityType: "integration", EntityID: "github", Name: "GitHub", IsInstalled: &installed}, "GitHub", "integration · installed"},
		{SearchEntity{EntityType: "user", EntityID: "u1", Name: "Bot", Email: "bot@example.com", IsServiceAccount: &serviceAccount}, "Bot", "user · bot@example.com · service account"},
		{SearchEntity{EntityType: "app", EntityID: "app-1"}, "app-1", "app"},
	}
	for _, test := range tests {
		if got := test.entity.DisplayLabel(); got != test.wantLabel {
			t.Errorf("%s label = %q, want %q", test.entity.EntityType, got, test.wantLabel)
		}
		if got := test.entity.DisplayDetail(); got != test.wantDetail {
			t.Errorf("%s detail = %q, want %q", test.entity.EntityType, got, test.wantDetail)
		}
	}
}

func TestSearchEntitiesEmptyPermissionFilteredResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"id":"flow","type":"search_entities","attributes":{"entities":[],"activeSuggestionGroups":[]}}}`)
	})
	response, err := c.SearchEntities(context.Background(), SearchEntitiesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Entities) != 0 || len(response.ActiveSuggestionGroups) != 0 {
		t.Fatalf("permission-filtered response = %#v", response)
	}
}

func TestSearchEntitiesHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.SearchEntities(ctx, SearchEntitiesInput{})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchEntities error = %v", err)
	}
}

func TestSendIncludesStructuredEntityContext(t *testing.T) {
	var request Request
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		writeStream(t, w)
	})
	want := &AssistantContext{Entities: []ContextEntity{{
		Type: "dashboard", ID: "dash-1", Label: "Checkout overview",
	}}}
	if _, err := c.Send(context.Background(), "inspect this", SendOptions{Context: want}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Data.Attributes.Context, want) {
		t.Fatalf("request context = %#v, want %#v", request.Data.Attributes.Context, want)
	}
}
