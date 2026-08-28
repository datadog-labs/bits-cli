// Package cmd is the process-level command surface for bits. It owns the
// Cobra command tree, generated help, and dispatch to caller-supplied
// operations, keeping dependencies on chat, auth, and the TUI out of this
// layer so the command interface can grow independently.
package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// LoginOptions carries the user-supplied login flags resolved by the command
// tree before dispatch.
type LoginOptions struct {
	Site     string
	ClientID string
}

// Defaults holds environment-derived defaults used to seed command flags. A
// fresh value is built for each execution so state does not leak between runs.
type Defaults struct {
	Site     string
	ClientID string
}

// Actions binds command dispatch to caller-owned operations. The command tree
// never imports chat, auth, or TUI packages directly; it invokes these.
type Actions struct {
	Chat   func(ctx context.Context, conversationID string) error
	Login  func(ctx context.Context, opts LoginOptions) error
	Logout func(ctx context.Context) error
}

// Execute builds a fresh command tree for args and runs it with ctx, writing
// output to stdout and stderr. A new tree per call keeps parsing state and
// environment-derived defaults from leaking between runs or tests.
func Execute(ctx context.Context, args []string, actions Actions, defaults Defaults, stdout, stderr io.Writer) error {
	root := newRootCommand(actions, defaults)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

func newRootCommand(actions Actions, defaults Defaults) *cobra.Command {
	var conversationID string
	// Keep root.Args nil so Cobra can identify unknown commands, suggest close
	// matches, and reject unknown help topics during command discovery. The
	// RunE check handles arguments after a -- terminator.
	root := &cobra.Command{
		Use:           "bits",
		Short:         "Datadog Assistant in your terminal",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			return actions.Chat(cmd.Context(), conversationID)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, cmd.CommandPath())
	})
	root.Flags().StringVar(
		&conversationID,
		"conversation",
		"",
		"resume an existing conversation by ID",
	)
	root.AddCommand(newLoginCommand(actions.Login, defaults), newLogoutCommand(actions.Logout))
	return root
}

func newLoginCommand(action func(context.Context, LoginOptions) error, defaults Defaults) *cobra.Command {
	opts := LoginOptions{
		Site:     defaultLoginSite(defaults.Site),
		ClientID: defaults.ClientID,
	}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Datadog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return action(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Site, "site", opts.Site, "Datadog site URL or hostname")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", opts.ClientID, "OAuth client ID override")
	return cmd
}

func newLogoutCommand(action func(context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out of Datadog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return action(cmd.Context())
		},
	}
}

// defaultLoginSite resolves the configured site, falling back to the canonical
// Datadog site when none is provided.
func defaultLoginSite(configuredSite string) string {
	if configuredSite != "" {
		return configuredSite
	}
	return auth.DefaultSite
}
