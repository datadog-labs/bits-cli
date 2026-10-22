package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/headless"
	"github.com/datadog-labs/bits-cli/internal/headless/adeep"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
	"github.com/spf13/cobra"
)

// newRunCommand defines a one-turn noninteractive surface with command-local
// execution flags. The prompt is always a literal flag value, never positional
// input or stdin. A headless run has no interactive approver, so gated actions
// are denied unless --permissions skip-permissions is given.
func newRunCommand(invoked *bool, stdout io.Writer) *cobra.Command {
	opts := RunOptions{ChatOptions: ChatOptions{AuthMode: auth.ModeAuto}}
	var authMode string
	var permissionsMode string
	var imagePaths []string
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
			images, err := imageContext(imagePaths)
			if err != nil {
				return err
			}
			opts.Context = images
			opts.AuthMode = mode
			opts.PermissionsMode = permissions
			opts.Delivery = delivery
			*invoked = true
			return runRunWithStore(command.Context(), opts, auth.DefaultStore(), stdout)
		},
	}
	flags := command.Flags()
	flags.StringArrayVar(&opts.SkillPaths, "skill", nil, "additional skill directory to discover (repeatable)")
	flags.StringVar(&opts.Prompt, "prompt", "", "literal prompt for the one assistant turn (required)")
	flags.StringArrayVar(&imagePaths, "image", nil, "image file attached to the prompt (repeatable)")
	flags.StringVar(&opts.Delivery, "delivery", "", "delivery streamed on stdout: adeep (required)")
	flags.StringVar(&opts.Model, "model", "", "backend model ID override (for example, anthropic/claude-sonnet-4-6)")
	flags.StringVar(&opts.InferenceMode, "reasoning", "", "reasoning mode: fast or deep (feature flagged by the backend)")
	flags.StringVar(&permissionsMode, "permissions", string(agent.ModeDeny), "permissions mode for gated actions: deny or skip-permissions")
	flags.StringVar(&authMode, "auth", string(auth.ModeAuto), "authentication mode: auto or api-key")
	flags.StringVar(&opts.Site, "site", "", "Datadog API site for api-key authentication")
	flags.StringVar(&opts.ConversationID, "conversation", "", "resume an existing conversation by ID")
	return command
}

// imageContext attaches --image files as file URLs, loaded when the turn starts.
func imageContext(paths []string) (*assistant.AssistantContext, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	images := &assistant.AssistantContext{}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err == nil {
			_, err = os.Stat(abs)
		}
		if err != nil {
			return nil, &imageinput.Error{Name: path, Err: err}
		}
		images.Entities = append(images.Entities, assistant.ContextEntity{
			Type: assistant.EntityImageURL, ID: imageinput.FileURL(abs), Label: filepath.Base(abs),
		})
	}
	return images, nil
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

// RunOptions carries the shared execution flags plus one-turn run options.
type RunOptions struct {
	ChatOptions
	Prompt   string
	Context  *assistant.AssistantContext
	Delivery string
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

// runRunWithStore executes one noninteractive turn. Missing OAuth fails fast
// with guidance to run bits login.
func runRunWithStore(ctx context.Context, opts RunOptions, store auth.CredentialStore, out io.Writer) error {
	workspace, err := openCurrentWorkspace()
	if err != nil {
		return err
	}
	defer func() { _ = workspace.Close() }()
	clientTools, err := configuredClientTools(workspace)
	if err != nil {
		return err
	}
	toolSet, err := agent.NewToolSet(clientTools...)
	if err != nil {
		return err
	}
	engine, err := newEngine(ctx, opts.ChatOptions, workspace, store)
	if err != nil {
		return err
	}
	return runEngineTurn(ctx, engine, agent.TurnInput{Tools: toolSet, Context: opts.Context}, opts, out)
}

// runEngineTurn drives exactly one turn and maps its outcome to the process
// contract.
func runEngineTurn(ctx context.Context, engine *agent.Engine, input agent.TurnInput, opts RunOptions, out io.Writer) error {
	var delivery headless.Delivery
	switch opts.Delivery {
	case "adeep":
		delivery = adeep.New(out)
	default:
		return fmt.Errorf("unsupported delivery %q", opts.Delivery)
	}
	if err := delivery.Start(headless.Start{StartedAt: time.Now(), RequestedModel: opts.Model}); err != nil {
		return err
	}
	input.Message = opts.Prompt // no-dd-sa:datadog/go-promptinjection -- opts.Prompt is intentionally sent as the user's message for this one turn; it is never used as a system instruction
	input.OnDeny = agent.DenyContinue
	outcome, err := headless.Run(ctx, engine, input, delivery)
	if err != nil {
		return err
	}
	if outcome == headless.OutcomeApprovalDenied {
		return &ExitError{Code: ExitApprovalDenied, Err: errors.New("an approval gate was denied; the turn finished with the backend's adjusted answer")}
	}
	return nil
}
