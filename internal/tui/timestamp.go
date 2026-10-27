package tui

import (
	"slices"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// responseTimestamp anchors a run's finish instant to the last block that run
// touched, so the renderer can place a "Done · <time>" line right under it.
type responseTimestamp struct {
	After agent.BlockID
	At    time.Time
}

// responseTimestamps resolves m.runFinished into anchors, in block order.
// Restored history (Run == 0) never anchors a line, and a run whose blocks no
// longer survive in m.state.Blocks is omitted entirely rather than anchored
// to a zero BlockID, which would render before every block.
func (m *Model) responseTimestamps() []responseTimestamp {
	// One pass over the blocks keeps the last index seen per RunID, since a
	// run can touch several blocks and only the last one should anchor it.
	lastIndex := make(map[agent.RunID]int, len(m.runFinished))
	for i, block := range m.state.Blocks {
		if block.Run == 0 {
			continue
		}
		if _, tracked := m.runFinished[block.Run]; !tracked {
			continue
		}
		lastIndex[block.Run] = i
	}

	type anchored struct {
		index int
		ts    responseTimestamp
	}
	anchors := make([]anchored, 0, len(lastIndex))
	for run, idx := range lastIndex {
		anchors = append(anchors, anchored{
			index: idx,
			ts: responseTimestamp{
				After: m.state.Blocks[idx].ID,
				At:    m.runFinished[run],
			},
		})
	}
	slices.SortFunc(anchors, func(a, b anchored) int { return a.index - b.index })

	timestamps := make([]responseTimestamp, len(anchors))
	for i, a := range anchors {
		timestamps[i] = a.ts
	}
	return timestamps
}
