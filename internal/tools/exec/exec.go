// Package exec provides bounded, one-shot local command execution for agent
// tools. It does not define or register a model-facing client tool.
package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/DataDog/bits-cli/internal/tools/spec"
)

const (
	execConcurrencyLimit = 4
	execTimeout          = time.Duration(spec.ExecDefaultTimeoutMS) * time.Millisecond
	// MaxTimeout bounds a caller-selected execution deadline. It prevents a
	// single unsandboxed command from holding one of the shared permits forever.
	MaxTimeout         = time.Duration(spec.ExecMaxTimeoutMS) * time.Millisecond
	execOutputLimit    = 64 << 10
	execTerminateGrace = 100 * time.Millisecond
)

// ErrExecUnsupportedPlatform is returned when the Unix runner is invoked on
// a platform for which Bits does not provide direct command execution.
var ErrExecUnsupportedPlatform = errors.New("exec command is unsupported on this platform")

// ExecTerminalReason identifies why a one-shot command stopped. It aliases the
// wire-contract type in package spec so the execution service and any importer
// (tool handler, renderers) share a single terminal-status vocabulary. Output
// truncation is orthogonal to this reason and is reported by ExecOutput.
type ExecTerminalReason = spec.ExecTerminalReason

const (
	ExecSucceeded    = spec.ExecSucceeded
	ExecNonZeroExit  = spec.ExecNonZeroExit
	ExecLaunchFailed = spec.ExecLaunchFailed
	ExecTimedOut     = spec.ExecTimedOut
	ExecCancelled    = spec.ExecCancelled
)

// ExecEnvironmentStrategy describes how a command receives its environment.
// V1 intentionally supports only ordinary process-environment inheritance.
type ExecEnvironmentStrategy uint8

const (
	ExecEnvironmentInherit ExecEnvironmentStrategy = iota
)

// ExecRequest is the validated command input accepted by ExecService. CWD must
// be absolute; callers remain responsible for resolving relative user input.
type ExecRequest struct {
	Command string
	CWD     string
	// Timeout overrides the service default when non-zero.
	Timeout time.Duration
}

// ExecPlan is the fully resolved, value-only description handed to a launcher.
// It contains no mutable environment, process, pipe, or transport state.
type ExecPlan struct {
	Command     string
	CWD         string
	Shell       string
	Environment ExecEnvironmentStrategy
	Timeout     time.Duration
	OutputLimit int
}

// ExecOutput contains the separately identified, bounded command streams as
// UTF-8 text. Invalid output bytes become replacement characters. Omitted byte
// counts refer to the original byte streams; their sum is non-zero exactly when
// Truncated is true.
type ExecOutput struct {
	Stdout             string
	Stderr             string
	StdoutOmittedBytes int64
	StderrOmittedBytes int64
	Truncated          bool
	// Incomplete reports that os/exec forcibly closed a pipe after its direct
	// child exited while a descendant still held it open. Bytes lost this way
	// cannot be counted, so this is distinct from Truncated.
	Incomplete bool
}

// ExecOutcome is the complete terminal result of a one-shot execution. Err is
// populated for launch, timeout, and cancellation failures. ExitCode is set
// only when the operating system reported a numeric exit status; Signal is set
// when Unix reported signal termination.
type ExecOutcome struct {
	Reason   ExecTerminalReason
	ExitCode *int
	Signal   string
	Elapsed  time.Duration
	Output   ExecOutput
	Err      error
}

// ExecService owns admission control and one-shot process lifecycle. A service
// must be shared by all exec_command handlers belonging to one Bits engine for
// its four-command limit to cover that engine.
type ExecService struct {
	launcher     execLauncher
	permits      chan struct{}
	timeout      time.Duration
	outputLimit  int
	resolveShell func() string
}

// NewExecService constructs the Linux/macOS direct execution service. On an
// unsupported platform it remains constructible, but Run reports a launch
// failure instead of exposing Unix execution behavior.
func NewExecService() *ExecService {
	return newExecService(newDirectExecLauncher(), execServiceConfig{
		concurrency:  execConcurrencyLimit,
		timeout:      execTimeout,
		outputLimit:  execOutputLimit,
		resolveShell: defaultExecShell,
	})
}

type execServiceConfig struct {
	concurrency  int
	timeout      time.Duration
	outputLimit  int
	resolveShell func() string
}

func newExecService(launcher execLauncher, config execServiceConfig) *ExecService {
	return &ExecService{
		launcher:     launcher,
		permits:      make(chan struct{}, config.concurrency),
		timeout:      config.timeout,
		outputLimit:  config.outputLimit,
		resolveShell: config.resolveShell,
	}
}

