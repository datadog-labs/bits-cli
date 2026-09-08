package tui

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/editor"
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
// Every query change cancels the previous request and advances the generation.
func (m *Model) syncEntitySearch() tea.Cmd {
	query, active := m.editor.ActiveEntityQuery()
	if !active {
		m.stopEntitySearch()
		return nil
	}
	if m.entitySearchActive && query == m.entitySearchQuery {
		return nil
	}
	if m.entitySearchCancel != nil {
		m.entitySearchCancel()
		m.entitySearchCancel = nil
	}
	m.entitySearchGeneration++
	m.entitySearchQuery = query
	m.entitySearchActive = true
	generation := m.entitySearchGeneration

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
	if !m.entitySearchActive && m.entitySearchCancel == nil {
		return
	}
	m.entitySearchGeneration++
	m.entitySearchQuery = ""
	m.entitySearchActive = false
	if m.entitySearchCancel != nil {
		m.entitySearchCancel()
		m.entitySearchCancel = nil
	}
}

func (m *Model) beginEntitySearch(msg entitySearchDebounceMsg) tea.Cmd {
	if msg.generation != m.entitySearchGeneration || msg.query != m.entitySearchQuery {
		return nil
	}
	searcher := m.entitySearcher
	if searcher == nil {
		m.editor.SetEntityResults(msg.query, editor.RemoteError, nil)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.entitySearchCancel = cancel
	sessionID := m.searchSessionID
	return func() tea.Msg {
		response, err := searcher.SearchEntities(ctx, assistant.SearchEntitiesInput{
			SearchSessionID:  sessionID,
			RawQuery:         msg.query,
			Limit:            10,
			SuggestionGroups: assistant.DefaultEntitySuggestionGroups,
		})
		return entitySearchResultMsg{
			generation: msg.generation, query: msg.query,
			response: response, err: err,
		}
	}
}

func (m *Model) applyEntitySearchResult(msg entitySearchResultMsg) {
	if msg.generation != m.entitySearchGeneration || msg.query != m.entitySearchQuery {
		return
	}
	if m.entitySearchCancel != nil {
		m.entitySearchCancel()
		m.entitySearchCancel = nil
	}
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
			Label: "DD " + label, Detail: entity.DisplayDetail(),
			Insert: "@" + label, Attachment: &attachment,
		})
	}
	return items
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

func batchCommands(commands ...tea.Cmd) tea.Cmd {
	nonNil := commands[:0]
	for _, command := range commands {
		if command != nil {
			nonNil = append(nonNil, command)
		}
	}
	switch len(nonNil) {
	case 0:
		return nil
	case 1:
		return nonNil[0]
	default:
		return tea.Batch(nonNil...)
	}
}
