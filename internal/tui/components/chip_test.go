package components

import (
	"testing"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

func TestChipsThatDoNotFitAreCounted(t *testing.T) {
	sty := styles.Default(true).Input.Chip
	var chips []Chip
	for _, label := range []string{"checkout-api", "payments-api", "inventory", "shipping", "billing", "search"} {
		chips = append(chips, Chip{Type: "service", Label: label, Removable: true})
	}
	for _, tt := range []struct {
		name     string
		maxRows  int
		placed   []PlacedChip
		countHit bool // whether clicking the count toggles the rows
	}{
		{name: "collapsed into +N more", maxRows: 1, placed: LayoutChips(sty, chips, 30, 1, -1), countHit: true},
		{name: "expanded within its rows", maxRows: 2, placed: LayoutExpandedChips(sty, chips, 30, 2, -1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			shown := 0
			for _, p := range tt.placed {
				if p.Row >= tt.maxRows || p.X+p.Width > 30 {
					t.Fatalf("%+v is outside %d rows of 30 cells", p, tt.maxRows)
				}
				if p.Index >= 0 && p.Index < len(chips) {
					shown++
				}
			}
			if shown == len(chips) {
				t.Fatal("fixture should not fit")
			}
			count := tt.placed[len(tt.placed)-1]
			if !tt.countHit {
				count = tt.placed[len(tt.placed)-2]
			}
			index, ok := ChipAt(tt.placed, count.X, count.Row)
			if ok != tt.countHit || (ok && index != len(chips)) {
				t.Fatalf("clicking the count %q gives %d, %v", count.View, index, ok)
			}
		})
	}
}