// Run executes request once and returns one terminal outcome. The fixed
// execution deadline begins after admission, so time spent waiting for another
// command does not consume the command's runtime budget.
func (s *ExecService) Run(ctx context.Context, request ExecRequest) ExecOutcome {
	calledAt := time.Now()
	plan, err := s.resolve(request)
	if err != nil {
		return ExecOutcome{
			Reason:  ExecLaunchFailed,
			Elapsed: time.Since(calledAt),
			Err:     err,
		}
	}

	if err := s.acquire(ctx); err != nil {
		return ExecOutcome{
			Reason:  reasonForContext(err),
			Elapsed: time.Since(calledAt),
			Err:     err,
		}
	}
	defer s.release()

	startedAt := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return ExecOutcome{
			Reason:  reasonForContext(err),
			Elapsed: time.Since(startedAt),
			Err:     err,
		}
	}

	stdoutLimit, stderrLimit := splitOutputLimit(plan.OutputLimit)
	stdout := newHeadTailBuffer(stdoutLimit)
	stderr := newHeadTailBuffer(stderrLimit)
	process, err := s.launcher.Start(plan, stdout, stderr)
	if err != nil {
		return ExecOutcome{
			Reason:  ExecLaunchFailed,
			Elapsed: time.Since(startedAt),
			Err:     err,
		}
	}

	waited := make(chan execProcessResult, 1)
	go func() {
		waited <- process.Wait()
	}()

	var result execProcessResult
	var terminalErr error
	var reason ExecTerminalReason
	select {
	case result = <-waited:
		reason = reasonForProcess(result)
	case <-runCtx.Done():
		// Prefer a process result that raced with context completion over
		// reporting a cancellation for a command that already finished.
		select {
		case result = <-waited:
			reason = reasonForProcess(result)
		default:
			terminalErr = runCtx.Err()
			reason = reasonForContext(terminalErr)
			result = terminateAndWait(process, waited)
		}
	}

	output := boundedExecOutput(stdout, stderr)
	output.Incomplete = result.outputIncomplete
	return ExecOutcome{
		Reason:   reason,
		ExitCode: cloneInt(result.exitCode),
		Signal:   result.signal,
		Elapsed:  time.Since(startedAt),
		Output:   output,
		Err:      terminalErr,
	}
}

func (s *ExecService) acquire(ctx context.Context) error {
	select {
	case s.permits <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *ExecService) release() {
	<-s.permits
}

func (s *ExecService) resolve(request ExecRequest) (ExecPlan, error) {
	if request.Command == "" {
		return ExecPlan{}, errors.New("exec command is empty")
	}
	if request.CWD == "" {
		return ExecPlan{}, errors.New("exec working directory is empty")
	}
	if !filepath.IsAbs(request.CWD) {
		return ExecPlan{}, fmt.Errorf("exec working directory %q is not absolute", request.CWD)
	}
	cwd := filepath.Clean(request.CWD)
	info, err := os.Stat(cwd)
	if err != nil {
		return ExecPlan{}, fmt.Errorf("validate exec working directory %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return ExecPlan{}, fmt.Errorf("exec working directory %q is not a directory", cwd)
	}
	shell := s.resolveShell()
	if shell == "" {
		return ExecPlan{}, errors.New("no shell is available")
	}
	timeout := s.timeout
	if request.Timeout != 0 {
		if request.Timeout < time.Millisecond || request.Timeout > MaxTimeout {
			return ExecPlan{}, fmt.Errorf("exec timeout %s must be between %s and %s", request.Timeout, time.Millisecond, MaxTimeout)
		}
		timeout = request.Timeout
	}
	return ExecPlan{
		Command:     request.Command,
		CWD:         cwd,
		Shell:       shell,
		Environment: ExecEnvironmentInherit,
		Timeout:     timeout,
		OutputLimit: s.outputLimit,
	}, nil
}

func resolveExecShell(configured string, lookPath func(string) (string, error)) string {
	if configured != "" {
		if resolved, err := lookPath(configured); err == nil {
			return resolved
		}
	}
	if resolved, err := lookPath("/bin/sh"); err == nil {
		return resolved
	}
	return ""
}

func terminateAndWait(process execProcess, waited <-chan execProcessResult) execProcessResult {
	_ = process.Terminate()
	timer := time.NewTimer(execTerminateGrace)
	defer timer.Stop()
	var result execProcessResult
	waitComplete := false
	select {
	case result = <-waited:
		waitComplete = true
		// The direct child may exit on TERM while a same-group descendant
		// ignores it. Preserve the result, but keep the group grace period.
		<-timer.C
	case <-timer.C:
	}
	_ = process.Kill()
	if !waitComplete {
		result = <-waited
	}
	return result
}

func reasonForContext(err error) ExecTerminalReason {
	if errors.Is(err, context.DeadlineExceeded) {
		return ExecTimedOut
	}
	return ExecCancelled
}

func reasonForProcess(result execProcessResult) ExecTerminalReason {
	if result.exitCode != nil && *result.exitCode == 0 && result.signal == "" {
		return ExecSucceeded
	}
	return ExecNonZeroExit
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type execLauncher interface {
	Start(ExecPlan, io.Writer, io.Writer) (execProcess, error)
}

type execProcess interface {
	Wait() execProcessResult
	Terminate() error
	Kill() error
}

type execProcessResult struct {
	exitCode         *int
	signal           string
	outputIncomplete bool
}
