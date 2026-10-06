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

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// ChatOptions carries the user-supplied root command flags resolved before
// chat starts.
type ChatOptions struct {
	SkillPaths      []string
	ConversationID  string
	Model           string
	InferenceMode   string
	AuthMode        auth.Mode
	Site            string
	PermissionsMode agent.PermissionsMode
}

// RunOptions carries the shared execution flags plus one-turn run options.
type RunOptions struct {
	ChatOptions
	Prompt   string
	Delivery string
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
	Run    func(ctx context.Context, opts RunOptions) error
	Login  func(ctx context.Context, opts LoginOptions) error
	Logout func(ctx context.Context) error
}

// Execute builds a fresh command tree for args and runs it with ctx, writing
// output to stdout and stderr. A new tree per call keeps parsing state from
// leaking between runs or tests.
func Execute(ctx context.Context, args []string, actions Actions, stdout, stderr io.Writer) error {
	invoked := false
	dispatch := Actions{
		Chat: func(ctx context.Context, opts ChatOptions) error {
			invoked = true
			return actions.Chat(ctx, opts)
		},
		Run: func(ctx context.Context, opts RunOptions) error {
			invoked = true
			return actions.Run(ctx, opts)
		},
		Login: func(ctx context.Context, opts LoginOptions) error {
			invoked = true
			return actions.Login(ctx, opts)
		},
		Logout: func(ctx context.Context) error {
			invoked = true
			return actions.Logout(ctx)
		},
	}
	root := newRootCommand(dispatch)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err != nil && !invoked {
		return &ExitError{Code: ExitUsage, Err: err}
	}
	return err
}

func newRootCommand(actions Actions) *cobra.Command {
	opts := ChatOptions{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}
	var authMode string
	var permissionsMode string
	// Keep root.Args nil so Cobra can identify unknown commands, suggest close
	// matches, and reject unknown help topics during command discovery. The
	// RunE check handles arguments after a -- terminator.
	root := &cobra.Command{
		Use:           "bits",
		Short:         "Datadog Assistant in your terminal",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       BuildVersion(),
		RunE: func(command *cobra.Command, args []string) error {
			if err := cobra.NoArgs(command, args); err != nil {
				return err
			}
			mode, err := parseAuthenticationMode(authMode)
			if err != nil {
				return err
			}
			permissions, err := parsePermissionsMode(permissionsMode)
			if err != nil {
				return err
			}
			if err := validateSiteSelection(mode, opts.Site, command.Flags().Changed("site")); err != nil {
				return err
			}
			opts.AuthMode = mode
			opts.PermissionsMode = permissions
			return actions.Chat(command.Context(), opts)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(command *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, command.CommandPath())
	})
	root.Flags().StringArrayVar(&opts.SkillPaths, "skill", nil, "additional skill directory to discover (repeatable)")
	root.Flags().StringVar(&authMode, "auth", string(auth.ModeAuto), "authentication mode: auto or api-key")
	root.Flags().StringVar(&permissionsMode, "permissions", string(agent.ModeManual), "permissions mode: manual or skip-permissions")
	root.Flags().StringVar(&opts.Site, "site", "", "Datadog API site for api-key authentication")
	root.Flags().StringVar(
		&opts.ConversationID,
		"conversation",
		"",
		"resume an existing conversation by ID",
	)
	root.AddCommand(newRunCommand(actions.Run), newLoginCommand(actions.Login), newLogoutCommand(actions.Logout))
	return root
}

func parseInferenceMode(raw string) (string, error) {
	switch raw {
	case "":
		return "", nil
	case "fast", "deep":
		return raw, nil
	default:
		return "", fmt.Errorf("invalid inference mode %q; expected fast or deep", raw)
	}
}

func validateSiteSelection(mode auth.Mode, site string, siteChanged bool) error {
	siteSet := siteChanged && strings.TrimSpace(site) != ""
	switch {
	case mode == auth.ModeAPIKey && !siteSet:
		return fmt.Errorf("--auth %s requires --site", auth.ModeAPIKey)
	case mode == auth.ModeAuto && siteChanged:
		return fmt.Errorf("--site requires --auth %s; use `bits login --site` for OAuth", auth.ModeAPIKey)
	}
	return nil
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

func parsePermissionsMode(raw string) (agent.PermissionsMode, error) {
	mode := agent.PermissionsMode(raw)
	switch mode {
	case agent.ModeManual, agent.ModeSkipPermissions:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid permissions mode %q; expected manual or skip-permissions", raw)
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
