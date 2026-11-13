package tui

import (
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// Replay gives locally minted user blocks server IDs. Reanchor session notices
// before replacing the transcript, using surviving blocks to bound each match.
func (m *Model) reanchorActivityNotices(blocks []agent.Block) {
	if len(m.notices) == 0 {
		return
	}
	positions := make(map[agent.BlockID]int, len(blocks))
	for i, b := range blocks {
		positions[b.ID] = i
	}
	old := m.state.Blocks
	limits := make([]int, len(old))
	limit := len(blocks)
	for i := len(old) - 1; i >= 0; i-- {
		limits[i] = limit
		if at, ok := positions[old[i].ID]; ok {
			limit = at
		}
	}
	anchors := make(map[agent.BlockID]agent.BlockID, len(old))
	var previous agent.BlockID
	cursor := 0
	for i, b := range old {
		if at, ok := positions[b.ID]; ok {
			previous = b.ID
			cursor = at + 1
		} else if b.ID.Scope == agent.ScopeLocal && b.Role == assistant.RoleUser && b.Markdown != nil {
			for j := cursor; j < limits[i]; j++ {
				candidate := blocks[j]
				if candidate.Role == b.Role && candidate.Markdown != nil && candidate.Markdown.Content == b.Markdown.Content {
					previous = candidate.ID
					cursor = j + 1
					break
				}
			}
		}
		anchors[b.ID] = previous
	}
	for i := range m.notices {
		if _, exists := positions[m.notices[i].After]; exists {
			continue
		}
		if after, ok := anchors[m.notices[i].After]; ok {
			m.notices[i].After = after
		}
	}
}
