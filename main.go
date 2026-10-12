package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/datadog-labs/bits-cli/internal/cmd"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		var exitErr *cmd.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(cmd.ExitFailure)
	}
}

func run(args []string) error {
	// Ctrl-C cancels the active command so headless delivery can finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return cmd.Execute(ctx, args, os.Stdout, os.Stderr)
}
