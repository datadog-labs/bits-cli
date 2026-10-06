package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// newRunCommand defines a one-turn noninteractive surface with command-local
// execution flags. The prompt is always a literal flag value, never positional
// input or stdin. A headless run has no interactive approver, so gated actions
// are denied unless --permissions skip-permissions is given.
func newRunCommand(action func(context.Context, RunOptions) error) *cobra.Command {
	opts := RunOptions{ChatOptions: ChatOptions{AuthMode: auth.ModeAuto}}
	var authMode string
	var permissionsMode string
	command := &cobra.Command{
		Use:   "run",
		Short: "Run one noninteractive assistant turn and stream a delivery on stdout",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !command.Flags().Changed("prompt") {
				return fmt.Errorf("required flag \"--prompt\" was not set")
			}
			if strings.TrimSpace(opts.Prompt) == "" {
				return fmt.Errorf("--prompt must not be empty or whitespace")
			}
			if opts.Prompt == "-" {
				return fmt.Errorf("--prompt must be a literal message; stdin (\"-\") is not supported")
			}
			if !command.Flags().Changed("delivery") {
				return fmt.Errorf("required flag \"--delivery\" was not set; the first delivery is adeep")
			}
			inferenceMode, err := parseInferenceMode(opts.InferenceMode)
			if err != nil {
				return err
			}
			opts.InferenceMode = inferenceMode
			delivery, err := parseDelivery(opts.Delivery)
			if err != nil {
				return err
			}
			permissions, err := parseRunPermissionsMode(permissionsMode)
			if err != nil {
				return err
			}
			mode, err := parseAuthenticationMode(authMode)
			if err != nil {
				return err
			}
			if err := validateSiteSelection(mode, opts.Site, command.Flags().Changed("site")); err != nil {
				return err
			}
			if mode == auth.ModeAPIKey {
				site, err := auth.NormalizeAPISite(opts.Site)
				if err != nil {
					return err
				}
				opts.Site = site
			}
			opts.AuthMode = mode
			opts.PermissionsMode = permissions
			opts.Delivery = delivery
			return action(command.Context(), opts)
		},
	}
	flags := command.Flags()
	flags.StringArrayVar(&opts.SkillPaths, "skill", nil, "additional skill directory to discover (repeatable)")
	flags.StringVar(&opts.Prompt, "prompt", "", "literal prompt for the one assistant turn (required)")
	flags.StringVar(&opts.Delivery, "delivery", "", "delivery streamed on stdout: adeep (required)")
	flags.StringVar(&opts.Model, "model", "", "backend model ID override (for example, anthropic/claude-sonnet-4-6)")
	flags.StringVar(&opts.InferenceMode, "reasoning", "", "reasoning mode: fast or deep (feature flagged by the backend)")
	flags.StringVar(&permissionsMode, "permissions", string(agent.ModeDeny), "permissions mode for gated actions: deny or skip-permissions")
	flags.StringVar(&authMode, "auth", string(auth.ModeAuto), "authentication mode: auto or api-key")
	flags.StringVar(&opts.Site, "site", "", "Datadog API site for api-key authentication")
	flags.StringVar(&opts.ConversationID, "conversation", "", "resume an existing conversation by ID")
	return command
}

func parseRunPermissionsMode(raw string) (agent.PermissionsMode, error) {
	mode := agent.PermissionsMode(raw)
	switch mode {
	case agent.ModeDeny, agent.ModeSkipPermissions:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid permissions mode %q; expected deny or skip-permissions", raw)
	}
}

func parseDelivery(raw string) (string, error) {
	if raw != "adeep" {
		return "", fmt.Errorf("invalid delivery %q; expected adeep", raw)
	}
	return raw, nil
}
