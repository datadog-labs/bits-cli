// Package cmd is the process-level command surface for bits. It owns the
// Cobra command tree, generated help, and dispatch to caller-supplied
// operations, keeping the command interface independent of chat and TUI
// implementations.
package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// ChatOptions carries the user-supplied root command flags resolved before
// chat starts.
type ChatOptions struct {
	ConversationID string
	AuthMode       auth.Mode
	Site           string
}

// LoginOptions carries the user-supplied login flags resolved by the command
// tree before dispatch.
type LoginOptions struct {
	Site     string
	ClientID string
}

// Actions binds command dispatch to caller-owned operations. The command tree
// invokes these without depending on their implementations.
type Actions struct {
	Chat   func(ctx context.Context, opts ChatOptions) error
	Login  func(ctx context.Context, opts LoginOptions) error
	Logout func(ctx context.Context) error
}

// Execute builds a fresh command tree for args and runs it with ctx, writing
// output to stdout and stderr. A new tree per call keeps parsing state from
// leaking between runs or tests.
func Execute(ctx context.Context, args []string, actions Actions, stdout, stderr io.Writer) error {
	root := newRootCommand(actions)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

func newRootCommand(actions Actions) *cobra.Command {
	opts := ChatOptions{AuthMode: auth.ModeAuto}
	var authMode string
	// Keep root.Args nil so Cobra can identify unknown commands, suggest close
	// matches, and reject unknown help topics during command discovery. The
	// RunE check handles arguments after a -- terminator.
	root := &cobra.Command{
		Use:           "bits",
		Short:         "Datadog Assistant in your terminal",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, args []string) error {
			if err := cobra.NoArgs(command, args); err != nil {
				return err
			}
			mode, err := parseAuthenticationMode(authMode)
			if err != nil {
				return err
			}
			siteSet := command.Flags().Changed("site") && strings.TrimSpace(opts.Site) != ""
			switch {
			case mode == auth.ModeAPIKey && !siteSet:
				return fmt.Errorf("--auth %s requires --site", auth.ModeAPIKey)
			case mode == auth.ModeAuto && command.Flags().Changed("site"):
				return fmt.Errorf("--site requires --auth %s; use `bits login --site` for OAuth", auth.ModeAPIKey)
			}
			opts.AuthMode = mode
			return actions.Chat(command.Context(), opts)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(command *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, command.CommandPath())
	})
	root.Flags().StringVar(&authMode, "auth", string(auth.ModeAuto), "authentication mode: auto or api-key")
	root.Flags().StringVar(&opts.Site, "site", "", "Datadog API site for api-key authentication")
	root.Flags().StringVar(
		&opts.ConversationID,
		"conversation",
		"",
		"resume an existing conversation by ID",
	)
	root.AddCommand(newLoginCommand(actions.Login), newLogoutCommand(actions.Logout))
	return root
}

func newLoginCommand(action func(context.Context, LoginOptions) error) *cobra.Command {
	opts := LoginOptions{Site: auth.DefaultSite}
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Datadog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return action(command.Context(), opts)
		},
	}
	command.Flags().StringVar(&opts.Site, "site", opts.Site, "Datadog site URL or hostname")
	command.Flags().StringVar(&opts.ClientID, "client-id", opts.ClientID, "OAuth client ID override")
	return command
}

func parseAuthenticationMode(raw string) (auth.Mode, error) {
	mode := auth.Mode(raw)
	switch mode {
	case auth.ModeAuto, auth.ModeAPIKey:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid authentication mode %q; expected auto or api-key", raw)
	}
}

func newLogoutCommand(action func(context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out of Datadog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return action(command.Context())
		},
	}
}
