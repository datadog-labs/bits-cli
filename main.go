package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
	"github.com/DataDog/bits-cli/internal/headless"
	"github.com/DataDog/bits-cli/internal/headless/adeep"
	"github.com/DataDog/bits-cli/internal/startup"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/workspace"
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
	// Ctrl-C cancels the active command instead of killing the process, so a
	// headless run can still write its terminal delivery record before exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return cmd.Execute(
		ctx,
		args,
		cmd.Actions{
			Chat:   runChat,
			Run:    runRun,
			Login:  runLogin,
			Logout: runLogout,
		},
		os.Stdout,
		os.Stderr,
	)
}

// runRun executes one noninteractive turn. It never initializes Bubble Tea or
// opens interactive login; missing OAuth fails fast with startup's login hint.
func runRun(ctx context.Context, opts cmd.RunOptions) error {
	return runRunWithStore(ctx, opts, auth.DefaultStore(), os.Stdout)
}

func runRunWithStore(ctx context.Context, opts cmd.RunOptions, store auth.CredentialStore, out io.Writer) error {
	workspace, err := openCurrentWorkspace()
	if err != nil {
		return err
	}
	defer func() { _ = workspace.Close() }()
	clientTools := tools.NewClientTools(workspace)
	toolSet, err := agent.NewToolSet(opts.ApprovalMode, clientTools...)
	if err != nil {
		return err
	}
	engine, err := startup.NewEngine(ctx, startup.EngineOptions{
		Client: startup.ClientOptions{
			Mode:    opts.AuthMode,
			Store:   store,
			APIKey:  os.Getenv("DD_API_KEY"),
			AppKey:  os.Getenv("DD_APP_KEY"),
			APISite: opts.Site,
		},
		Send:           assistant.SendOptions{ConversationID: opts.ConversationID, Model: opts.Model},
		UseFakeBackend: os.Getenv("BITS_FAKE_BACKEND") == "1",
	})
	if err != nil {
		return err
	}
	return runEngineTurn(ctx, engine, toolSet, opts, out)
}

// runEngineTurn drives exactly one turn, attempts Finish once, and maps its
// authoritative outcome to the process contract.
func runEngineTurn(ctx context.Context, engine *agent.Engine, tools *agent.ToolSet, opts cmd.RunOptions, out io.Writer) error {
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

	// A headless gated run has no interactive approver. Deny each pending gate
	// and continue so the backend can adjust before the terminal response.
	// Snapshots repeat every still-pending gate until the engine processes the
	// deny, so decide each tool call once to avoid overflowing the command queue.
	denied := make(map[string]struct{})
	consume := func(event agent.Event) error {
		if err := delivery.Consume(event); err != nil {
			return err
		}
		if event.Kind == agent.EventTranscript {
			for _, block := range event.Transcript.PendingApprovals() {
				id := block.ToolCallID()
				if _, done := denied[id]; done {
					continue
				}
				if err := autoDenyApproval(engine.Decide, block); err != nil {
					return err
				}
				denied[id] = struct{}{}
			}
		}
		return nil
	}

	result, err := engine.RunTurn(ctx, agent.TurnInput{Message: opts.Prompt, Tools: tools, OnDeny: agent.DenyContinue}, consume) // no-dd-sa:datadog/go-promptinjection -- opts.Prompt is intentionally sent as the user's message for this one turn; it is never used as a system instruction
	finishErr := delivery.Finish(headless.Finish{EndedAt: time.Now(), Result: result, Err: err})

	switch headless.ClassifyTurn(result) {
	case headless.OutcomeCompleted:
		return finishErr
	case headless.OutcomeApprovalDenied:
		if finishErr != nil {
			return finishErr
		}
		return &cmd.ExitError{Code: cmd.ExitApprovalDenied, Err: errors.New("an approval gate was denied; the turn finished with the backend's adjusted answer")}
	default:
		if err != nil {
			return err
		}
		return finishErr
	}
}

func autoDenyApproval(decide func(string, agent.ApprovalDecision) bool, block agent.Block) error {
	if block.Tool == nil || block.Tool.Status != agent.ToolAwaitingApproval {
		return nil
	}
	if !decide(block.ToolCallID(), agent.ApprovalDeny) {
		return fmt.Errorf("failed to auto-deny approval gate for tool call %s: command queue full", block.ToolCallID())
	}
	return nil
}

