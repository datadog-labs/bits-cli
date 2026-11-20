// Package exec provides local command execution for agent tools, with
// bounded output.
// Commands may outlive the call that started them. It does not define or
// register a model-facing client tool.
package exec

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

const (
	// execConcurrencyLimit bounds running commands, including those left in
	// the background, which hold their permit until they end.
	execConcurrencyLimit = 8
	execOutputLimit      = 64 << 10
	execTerminateGrace   = 100 * time.Millisecond
)

// ExecStatus identifies whether a command still runs, or why it stopped. It
// aliases the wire-contract type in package spec so the execution service and
// any importer (tool handler, renderers) share a single vocabulary. Output
// truncation is orthogonal to it and is reported by ExecOutput.
type ExecStatus = spec.ExecStatus

const (
	ExecSucceeded    = spec.ExecSucceeded
	ExecNonZeroExit  = spec.ExecNonZeroExit
	ExecLaunchFailed = spec.ExecLaunchFailed
	ExecTimedOut     = spec.ExecTimedOut
	ExecCancelled    = spec.ExecCancelled
	ExecRunning      = spec.ExecRunning
)

// ExecEnvironmentStrategy describes how a command receives its environment.
// ExecEnvironmentInherit uses the process environment, with an optional name
// exclusion list configured by the process that launches Bits. Names are
// case-insensitive on Windows.
type ExecEnvironmentStrategy uint8

const (
	ExecEnvironmentInherit ExecEnvironmentStrategy = iota
)

