package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const entitiesSuggestionsPath = "/api/ui/search/suggestions/entities"

// DefaultEntitySuggestionGroups matches the entity groups used by the web
// Assistant. The response remains open to other entity types.
var DefaultEntitySuggestionGroups = []string{
	"dashboard",
	"incident",
	"monitor",
	"notebook",
	"team",
	"user",
	"service",
	"integration",
	"workflow",
	"synthetic_test",
	"resource",
	"spreadsheet",
	"app",
}

// SearchEntitiesInput configures one Assistant autocomplete query.
type SearchEntitiesInput struct {
	SearchSessionID  string
	RawQuery         string
	Limit            int
	SuggestionGroups []string
}

type entitySearchRequest struct {
	Data entitySearchRequestData `json:"data"`
}

type entitySearchRequestData struct {
	Type       string                        `json:"type"`
	Attributes entitySearchRequestAttributes `json:"attributes"`
}

type entitySearchRequestAttributes struct {
	ID               string                  `json:"id"`
	RawQuery         string                  `json:"raw_query"`
	Limit            int                     `json:"limit"`
	Product          string                  `json:"product"`
	Context          string                  `json:"context"`
	SuggestionGroups []string                `json:"suggestion_groups"`
	UserContext      entitySearchUserContext `json:"userContext"`
}

type entitySearchUserContext struct {
	RecentSearches []any `json:"recentSearches"`
}

// SearchEntitiesResponse is the usable subset of an entity suggestions
// response. SearchFlowID, CandidateID, rank, and tracking fields are retained
// so an approved CLI feedback path can use them later.
// TODO(BCLI-10): send impression and selection feedback only after Bits CLI has
// an approved attribution transport. Browser RUM is not available here.
type SearchEntitiesResponse struct {
	SearchFlowID           string
	Entities               []SearchEntity
	ActiveSuggestionGroups []string
	TrackingAttributes     map[string]string
}

// SearchEntity is intentionally an open model. Common fields are typed for
// display, while Fields preserves type-specific and future response fields.
type SearchEntity struct {
	CandidateID      string
	EntityID         string
	EntityType       EntityType
	Rank             int
	RankScore        *float64
	Name             string
	Title            string
	Text             string
	URL              string
	ProductAreas     []string
	Severity         string
	State            string
	AuthorName       string
	AuthorHandle     string
	CreatorName      string
	Email            string
	Category         string
	IsInstalled      *bool
	IsServiceAccount *bool
	Fields           map[string]json.RawMessage
}

func (e *SearchEntity) UnmarshalJSON(data []byte) error {
	type wireEntity struct {
		ID               string     `json:"id"`
		EntityID         string     `json:"entityId"`
		EntityType       EntityType `json:"entityType"`
		RankScore        *float64   `json:"rankScore"`
		Name             string     `json:"name"`
		Title            string     `json:"title"`
		Text             string     `json:"text"`
		URL              string     `json:"url"`
		ProductAreas     []string   `json:"productAreas"`
		Severity         string     `json:"severity"`
		State            string     `json:"state"`
		AuthorName       string     `json:"authorName"`
		AuthorHandle     string     `json:"authorHandle"`
		CreatorName      string     `json:"creatorName"`
		Email            string     `json:"email"`
		Category         string     `json:"category"`
		IsInstalled      *bool      `json:"isInstalled"`
		IsServiceAccount *bool      `json:"isServiceAccount"`
	}
	var wire wireEntity
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*e = SearchEntity{
		CandidateID: wire.ID, EntityID: wire.EntityID, EntityType: wire.EntityType,
		RankScore: wire.RankScore, Name: wire.Name, Title: wire.Title, Text: wire.Text,
		URL: wire.URL, ProductAreas: wire.ProductAreas, Severity: wire.Severity,
		State: wire.State, AuthorName: wire.AuthorName, AuthorHandle: wire.AuthorHandle,
		CreatorName: wire.CreatorName, Email: wire.Email, Category: wire.Category,
		IsInstalled: wire.IsInstalled, IsServiceAccount: wire.IsServiceAccount,
		Fields: fields,
	}
	return nil
}

