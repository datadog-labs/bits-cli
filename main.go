package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	// Termination and hangup take the same path, so commands Bits started are
	// stopped rather than left running in their own process groups. Handling
	// an ignored hangup would undo nohup, so it stays ignored.
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	return cmd.Execute(ctx, args, os.Stdout, os.Stderr)
}