// ExecRequest is the validated command input accepted by ExecService. CWD must
// be absolute; callers remain responsible for resolving relative user input.
type ExecRequest struct {
	Command string
	CWD     string
	Shell   string
	// Timeout bounds the command's runtime when non-zero.
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

// ExecOutcome is what a caller observes of a command: its output since the
// previous observation and its status. A running command has an ID and no
// exit details. Err is populated for launch, timeout, and cancellation
// failures. ExitCode is set only when the operating system reported a numeric
// exit status; Signal is set when Unix reported signal termination.
type ExecOutcome struct {
	Status ExecStatus
	// ID names the command Start left running in the background. Later
	// observations by ID name it by Command instead: their caller knows
	// only the ID, and so do its readers, such as the transcript.
	ID       string
	Command  string
	ExitCode *int
	Signal   string
	Elapsed  time.Duration
	Output   ExecOutput
	Err      error
}

// ErrUnknownCommand reports an ID that names no command: it never ran in the
// background, or its final outcome was already observed.
var ErrUnknownCommand = errors.New("unknown command")

// ExecService owns admission control and the lifecycle of every command it
// starts. A service must be shared by all exec tool handlers of one Bits
// process for its concurrency limit to cover that process.
type ExecService struct {
	launcher         execLauncher
	permits          chan struct{}
	outputLimit      int
	defaultShellPath string

	// life ends when Close starts, stopping every command.
	life     context.Context
	endLife  context.CancelCauseFunc
	mu       sync.Mutex
	running  sync.WaitGroup          // supervisors; Add only under mu while life lasts
	commands map[string]*execCommand // commands that ran past their yield
	// changed signals its one consumer that Running may have changed.
	changed chan struct{}
	// ended counts commands that ended. Clock readings can tie, notably on
	// Windows, so it orders them instead.
	ended atomic.Uint64
}

// execCommand is one launched command. Its supervisor sets outcome, without
// output, and endOrder before closing done.
type execCommand struct {
	command        string
	stdout, stderr *headTailBuffer
	startedAt      time.Time
	stop           context.CancelCauseFunc
	done           chan struct{}
	outcome        ExecOutcome
	endOrder       uint64 // ExecService.ended when this command ended

	mu     sync.Mutex // serializes observations, which advance cursor
	cursor [2]int64
}

// NewExecService constructs the direct execution service with the default shell
// selected by the workspace. Optional names are excluded from child process
// environments.
func NewExecService(defaultShellPath string, excludedEnv ...string) *ExecService {
	return newExecService(newDirectExecLauncher(excludedEnv...), execServiceConfig{
		concurrency:      execConcurrencyLimit,
		outputLimit:      execOutputLimit,
		defaultShellPath: defaultShellPath,
	})
}

type execServiceConfig struct {
	concurrency      int
	outputLimit      int
	defaultShellPath string
}

func newExecService(launcher execLauncher, config execServiceConfig) *ExecService {
	s := &ExecService{
		launcher:         launcher,
		permits:          make(chan struct{}, config.concurrency),
		outputLimit:      config.outputLimit,
		defaultShellPath: config.defaultShellPath,
		commands:         make(map[string]*execCommand),
		changed:          make(chan struct{}, 1),
	}
	s.life, s.endLife = context.WithCancelCause(context.Background())
	return s
}

// Start launches request and waits until it ends, ctx ends, or yield elapses.
// A command still running at the yield keeps running in the background under
// the returned ID until it ends, its timeout elapses, Stop, or Close. A
// command whose timeout fits within what admission left of the yield never
// runs in the background.
// If ctx ends first the command is stopped, since its caller would never learn
// its ID. Admission waits for another command to end at most the yield, so a
// caller can always stop the commands holding the permits.
func (s *ExecService) Start(ctx context.Context, request ExecRequest, yield time.Duration) ExecOutcome {
	calledAt := time.Now()
	plan, err := s.resolve(request)
	if err != nil {
		return ExecOutcome{Status: ExecLaunchFailed, Elapsed: time.Since(calledAt), Err: err}
	}
	timer := time.NewTimer(yield)
	defer timer.Stop()
	yielded := timer.C
	if err := s.acquire(ctx, yielded); err != nil {
		return ExecOutcome{Status: admissionStatus(ctx, err), Elapsed: time.Since(calledAt), Err: err}
	}
	c, err := s.launch(plan)
	if err != nil {
		s.release()
		status := ExecLaunchFailed
		if errors.Is(err, errExecServiceClosed) {
			status = ExecCancelled
		}
		return ExecOutcome{Status: status, Elapsed: time.Since(calledAt), Err: err}
	}
	// Admission spent part of the yield; the call still returns within it.
	if plan.Timeout != 0 && plan.Timeout <= yield-time.Since(calledAt) {
		yielded = nil
	}
	select {
	case <-c.done:
	case <-yielded:
		// Prefer the outcome of a command that ended as the yield elapsed,
		// and never hand an ID to a caller that is gone.
		select {
		case <-c.done:
		default:
			outcome, err := s.register(ctx, c)
			if err == nil {
				return outcome
			}
			c.stop(err)
			<-c.done
		}
	case <-ctx.Done():
		c.stop(ctx.Err())
		<-c.done
	}
	outcome, _ := c.observe() // every path above waited for done
	return outcome
}

// Wait waits up to wait for command id to end and returns its output since
// the previous observation. Once it reports the final outcome, id is unknown.
// Ending ctx abandons the wait, not the command.
func (s *ExecService) Wait(ctx context.Context, id string, wait time.Duration) (ExecOutcome, error) {
	c, err := s.lookup(id)
	if err != nil {
		return ExecOutcome{}, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
	case <-ctx.Done():
		return ExecOutcome{}, ctx.Err()
	}
	return s.observe(id, c)
}

// Stop stops command id, waits for it to be reaped, and returns its final
// outcome. A command that ended first reports how it ended.
func (s *ExecService) Stop(ctx context.Context, id string) (ExecOutcome, error) {
	c, err := s.lookup(id)
	if err != nil {
		return ExecOutcome{}, err
	}
	c.stop(errStoppedByCaller)
	select {
	case <-c.done:
	case <-ctx.Done():
		return ExecOutcome{}, ctx.Err()
	}
	return s.observe(id, c)
}

// Close stops every command and waits until all are reaped. Later commands
// are not started.
func (s *ExecService) Close() {
	s.mu.Lock()
	s.endLife(errStoppedOnClose)
	s.mu.Unlock()
	s.running.Wait()
}

// Running counts the commands running in the background.
func (s *ExecService) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runningLocked())
}

// Changed receives after Running may have changed. It has one consumer.
func (s *ExecService) Changed() <-chan struct{} {
	return s.changed
}

// StopAll asks every command running in the background to stop on the user's
// behalf and returns how many it asked; their supervisors reap them.
func (s *ExecService) StopAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	running := s.runningLocked()
	for _, id := range running {
		s.commands[id].stop(errStoppedByUser)
	}
	return len(running)
}

