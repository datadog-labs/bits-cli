package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui"
)

func main() {
	conversationID := flag.String("conversation", "",
		"resume an existing conversation by id: its history is restored before the prompt")
	flag.Parse()

	// BITS_FAKE_BACKEND streams seeded pseudo-random output with no network or
	// auth, for offline development and demos.
	var backend agent.Backend
	if os.Getenv("BITS_FAKE_BACKEND") == "1" {
		backend = fake.New()
	} else {
		client, err := assistant.NewClient()
		if err != nil {
			fmt.Fprintln(os.Stderr, "bits:", err)
			os.Exit(1)
		}
		backend = client
	}

	engine := agent.New(backend, assistant.SendOptions{ConversationID: *conversationID})
	p := tea.NewProgram(tui.New(engine))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		os.Exit(1)
	}
}
