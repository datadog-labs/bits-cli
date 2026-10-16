// Package cmd owns the Bits command tree and its interactive and headless runs.
package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/config"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/spf13/cobra"
)

// ChatOptions carries the user-supplied root command flags and the resolved
// configuration, both settled before chat starts.
type ChatOptions struct {
	Config          *config.Config
	SkillPaths      []string
	ConversationID  string
	AuthMode        auth.Mode
	Site            string
	PermissionsMode agent.PermissionsMode
}

// Execute builds a fresh command tree for args and runs it with ctx, writing
// output to stdout and stderr. A new tree per call keeps parsing state isolated.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	invoked := false
	root := newRootCommand(&invoked, stdout, stderr)
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

func newRootCommand(invoked *bool, stdout, stderr io.Writer) *cobra.Command {
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
		Version:       version.Build(),
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
			cfg, err := config.Load(config.Params{})
			if err != nil {
				return err
			}
			opts.Config = cfg
			opts.AuthMode = mode
			opts.PermissionsMode = permissions
			*invoked = true
			return runChat(command.Context(), opts, stdout, stderr)
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
	root.AddCommand(newRunCommand(invoked, stdout), newLoginCommand(invoked, stderr), newLogoutCommand(invoked, stderr), newUpdateCommand(invoked, stdout, stderr))
	return root
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
