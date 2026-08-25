// Command style-guide opens the dev-only style catalog: a Bubble Tea page that
// renders every text attribute, semantic style role, markdown element, and color
// token the chat TUI uses, at live terminal width with a dark/light toggle. It
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

	"github.com/DataDog/bits-cli/tools/style-guide/catalog"
)

func main() {
	if _, err := tea.NewProgram(catalog.New()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "style-guide:", err)
		os.Exit(1)
	}
}
