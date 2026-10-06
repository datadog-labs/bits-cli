// Command style-guide opens the dev-only style catalog: a Bubble Tea page that
// renders shared components, text attributes, semantic roles, markdown elements,
// and color tokens at live terminal width with a dark/light toggle. It
// touches no engine, backend, network, or auth.
//
// Run it with:
//
//	go run ./tools/style-guide
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/tools/style-guide/catalog"
)

func main() {
	if _, err := tea.NewProgram(catalog.New()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "style-guide:", err)
		os.Exit(1)
	}
}