func runLogin(ctx context.Context, opts cmd.LoginOptions) error {
	session, err := auth.Login(ctx, auth.LoginOptions{
		Site:     opts.Site,
		ClientID: opts.ClientID,
		Store:    auth.DefaultStore(),
		Out:      os.Stderr,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s.\n", session.Site)
	return nil
}

func runLogout(ctx context.Context) error {
	logoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	hadSession, revokeErr, err := auth.Logout(logoutCtx, auth.DefaultStore(), nil)
	cancel()
	if err != nil {
		return err
	}
	if !hadSession {
		fmt.Fprintln(os.Stderr, "No Bits CLI OAuth session is stored.")
		return nil
	}
	if revokeErr != nil {
		fmt.Fprintf(os.Stderr, "Logged out locally; remote token revocation failed: %v\n", revokeErr)
		return nil
	}
	fmt.Fprintln(os.Stderr, "Logged out.")
	return nil
}

func printLoggedOut(w io.Writer) error {
	_, err := fmt.Fprintln(w, "Logged out of Bits.")
	return err
}

func runChat(parent context.Context, opts cmd.ChatOptions) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	workspace, err := openCurrentWorkspace()
	if err != nil {
		return err
	}
	defer func() { _ = workspace.Close() }()
	root, err := startupModel(ctx, opts, workspace)
	if err != nil {
		return err
	}

	// Login and chat are modes of one Bubble Tea program. Keeping the same
	// renderer alive prevents an alt-screen teardown flash after OAuth succeeds.
	model, err := tea.NewProgram(root, tea.WithContext(ctx)).Run()
	// The chat may leave an OSC 22 hand pointer over a clickable row. Reset it
	// once the program has stopped, so every exit path (ctrl+c, /quit, /logout,
	// errors) is covered. Terminals without OSC 22 ignore it.
	fmt.Print(ansi.SetPointerShape("default"))
	if err != nil {
		return err
	}
	m, ok := model.(*tui.Model)
	if !ok {
		return errors.New("bits TUI returned an unexpected model")
	}
	if err := m.StartupError(); err != nil {
		if errors.Is(err, loginui.ErrCanceled) {
			return fmt.Errorf("sign in to Datadog: %w", err)
		}
		return err
	}
	if m.LoggedOut() {
		return printLoggedOut(os.Stderr)
	}
	// On a clean exit with an active conversation, surface how to get back to it.
	// The conversation id is the server-side handle the Assistant API restores via
	// --conversation; there is no local session store yet.
	if m.ConversationID() != "" {
		fmt.Printf("Resume this conversation with: bits --conversation %s\n", m.ConversationID())
	}
	return nil
}

func openCurrentWorkspace() (*workspace.Workspace, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine workspace: %w", err)
	}
	return workspace.Open(root)
}

func startupModel(ctx context.Context, opts cmd.ChatOptions, workspace *workspace.Workspace) (*tui.Model, error) {
	return startupModelWithStore(ctx, opts, auth.DefaultStore(), workspace)
}

func startupModelWithStore(ctx context.Context, opts cmd.ChatOptions, store auth.CredentialStore, workspace *workspace.Workspace) (*tui.Model, error) {
	clientTools := tools.NewClientTools(workspace)
	toolSet, err := agent.NewToolSet(opts.ApprovalMode, clientTools...)
	if err != nil {
		return nil, err
	}
	config := tui.Config{
		Tools:     toolSet,
		Version:   cmd.BuildVersion(),
		Workspace: workspace,
		Logout: func(logoutCtx context.Context) (bool, error, error) {
			return auth.Logout(logoutCtx, store, nil)
		},
	}
	engineOpts := startup.EngineOptions{
		Client: startup.ClientOptions{
			Mode:    opts.AuthMode,
			Store:   store,
			APIKey:  os.Getenv("DD_API_KEY"),
			AppKey:  os.Getenv("DD_APP_KEY"),
			APISite: opts.Site,
		},
		Send:           assistant.SendOptions{ConversationID: opts.ConversationID},
		UseFakeBackend: os.Getenv("BITS_FAKE_BACKEND") == "1",
	}
	engine, err := startup.NewEngine(ctx, engineOpts)
	if err == nil {
		return tui.New(engine, config), nil
	}
	if !errors.Is(err, startup.ErrLoginRequired) {
		return nil, err
	}

	loginModel := loginui.New(ctx, func(loginCtx context.Context, site string, report func(loginui.BrowserStatus)) error {
		_, loginErr := auth.Login(loginCtx, auth.LoginOptions{
			Site:     site,
			ClientID: "",
			Store:    store,
			OnBrowserOpen: func(url string, openErr error) {
				report(loginui.BrowserStatus{AuthorizationURL: url, OpenError: openErr})
			},
			Out: io.Discard,
		})
		return loginErr
	})
	return tui.NewWithLogin(ctx, loginModel, func(factoryCtx context.Context) (*agent.Engine, error) {
		return startup.NewEngine(factoryCtx, engineOpts)
	}, config), nil
}