// DisplayLabel derives human-readable text without assuming every entity has
// a name. The canonical entity ID is the final fallback.
func (e SearchEntity) DisplayLabel() string {
	label := firstNonBlank(e.Name, e.Title, e.Text, e.EntityID)
	if e.EntityType == EntityIncident && e.Title != "" && e.EntityID != "" {
		return "IR-" + e.EntityID + ": " + e.Title
	}
	return label
}

// DisplayDetail includes the open entity type and useful disambiguating data.
func (e SearchEntity) DisplayDetail() string {
	parts := []string{humanizeEntityType(string(e.EntityType))}
	switch string(e.EntityType) {
	case "dashboard", "notebook", "spreadsheet":
		parts = appendNonBlank(parts, e.AuthorName, e.AuthorHandle)
	case "monitor":
		parts = appendNonBlank(parts, e.CreatorName)
	case "incident":
		parts = appendNonBlank(parts, e.Severity, e.State)
	case "service":
		if len(e.ProductAreas) > 0 {
			parts = append(parts, strings.Join(e.ProductAreas, ", "))
		}
	case "user":
		parts = appendNonBlank(parts, e.Email)
		if e.IsServiceAccount != nil && *e.IsServiceAccount {
			parts = append(parts, "service account")
		}
	case "integration":
		if e.IsInstalled != nil {
			if *e.IsInstalled {
				parts = append(parts, "installed")
			} else {
				parts = append(parts, "available")
			}
		}
	case "page":
		parts = appendNonBlank(parts, e.Category)
	}
	if len(parts) == 1 {
		parts = appendNonBlank(parts, e.URL)
	}
	return strings.Join(parts, " · ")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown entity"
}

func appendNonBlank(parts []string, values ...string) []string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	return parts
}

func humanizeEntityType(value string) string {
	if value == "" {
		return "Datadog entity"
	}
	return strings.ReplaceAll(value, "_", " ")
}

// SearchEntities queries the same entity suggestions route as the web
// Assistant. The POST is not retried, which makes caller cancellation and
// supersession deterministic.
func (c *Client) SearchEntities(ctx context.Context, in SearchEntitiesInput) (SearchEntitiesResponse, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	groups := in.SuggestionGroups
	if groups == nil {
		groups = DefaultEntitySuggestionGroups
	}
	body := entitySearchRequest{Data: entitySearchRequestData{
		Type: "entities_search_request",
		Attributes: entitySearchRequestAttributes{
			ID: in.SearchSessionID, RawQuery: in.RawQuery, Limit: limit,
			Product: "assistant", Context: "input-text-autocomplete",
			SuggestionGroups: append([]string(nil), groups...),
			UserContext:      entitySearchUserContext{RecentSearches: []any{}},
		},
	}}
	req, err := c.newRequest(ctx, http.MethodPost, entitiesSuggestionsPath, body)
	if err != nil {
		return SearchEntitiesResponse{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return SearchEntitiesResponse{}, ctx.Err()
		}
		return SearchEntitiesResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		apiErr := httpError(snippet, resp.StatusCode, http.MethodPost, entitiesSuggestionsPath)
		var rejectErr error
		if resp.StatusCode == http.StatusUnauthorized {
			rejectErr = c.rejectAccessToken(ctx, req)
		}
		return SearchEntitiesResponse{}, errors.Join(apiErr, rejectErr)
	}
	var wire struct {
		Data *struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Attributes *struct {
				Entities               []SearchEntity    `json:"entities"`
				ActiveSuggestionGroups []string          `json:"activeSuggestionGroups"`
				TrackingAttributes     map[string]string `json:"trackingAttributes"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := decodeJSONBody(&wire, resp.Body, maxResponseBodyBytes); err != nil {
		return SearchEntitiesResponse{}, fmt.Errorf("decode entity suggestions: %w", err)
	}
	if wire.Data == nil || wire.Data.Attributes == nil {
		return SearchEntitiesResponse{}, nil
	}
	for rank := range wire.Data.Attributes.Entities {
		wire.Data.Attributes.Entities[rank].Rank = rank
	}
	return SearchEntitiesResponse{
		SearchFlowID:           wire.Data.ID,
		Entities:               wire.Data.Attributes.Entities,
		ActiveSuggestionGroups: wire.Data.Attributes.ActiveSuggestionGroups,
		TrackingAttributes:     wire.Data.Attributes.TrackingAttributes,
	}, nil
}