// runningLocked lists the IDs of commands running in the background. The
// caller holds mu.
func (s *ExecService) runningLocked() []string {
	var ids []string
	for id, c := range s.commands {
		select {
		case <-c.done:
		default:
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *ExecService) notifyChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

var errExecServiceClosed = errors.New("exec service is closed")

// Why a command was stopped, as its outcome reports it: whoever reads it
// should not mistake a deliberate stop for a crash.
var (
	errStoppedByCaller = errors.New("stopped on request")
	errStoppedByUser   = errors.New("stopped by the user")
	errStoppedOnClose  = errors.New("stopped because Bits is exiting")
)

// acquire takes a permit, waiting until ctx ends, the service closes, or
// yielded fires.
func (s *ExecService) acquire(ctx context.Context, yielded <-chan time.Time) error {
	if err := cmp.Or(ctx.Err(), s.closedErr()); err != nil {
		return err
	}
	// A free permit wins over an already elapsed yield.
	select {
	case s.permits <- struct{}{}:
		return nil
	default:
	}
	select {
	case s.permits <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.life.Done():
		return errExecServiceClosed
	case <-yielded:
		return s.busyErr()
	}
	// A permit freed as the caller left may win the select; never launch for
	// a caller that is gone.
	if err := ctx.Err(); err != nil {
		s.release()
		return err
	}
	return nil
}

// admissionStatus classifies why a command was not admitted.
func admissionStatus(ctx context.Context, err error) ExecStatus {
	switch {
	case ctx.Err() != nil:
		return statusForContext(ctx.Err())
	case errors.Is(err, errExecServiceClosed):
		return ExecCancelled
	default:
		return ExecLaunchFailed
	}
}

func (s *ExecService) release() {
	<-s.permits
}

func (s *ExecService) closedErr() error {
	if s.life.Err() != nil {
		return errExecServiceClosed
	}
	return nil
}

func (s *ExecService) busyErr() error {
	s.mu.Lock()
	ids := s.runningLocked()
	s.mu.Unlock()
	if len(ids) == 0 {
		return fmt.Errorf("%d commands are already running; retry once one ends", cap(s.permits))
	}
	slices.Sort(ids)
	return fmt.Errorf("%d commands are already running; wait for or stop one of %s", cap(s.permits), strings.Join(ids, ", "))
}

// launch starts plan under a supervisor that owns it until it is reaped.
func (s *ExecService) launch(plan ExecPlan) (*execCommand, error) {
	s.mu.Lock()
	if err := s.closedErr(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	// Close waits for this command from here on; its context ends with life.
	s.running.Add(1)
	s.mu.Unlock()
	stdoutLimit, stderrLimit := splitOutputLimit(plan.OutputLimit)
	ctx, stop := context.WithCancelCause(s.life)
	c := &execCommand{
		command:   plan.Command,
		stdout:    newHeadTailBuffer(stdoutLimit),
		stderr:    newHeadTailBuffer(stderrLimit),
		startedAt: time.Now(),
		stop:      stop,
		done:      make(chan struct{}),
	}
	process, err := s.launcher.Start(plan, c.stdout, c.stderr)
	if err != nil {
		stop(nil)
		s.running.Done()
		return nil, err
	}
	go s.supervise(ctx, c, process, plan.Timeout)
	return c, nil
}

// supervise waits for the process to exit or for its context to end, which
// a timeout, Stop, an abandoned Start, or Close causes. A process result that
// races with the context wins: the command did finish.
func (s *ExecService) supervise(ctx context.Context, c *execCommand, process execProcess, timeout time.Duration) {
	defer s.running.Done()
	defer c.stop(nil)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	waited := make(chan execProcessResult, 1)
	go func() {
		waited <- process.Wait()
	}()
	var result execProcessResult
	var err error
	select {
	case result = <-waited:
	case <-ctx.Done():
		select {
		case result = <-waited:
		default:
			err = context.Cause(ctx)
			result = terminateAndWait(process, waited)
		}
	}
	if err == nil {
		// A command ends with its shell: what it left behind, such as
		// `server &`, would otherwise run untracked and outlive Bits. A
		// stopped command's descendants instead got terminateAndWait's grace.
		// Like there, nothing can act on a failure.
		_ = process.Kill()
	}
	status := statusForProcess(result)
	if err != nil {
		status = statusForContext(err)
	}
	c.outcome = ExecOutcome{
		Status:   status,
		ExitCode: cloneInt(result.exitCode),
		Signal:   result.signal,
		Elapsed:  time.Since(c.startedAt),
		Output:   ExecOutput{Incomplete: result.outputIncomplete},
		Err:      err,
	}
	c.endOrder = s.ended.Add(1)
	s.release()
	close(c.done)
	s.notifyChanged()
}

// maxFinishedCommands bounds background commands that ended without anyone
// observing their final outcome, each keeping its output buffers. Those that
// ended first are forgotten first.
const maxFinishedCommands = 16

// register makes c reachable by a new ID and returns its running outcome,
// or its final one if it ended meanwhile. It fails once ctx ended, checked
// under the lock StopAll takes: a caller cancelled before StopAll cannot add
// a command that StopAll missed.
func (s *ExecService) register(ctx context.Context, c *execCommand) (ExecOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ExecOutcome{}, err
	}
	s.pruneFinished()
	id := newCommandID()
	for s.commands[id] != nil {
		id = newCommandID()
	}
	s.commands[id] = c
	s.notifyChanged()
	outcome := s.observeLocked(id, c)
	if outcome.Status == ExecRunning {
		outcome.ID = id
	}
	return outcome, nil
}

// pruneFinished forgets the oldest unobserved finished commands beyond the
// bound. The caller holds mu.
func (s *ExecService) pruneFinished() {
	var finished []string
	for id, c := range s.commands {
		select {
		case <-c.done:
			finished = append(finished, id)
		default:
		}
	}
	slices.SortFunc(finished, func(a, b string) int {
		return cmp.Compare(s.commands[a].endOrder, s.commands[b].endOrder)
	})
	for _, id := range finished[:max(0, len(finished)-maxFinishedCommands+1)] {
		delete(s.commands, id)
	}
}

func (s *ExecService) lookup(id string) (*execCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.commands[id]
	if c == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownCommand, id)
	}
	return c, nil
}

