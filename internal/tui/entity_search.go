package tui

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

const (
	entitySearchDebounce = 180 * time.Millisecond
	entitySearchCacheTTL = 30 * time.Second
	entitySearchCacheMax = 32
)

type entitySearchDebounceMsg struct {
	generation uint64
	query      string
}

type entitySearchResultMsg struct {
	generation uint64
	query      string
	response   assistant.SearchEntitiesResponse
	err        error
}

type entitySearchCacheEntry struct {
	response  assistant.SearchEntitiesResponse
	expiresAt time.Time
}

// syncEntitySearch reconciles the editor's active @ span with remote work.
// Every query change cancels the previous request and advances the generation;
// stopping advances it too, so a matching generation means this query is still
// the active one.
func (m *Model) syncEntitySearch() tea.Cmd {
	if m.searchesBlocked() {
		m.stopEntitySearch()
		return nil
	}
	query, active := m.editor.ActiveEntityQuery()
	if !active {
		m.stopEntitySearch()
		return nil
	}
	if m.entitySearchActive && query == m.entitySearchQuery {
		return nil
	}
	m.entitySearchTask.stop()
	m.entitySearchQuery = query
	m.entitySearchActive = true
	generation := m.entitySearchTask.gen

	if cached, ok := m.cachedEntitySearch(query, time.Now()); ok {
		m.editor.SetEntityResults(query, editor.RemoteReady, entityCandidates(cached))
		return nil
	}
	m.editor.SetEntityResults(query, editor.RemoteLoading, nil)
	return tea.Tick(entitySearchDebounce, func(time.Time) tea.Msg {
		return entitySearchDebounceMsg{generation: generation, query: query}
	})
}

func (m *Model) stopEntitySearch() {
	if !m.entitySearchActive && !m.entitySearchTask.running() {
		return
	}
	m.entitySearchTask.stop()
	m.entitySearchQuery = ""
	m.entitySearchActive = false
}

func (m *Model) beginEntitySearch(msg entitySearchDebounceMsg) tea.Cmd {
	if m.searchesBlocked() || msg.generation != m.entitySearchTask.gen {
		return nil
	}
	searcher := m.entitySearcher
	if searcher == nil {
		m.editor.SetEntityResults(msg.query, editor.RemoteError, nil)
		return nil
	}
	// The request gets its own generation; the debounce's is spent.
	ctx, generation := m.entitySearchTask.start(context.Background(), 0)
	sessionID := m.searchSessionID
	rawQuery, suggestionGroups := parseEntitySearchQuery(msg.query)
	return func() tea.Msg {
		// The command can be queued until after logout has canceled its context.
		if err := ctx.Err(); err != nil {
			return entitySearchResultMsg{generation: generation, query: msg.query, err: err}
		}
		response, err := searcher.SearchEntities(ctx, assistant.SearchEntitiesInput{
			SearchSessionID:  sessionID,
			RawQuery:         rawQuery,
			Limit:            10,
			SuggestionGroups: suggestionGroups,
		})
		return entitySearchResultMsg{
			generation: generation, query: msg.query,
			response: response, err: err,
		}
	}
}

// parseEntitySearchQuery translates @type:query syntax into the API's
// suggestion_groups filter. A space after the colon or a leading quote keeps
// the query literal, so @"service: Assistant API" still searches every type.
func parseEntitySearchQuery(query string) (string, []string) {
	groups := assistant.DefaultEntitySuggestionGroups
	if strings.HasPrefix(query, `"`) {
		return trimSearchQuotes(query), groups
	}

	prefix, remainder, found := strings.Cut(query, ":")
	if !found || (remainder != "" && unicode.IsSpace([]rune(remainder)[0])) {
		return query, groups
	}
	for _, group := range assistant.DefaultEntitySuggestionGroups {
		if strings.EqualFold(prefix, group) {
			return trimSearchQuotes(remainder), []string{group}
		}
	}
	return query, groups
}

func trimSearchQuotes(query string) string {
	quoted, ok := strings.CutPrefix(query, `"`)
	if !ok {
		return query
	}
	// Decode only the escapes emitted by entityMention. Autocomplete also
	// receives incomplete quotes/escapes, so a strict string decoder won't do.
	var decoded strings.Builder
	for i := 0; i < len(quoted); i++ {
		c := quoted[i]
		if c == '\\' && i+1 < len(quoted) && (quoted[i+1] == '\\' || quoted[i+1] == '"') {
			i++
			c = quoted[i]
		} else if c == '"' && i == len(quoted)-1 {
			break
		}
		decoded.WriteByte(c)
	}
	return decoded.String()
}

