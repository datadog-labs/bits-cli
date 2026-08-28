package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

type loginCommandOptions struct {
	Site     string
	ClientID string
}

type commandDefaults struct {
	Site     string
	ClientID string
}

type commandActions struct {
	chat   func(context.Context, string) error
	login  func(context.Context, loginCommandOptions) error
	logout func(context.Context) error
}

func run(args []string) error {
	return executeCommands(
		context.Background(),
		args,
		commandActions{
			chat:   runChat,
			login:  runLogin,
			logout: runLogout,
		},
		commandDefaults{
			Site:     os.Getenv("DD_SITE_URL"),
			ClientID: os.Getenv("BITS_OAUTH_CLIENT_ID"),
		},
		os.Stdout,
		os.Stderr,
	)
}

func executeCommands(
	ctx context.Context,
	args []string,
	actions commandActions,
	defaults commandDefaults,
	stdout, stderr io.Writer,
) error {
	root := newRootCommand(actions, defaults)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

func newRootCommand(actions commandActions, defaults commandDefaults) *cobra.Command {
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
			return actions.chat(cmd.Context(), conversationID)
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
	root.AddCommand(newLoginCommand(actions.login, defaults), newLogoutCommand(actions.logout))
	return root
}

func newLoginCommand(action func(context.Context, loginCommandOptions) error, defaults commandDefaults) *cobra.Command {
	opts := loginCommandOptions{
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