// observe returns c's output since the last observation and forgets id once
// it reports the final outcome, which exactly one observer receives.
func (s *ExecService) observe(id string, c *execCommand) (ExecOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commands[id] != c {
		return ExecOutcome{}, fmt.Errorf("%w %q", ErrUnknownCommand, id)
	}
	outcome := s.observeLocked(id, c)
	outcome.Command = c.command
	return outcome, nil
}

// observeLocked observes c, registered as id. The caller holds mu.
func (s *ExecService) observeLocked(id string, c *execCommand) ExecOutcome {
	outcome, final := c.observe()
	if final {
		delete(s.commands, id)
	}
	return outcome
}

// observe returns the command's output past its cursor, with its final
// outcome once it ended.
func (c *execCommand) observe() (ExecOutcome, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		outcome := c.outcome
		incomplete := outcome.Output.Incomplete
		outcome.Output = readExecOutput(c.stdout, c.stderr, &c.cursor, true)
		outcome.Output.Incomplete = incomplete
		return outcome, true
	default:
		return ExecOutcome{
			Status:  ExecRunning,
			Elapsed: time.Since(c.startedAt),
			Output:  readExecOutput(c.stdout, c.stderr, &c.cursor, false),
		}, false
	}
}

// newCommandID returns "cmd_" and 48 random bits: unguessable enough that one
// conversation cannot stumble on another's command.
func newCommandID() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return "cmd_" + hex.EncodeToString(b[:])
}

func (s *ExecService) resolve(request ExecRequest) (ExecPlan, error) {
	if request.Command == "" {
		return ExecPlan{}, errors.New("exec command is empty")
	}
	if request.CWD == "" {
		return ExecPlan{}, errors.New("exec working directory is empty")
	}
	if !filepath.IsAbs(request.CWD) {
		return ExecPlan{}, fmt.Errorf("exec working directory %s is not absolute", request.CWD)
	}
	cwd := filepath.Clean(request.CWD)
	info, err := os.Stat(cwd)
	if err != nil {
		return ExecPlan{}, fmt.Errorf("validate exec working directory: %w", err)
	}
	if !info.IsDir() {
		return ExecPlan{}, fmt.Errorf("exec working directory %s is not a directory", cwd)
	}
	var shell string
	if request.Shell != "" {
		if !supportedExecShell(request.Shell) {
			return ExecPlan{}, fmt.Errorf("unsupported exec shell %s: use %s", request.Shell, supportedExecShells())
		}
		resolved, err := osexec.LookPath(request.Shell)
		if err != nil {
			return ExecPlan{}, fmt.Errorf("resolve exec shell: %w", err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return ExecPlan{}, fmt.Errorf("resolve absolute exec shell %s: %w", request.Shell, err)
		}
		shell = resolved
	} else {
		shell = s.defaultShellPath
	}
	if shell == "" {
		return ExecPlan{}, errors.New("no shell is available")
	}
	if request.Timeout != 0 && request.Timeout < time.Millisecond {
		return ExecPlan{}, fmt.Errorf("exec timeout %s must be at least %s", request.Timeout, time.Millisecond)
	}
	return ExecPlan{
		Command:     request.Command,
		CWD:         cwd,
		Shell:       shell,
		Environment: ExecEnvironmentInherit,
		Timeout:     request.Timeout,
		OutputLimit: s.outputLimit,
	}, nil
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

func statusForContext(err error) ExecStatus {
	if errors.Is(err, context.DeadlineExceeded) {
		return ExecTimedOut
	}
	return ExecCancelled
}

func statusForProcess(result execProcessResult) ExecStatus {
	if result.exitCode != nil && *result.exitCode == 0 && result.signal == "" {
		return ExecSucceeded
	}
	return ExecNonZeroExit
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	return new(*value)
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
