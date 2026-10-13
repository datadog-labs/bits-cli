package components

import (
	"testing"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

func TestHiddenChipsCannotBeClicked(t *testing.T) {
	chips := []Chip{
		{Type: "service", Label: "checkout-api", Removable: true},
		{Type: "service", Label: "payments-api", Removable: true},
		{Type: "service", Label: "inventory", Removable: true},
	}
	placed := LayoutChips(styles.Default(true).Input.Chip, chips, 30, 1, -1)

	last := placed[len(placed)-1]
	if last.Index != len(chips) || last.Row != 0 {
		t.Fatalf("expected a +N more token on the only row, got %+v", placed)
	}
	for _, p := range placed {
		if p.X+p.Width > 30 {
			t.Fatalf("chip %d overflows the row: %+v", p.Index, p)
		}
	}
	for x := range 30 {
		if index, ok := ChipAt(placed, x, 0); ok && index > 0 && index < len(chips) {
			t.Fatalf("hidden chip %d is clickable at cell %d", index, x)
		}
	}
}
