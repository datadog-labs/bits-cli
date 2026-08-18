package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui"
)

func main() {
	client, err := assistant.NewClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		os.Exit(1)
	}

	// Slack profile: chat-oriented surface (no widgets, absolute links), which
	// suits a terminal better than the widget-happy web UI default.
	engine := agent.New(client, assistant.SendOptions{Profile: assistant.ProfileSlack})

	p := tea.NewProgram(tui.New(engine), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		os.Exit(1)
	}
}
