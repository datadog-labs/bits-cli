package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// newRunCommand defines a one-turn noninteractive surface with command-local
// execution flags. The prompt is always a literal flag value, never positional
// input or stdin.
func newRunCommand(action func(context.Context, RunOptions) error) *cobra.Command {
	opts := RunOptions{ChatOptions: ChatOptions{AuthMode: auth.ModeAuto, ApprovalMode: agent.ModeAllowAll}}
	var authMode string
	var approvalMode string
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
			delivery, err := parseDelivery(opts.Delivery)
			if err != nil {
				return err
			}
			mode, err := parseAuthenticationMode(authMode)
			if err != nil {
				return err
			}
			approval, err := parseApprovalMode(approvalMode)
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
			opts.ApprovalMode = approval
			opts.Delivery = delivery
			return action(command.Context(), opts)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.Prompt, "prompt", "", "literal prompt for the one assistant turn (required)")
	flags.StringVar(&opts.Delivery, "delivery", "", "delivery streamed on stdout: adeep (required)")
	flags.StringVar(&opts.Model, "model", "", "model override for this run")
	flags.StringVar(&authMode, "auth", string(auth.ModeAuto), "authentication mode: auto or api-key")
	flags.StringVar(&approvalMode, "approval", string(agent.ModeAllowAll), "approval mode: allow-all or gated")
	flags.StringVar(&opts.Site, "site", "", "Datadog API site for api-key authentication")
	flags.StringVar(&opts.ConversationID, "conversation", "", "resume an existing conversation by ID")
	return command
}

func parseDelivery(raw string) (string, error) {
	if raw != "adeep" {
		return "", fmt.Errorf("invalid delivery %q; expected adeep", raw)
	}
	return raw, nil
}