func (m *Model) applyEntitySearchResult(msg entitySearchResultMsg) {
	if m.searchesBlocked() || msg.generation != m.entitySearchTask.gen {
		return
	}
	m.entitySearchTask.done()
	if msg.err != nil {
		m.editor.SetEntityResults(msg.query, editor.RemoteError, nil)
		return
	}
	m.storeEntitySearch(msg.query, msg.response, time.Now())
	m.editor.SetEntityResults(msg.query, editor.RemoteReady, entityCandidates(msg.response))
}

func entityCandidates(response assistant.SearchEntitiesResponse) []editor.Candidate {
	items := make([]editor.Candidate, 0, len(response.Entities))
	for _, entity := range response.Entities {
		label := entity.DisplayLabel()
		displayLabel := escape.SingleLine(label)
		if displayLabel == "" {
			displayLabel = escape.SingleLine(entity.EntityID)
		}
		if displayLabel == "" {
			displayLabel = "unknown entity"
		}
		displayType := escape.SingleLine(string(entity.EntityType))
		typeName := displayEntityType(displayType)
		id := entity.CandidateID
		if id == "" {
			id = string(entity.EntityType) + ":" + entity.EntityID
		}
		attachment := editor.Attachment{
			Type: string(entity.EntityType), ID: entity.EntityID, Label: label,
			CandidateID: entity.CandidateID, SearchFlowID: response.SearchFlowID,
			Rank: entity.Rank, RankScore: entity.RankScore,
			TrackingAttributes: response.TrackingAttributes,
		}
		items = append(items, editor.Candidate{
			Kind: editor.CandidateEntity, ID: id,
			Label:  "◇ [" + typeName + "] " + displayLabel,
			Insert: entityMention(displayType, displayLabel), Attachment: &attachment,
		})
	}
	return items
}

func displayEntityType(entityType string) string {
	words := strings.Fields(strings.ReplaceAll(entityType, "_", " "))
	for i, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			words[i] = string(unicode.ToUpper(runes[0])) + string(runes[1:])
		}
	}
	if len(words) == 0 {
		return "Entity"
	}
	return strings.Join(words, " ")
}

func entityMention(entityType, label string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	entityType = replacer.Replace(entityType)
	label = replacer.Replace(label)
	return "@" + entityType + `:"` + label + `"`
}

func (m *Model) cachedEntitySearch(query string, now time.Time) (assistant.SearchEntitiesResponse, bool) {
	entry, ok := m.entitySearchCache[query]
	if !ok {
		return assistant.SearchEntitiesResponse{}, false
	}
	if !now.Before(entry.expiresAt) {
		delete(m.entitySearchCache, query)
		m.removeEntitySearchCacheOrder(query)
		return assistant.SearchEntitiesResponse{}, false
	}
	return entry.response, true
}

func (m *Model) storeEntitySearch(query string, response assistant.SearchEntitiesResponse, now time.Time) {
	m.removeEntitySearchCacheOrder(query)
	m.entitySearchCacheOrder = append(m.entitySearchCacheOrder, query)
	m.entitySearchCache[query] = entitySearchCacheEntry{response: response, expiresAt: now.Add(entitySearchCacheTTL)}
	for len(m.entitySearchCacheOrder) > entitySearchCacheMax {
		oldest := m.entitySearchCacheOrder[0]
		m.entitySearchCacheOrder = m.entitySearchCacheOrder[1:]
		delete(m.entitySearchCache, oldest)
	}
}

func (m *Model) removeEntitySearchCacheOrder(query string) {
	for index, cachedQuery := range m.entitySearchCacheOrder {
		if cachedQuery == query {
			m.entitySearchCacheOrder = append(m.entitySearchCacheOrder[:index], m.entitySearchCacheOrder[index+1:]...)
			return
		}
	}
}

var fallbackSearchID atomic.Uint64

func newSearchSessionID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		binary.BigEndian.PutUint64(value[0:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(value[8:16], fallbackSearchID.Add(1))
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func contextFromAttachments(attachments []editor.Attachment) *assistant.AssistantContext {
	if len(attachments) == 0 {
		return nil
	}
	entities := make([]assistant.ContextEntity, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.ID) == "" || strings.TrimSpace(attachment.Type) == "" {
			continue
		}
		entities = append(entities, assistant.ContextEntity{
			Type:  assistant.EntityType(attachment.Type),
			ID:    attachment.ID,
			Label: attachment.Label,
		})
	}
	if len(entities) == 0 {
		return nil
	}
	return &assistant.AssistantContext{Entities: entities}
}
