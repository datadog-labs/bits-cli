package chat

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestStylesForCarriesAccordionTokens(t *testing.T) {
	theme := styles.Default(true)
	got := StylesFor(theme)
	if got.Accordion.Expanded != theme.Accordion.Expanded || got.Accordion.Collapsed != theme.Accordion.Collapsed {
		t.Fatalf("StylesFor dropped the Accordion tokens: got %+v, want %+v", got.Accordion, theme.Accordion)
	}
}
