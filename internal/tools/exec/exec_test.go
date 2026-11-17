package exec

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func TestExecServiceRejectsInvalidRequestBeforeLaunch(t *testing.T) {
	launcher := &recordingExecLauncher{}
	service := testExecService(launcher, 1024)
	absolute := t.TempDir()
	regularFile := filepath.Join(absolute, "file")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []ExecRequest{
		{CWD: absolute},
		{Command: "true"},
		{Command: "true", CWD: "relative"},
		{Command: "true", CWD: filepath.Join(absolute, "missing")},
		{Command: "true", CWD: regularFile},
	}
	for _, request := range tests {
		outcome := service.run(context.Background(), request)
		if outcome.Status != ExecLaunchFailed || outcome.Err == nil {
			t.Errorf("Run(%+v) = %+v, want a typed launch failure", request, outcome)
		}
	}
	if got := launcher.starts.Load(); got != 0 {
		t.Fatalf("launcher starts = %d, want 0", got)
	}
}

func TestExecServiceResolvesFailuresBeforeAdmission(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			started <- struct{}{}
			return &blockingExecProcess{release: release}, nil
		},
	}
	service := newExecService(launcher, execServiceConfig{
		concurrency:      1,
		outputLimit:      1024,
		defaultShellPath: "/bin/sh",
	})
	cwd := t.TempDir()
	worker := make(chan ExecOutcome, 1)
	go func() {
		worker <- service.run(context.Background(), ExecRequest{Command: "run", CWD: cwd})
	}()
	<-started

	invalid := make(chan ExecOutcome, 1)
	go func() {
		invalid <- service.run(context.Background(), ExecRequest{CWD: cwd})
	}()
	select {
	case outcome := <-invalid:
		if outcome.Status != ExecLaunchFailed || outcome.Err == nil {
			t.Errorf("invalid queued outcome = %+v, want launch failure", outcome)
		}
	case <-time.After(time.Second):
		close(release)
		<-worker
		t.Fatal("invalid request waited for an execution permit")
	}
	if got := launcher.starts.Load(); got != 1 {
		t.Errorf("launcher starts = %d, want only the admitted valid command", got)
	}

	close(release)
	if outcome := <-worker; outcome.Status != ExecSucceeded {
		t.Fatalf("valid worker outcome = %+v, want success", outcome)
	}
}

func TestExecServiceShellResolutionPrecedesCancelledAdmission(t *testing.T) {
	launcher := &recordingExecLauncher{}
	service := newExecService(launcher, execServiceConfig{
		concurrency:      1,
		outputLimit:      1024,
		defaultShellPath: "",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := service.run(ctx, ExecRequest{Command: "true", CWD: t.TempDir()})
	if outcome.Status != ExecLaunchFailed || outcome.Err == nil {
		t.Fatalf("outcome = %+v, want deterministic shell-resolution failure", outcome)
	}
	if got := launcher.starts.Load(); got != 0 {
		t.Fatalf("launcher starts = %d, want 0", got)
	}
}

func TestExecServiceReportsLauncherStartFailure(t *testing.T) {
	wantErr := errors.New("start failed")
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			return nil, wantErr
		},
	}
	outcome := testExecService(launcher, 1024).run(context.Background(), ExecRequest{
		Command: "true",
		CWD:     t.TempDir(),
	})
	if outcome.Status != ExecLaunchFailed || !errors.Is(outcome.Err, wantErr) {
		t.Fatalf("outcome = %+v, want launcher start failure", outcome)
	}
}

func TestExecServiceResolvesPlanBeforeLaunch(t *testing.T) {
	var got ExecPlan
	launcher := &recordingExecLauncher{
		start: func(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
			got = plan
			_, _ = stdout.Write([]byte("out"))
			_, _ = stderr.Write([]byte("err"))
			return immediateExecProcess(0), nil
		},
	}
	service := newExecService(launcher, execServiceConfig{
		concurrency:      4,
		outputLimit:      1234,
		defaultShellPath: "/custom/shell",
	})
	base := t.TempDir()
	outcome := service.run(context.Background(), ExecRequest{
		Command: "printf hi",
		CWD:     base + string(filepath.Separator) + "child" + string(filepath.Separator) + "..",
		Timeout: 3 * time.Second,
	})

	want := ExecPlan{
		Command:     "printf hi",
		CWD:         base,
		Shell:       "/custom/shell",
		Environment: ExecEnvironmentInherit,
		Timeout:     3 * time.Second,
		OutputLimit: 1234,
	}
	if got != want {
		t.Fatalf("launcher plan = %+v, want %+v", got, want)
	}
	if outcome.Status != ExecSucceeded || outcome.Output.Stdout != "out" || outcome.Output.Stderr != "err" {
		t.Fatalf("outcome = %+v, want successful labeled output", outcome)
	}
}

func TestExecServiceUsesCallerTimeoutWithinBound(t *testing.T) {
	var got ExecPlan
	launcher := &recordingExecLauncher{
		start: func(plan ExecPlan, _, _ io.Writer) (execProcess, error) {
			got = plan
			return immediateExecProcess(0), nil
		},
	}
	service := testExecService(launcher, 1024)
	outcome := service.run(context.Background(), ExecRequest{
		Command: "true",
		CWD:     t.TempDir(),
		Timeout: 2500 * time.Millisecond,
	})
	if outcome.Status != ExecSucceeded {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if got.Timeout != 2500*time.Millisecond {
		t.Fatalf("plan timeout = %s, want 2.5s", got.Timeout)
	}
}

func TestExecServiceRejectsSubMillisecondTimeout(t *testing.T) {
	launcher := &recordingExecLauncher{}
	service := testExecService(launcher, 1024)
	for _, timeout := range []time.Duration{-time.Second, 500 * time.Microsecond} {
		outcome := service.run(context.Background(), ExecRequest{Command: "true", CWD: t.TempDir(), Timeout: timeout})
		if outcome.Status != ExecLaunchFailed || outcome.Err == nil {
			t.Errorf("Run(timeout=%s) = %+v, want launch failure", timeout, outcome)
		}
	}
	if got := launcher.starts.Load(); got != 0 {
		t.Fatalf("launcher starts = %d, want 0", got)
	}
}

func TestExecServiceAdmissionLimitAndQueueCancellation(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, execConcurrencyLimit)
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			started <- struct{}{}
			return &blockingExecProcess{release: release}, nil
		},
	}
	service := testExecService(launcher, 1024)
	cwd := t.TempDir()

	var workers sync.WaitGroup
	for range execConcurrencyLimit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			outcome := service.run(context.Background(), ExecRequest{Command: "run", CWD: cwd})
			if outcome.Status != ExecSucceeded {
				t.Errorf("admitted outcome status = %q, want success", outcome.Status)
			}
		}()
	}
	for range execConcurrencyLimit {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("the permitted commands were not admitted")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan ExecOutcome, 1)
	go func() {
		queued <- service.run(ctx, ExecRequest{Command: "queued", CWD: cwd})
	}()
	// The start notifications prove all permits are occupied. The fifth
	// call therefore cannot reach the launcher before cancellation.
	cancel()
	outcome := <-queued
	if outcome.Status != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("queued outcome = %+v, want cancellation", outcome)
	}
	if got := launcher.starts.Load(); got != execConcurrencyLimit {
		t.Fatalf("launcher starts = %d, want %d", got, execConcurrencyLimit)
	}

	close(release)
	workers.Wait()
}

func TestExecServiceCancellationTerminatesAndReaps(t *testing.T) {
	process := &controlledExecProcess{
		waitStarted: make(chan struct{}),
		done:        make(chan execProcessResult, 1),
	}
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			return process, nil
		},
	}
	service := testExecService(launcher, 1024)
	cwd := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan ExecOutcome, 1)
	go func() {
		outcomes <- service.run(ctx, ExecRequest{Command: "run", CWD: cwd})
	}()
	<-process.waitStarted
	cancel()

	outcome := <-outcomes
	if outcome.Status != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("outcome = %+v, want cancellation", outcome)
	}
	if !process.terminated.Load() || !process.killed.Load() {
		t.Fatal("cancellation did not complete TERM-to-KILL process-group cleanup")
	}
	if !process.waitReturned.Load() {
		t.Fatal("Run returned before reaping the direct process")
	}
}

func TestExecServiceTimeoutKillsAndReapsAfterGrace(t *testing.T) {
	process := &controlledExecProcess{
		waitStarted:     make(chan struct{}),
		done:            make(chan execProcessResult, 1),
		ignoreTerminate: true,
	}
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			return process, nil
		},
	}
	service := testExecService(launcher, 1024)
	outcome := service.run(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir(), Timeout: 10 * time.Millisecond})

	if outcome.Status != ExecTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome = %+v, want timeout", outcome)
	}
	if !process.terminated.Load() || !process.killed.Load() {
		t.Fatal("timeout did not escalate termination to a kill")
	}
	if !process.waitReturned.Load() {
		t.Fatal("Run returned before reaping the killed direct process")
	}
}

func testExecService(launcher execLauncher, outputLimit int) *ExecService {
	return newExecService(launcher, execServiceConfig{
		concurrency:      execConcurrencyLimit,
		outputLimit:      outputLimit,
		defaultShellPath: "/bin/sh",
	})
}

type recordingExecLauncher struct {
	starts atomic.Int32
	start  func(ExecPlan, io.Writer, io.Writer) (execProcess, error)
}

func (l *recordingExecLauncher) Start(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
	l.starts.Add(1)
	if l.start != nil {
		return l.start(plan, stdout, stderr)
	}
	return nil, errors.New("unexpected launch")
}

type blockingExecProcess struct {
	release <-chan struct{}
}

func (p *blockingExecProcess) Wait() execProcessResult {
	<-p.release
	exitCode := 0
	return execProcessResult{exitCode: &exitCode}
}

func (*blockingExecProcess) Terminate() error { return nil }
func (*blockingExecProcess) Kill() error      { return nil }

type immediateExecProcess int

func (p immediateExecProcess) Wait() execProcessResult {
	exitCode := int(p)
	return execProcessResult{exitCode: &exitCode}
}

func (immediateExecProcess) Terminate() error { return nil }
func (immediateExecProcess) Kill() error      { return nil }

type controlledExecProcess struct {
	waitStarted     chan struct{}
	done            chan execProcessResult
	ignoreTerminate bool
	waitOnce        sync.Once
	finishOnce      sync.Once
	terminated      atomic.Bool
	killed          atomic.Bool
	waitReturned    atomic.Bool
}

func (p *controlledExecProcess) Wait() execProcessResult {
	p.waitOnce.Do(func() { close(p.waitStarted) })
	result := <-p.done
	p.waitReturned.Store(true)
	return result
}

func (p *controlledExecProcess) Terminate() error {
	p.terminated.Store(true)
	if !p.ignoreTerminate {
		p.finish("terminated")
	}
	return nil
}

func (p *controlledExecProcess) Kill() error {
	p.killed.Store(true)
	p.finish("killed")
	return nil
}

func (p *controlledExecProcess) finish(signal string) {
	p.finishOnce.Do(func() {
		p.done <- execProcessResult{signal: signal}
	})
}

func TestExecServiceDrainsBothStreamsUnderPressure(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	service := NewExecService(ws.DefaultShellPath())
	const streamBytes = 768 << 10
	// Both streams exceed their output cap and pipe capacity. Completion
	// proves the child can finish while both streams are drained.
	command := `(printf '%786432s' '' | tr ' ' O) & (printf '%786432s' '' | tr ' ' E >&2) & wait`
	if runtime.GOOS == "windows" {
		command = `[Console]::Out.Write(('O' * 786432)); [Console]::Error.Write(('E' * 786432))`
	}
	outcome := service.run(context.Background(), ExecRequest{Command: command, CWD: t.TempDir(), Timeout: 10 * time.Second})

	if outcome.Status != ExecSucceeded {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if !outcome.Output.Truncated {
		t.Fatal("pressure output was not marked truncated")
	}
	if got := len(outcome.Output.Stdout) + len(outcome.Output.Stderr); got != execOutputLimit {
		t.Fatalf("retained output = %d bytes, want %d", got, execOutputLimit)
	}
	wantOmitted := int64(streamBytes - execOutputLimit/2)
	if outcome.Output.StdoutOmittedBytes != wantOmitted || outcome.Output.StderrOmittedBytes != wantOmitted {
		t.Fatalf("omitted bytes = (%d, %d), want (%d, %d)",
			outcome.Output.StdoutOmittedBytes, outcome.Output.StderrOmittedBytes, wantOmitted, wantOmitted)
	}
}

// run runs request to completion, as a caller that never yields does.
func (s *ExecService) run(ctx context.Context, request ExecRequest) ExecOutcome {
	return s.Start(ctx, request, math.MaxInt64)
}

// A command still running at its yield keeps running under an ID; waits
// return only output written since the last observation, and the final
// outcome retires the ID.
func TestExecServiceYieldsAndWaits(t *testing.T) {
	process := &controlledExecProcess{waitStarted: make(chan struct{}), done: make(chan execProcessResult, 1)}
	var stdout io.Writer
	launcher := &recordingExecLauncher{
		start: func(_ ExecPlan, out, _ io.Writer) (execProcess, error) {
			stdout = out
			_, _ = out.Write([]byte("first\n"))
			return process, nil
		},
	}
	service := testExecService(launcher, 1024)
	started := service.Start(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir()}, 0)
	if started.Status != ExecRunning || started.ID == "" || started.Output.Stdout != "first\n" {
		t.Fatalf("Start = %+v, want running with an ID and the first line", started)
	}

	_, _ = stdout.Write([]byte("second\n"))
	running, err := service.Wait(context.Background(), started.ID, 0)
	if err != nil || running.Status != ExecRunning || running.Output.Stdout != "second\n" {
		t.Fatalf("Wait = %+v, %v; want running with only the second line", running, err)
	}

	exitCode := 3
	process.done <- execProcessResult{exitCode: &exitCode}
	final, err := service.Wait(context.Background(), started.ID, time.Hour)
	if err != nil || final.Status != ExecNonZeroExit || *final.ExitCode != 3 || final.ID != "" || final.Output.Stdout != "" {
		t.Fatalf("Wait = %+v, %v; want the exit with no repeated output", final, err)
	}
	if _, err := service.Wait(context.Background(), started.ID, 0); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("Wait after the final outcome = %v, want ErrUnknownCommand", err)
	}
}

// Stop, StopAll, and Close all end background commands with the full
// TERM-to-KILL cleanup, and a closed service starts nothing.
func TestExecServiceStopsBackgroundCommands(t *testing.T) {
	for _, stop := range []string{"stop", "stop all", "close"} {
		t.Run(stop, func(t *testing.T) {
			process := &controlledExecProcess{waitStarted: make(chan struct{}), done: make(chan execProcessResult, 1), ignoreTerminate: true}
			launcher := &recordingExecLauncher{
				start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) { return process, nil },
			}
			service := testExecService(launcher, 1024)
			started := service.Start(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir()}, 0)
			if started.Status != ExecRunning {
				t.Fatalf("Start = %+v, want running", started)
			}
			switch stop {
			case "stop":
				outcome, err := service.Stop(context.Background(), started.ID)
				if err != nil || outcome.Status != ExecCancelled || !errors.Is(outcome.Err, errStoppedByCaller) {
					t.Fatalf("Stop = %+v, %v; want cancellation on request", outcome, err)
				}
			case "stop all":
				<-service.Changed() // the registration
				if got := service.Running(); got != 1 {
					t.Fatalf("Running = %d, want 1", got)
				}
				if got := service.StopAll(); got != 1 {
					t.Fatalf("StopAll = %d, want 1", got)
				}
				<-service.Changed() // the command ended
				if got := service.Running(); got != 0 {
					t.Fatalf("Running after StopAll = %d, want 0", got)
				}
				// The model reads why: a user's stop is not a crash.
				if outcome, err := service.Wait(context.Background(), started.ID, 0); err != nil || !errors.Is(outcome.Err, errStoppedByUser) {
					t.Fatalf("Wait after StopAll = %+v, %v; want stopped by the user", outcome, err)
				}
			default:
				service.Close()
				if outcome := service.run(context.Background(), ExecRequest{Command: "late", CWD: t.TempDir()}); outcome.Status != ExecCancelled {
					t.Fatalf("Start after Close = %+v, want cancellation", outcome)
				}
			}
			if !process.terminated.Load() || !process.killed.Load() || !process.waitReturned.Load() {
				t.Fatal("background command was not terminated, killed, and reaped")
			}
		})
	}
}

// With every permit held by background commands, a launch waits at most its
// yield and names the commands the caller can stop.
func TestExecServiceAdmissionWaitsAtMostTheYield(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			return &blockingExecProcess{release: release}, nil
		},
	}
	service := newExecService(launcher, execServiceConfig{concurrency: 1, outputLimit: 1024, defaultShellPath: "/bin/sh"})
	held := service.Start(context.Background(), ExecRequest{Command: "server", CWD: t.TempDir()}, 0)
	if held.Status != ExecRunning {
		t.Fatalf("first Start = %+v, want running", held)
	}
	refused := service.Start(context.Background(), ExecRequest{Command: "next", CWD: t.TempDir()}, 0)
	if refused.Status != ExecLaunchFailed || refused.Err == nil || !strings.Contains(refused.Err.Error(), held.ID) {
		t.Fatalf("second Start = %+v, want a launch failure naming %s", refused, held.ID)
	}
	// A command that could never run in the background still waits only the
	// yield for admission, and a caller's deadline reads as a timeout.
	oneShot := service.Start(context.Background(), ExecRequest{Command: "next", CWD: t.TempDir(), Timeout: time.Millisecond}, time.Millisecond)
	if oneShot.Status != ExecLaunchFailed || !strings.Contains(oneShot.Err.Error(), held.ID) {
		t.Fatalf("one-shot Start = %+v, want a launch failure naming %s", oneShot, held.ID)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()
	if late := service.Start(expired, ExecRequest{Command: "next", CWD: t.TempDir()}, time.Hour); late.Status != ExecTimedOut {
		t.Fatalf("Start past its deadline = %+v, want timeout", late)
	}
	if got := launcher.starts.Load(); got != 1 {
		t.Fatalf("launcher starts = %d, want 1", got)
	}
}

// Admission spends the yield: a command admitted late still returns when the
// yield elapses, although its timeout fits within the whole yield.
func TestExecServiceYieldIncludesAdmission(t *testing.T) {
	var processes []*controlledExecProcess
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			// Ignoring TERM keeps the late command running a kill grace past
			// its timeout, so it cannot end as its yield elapses.
			p := &controlledExecProcess{waitStarted: make(chan struct{}), done: make(chan execProcessResult, 1), ignoreTerminate: true}
			processes = append(processes, p)
			return p, nil
		},
	}
	service := newExecService(launcher, execServiceConfig{concurrency: 1, outputLimit: 1024, defaultShellPath: "/bin/sh"})
	defer service.Close()
	if held := service.Start(context.Background(), ExecRequest{Command: "server", CWD: t.TempDir()}, 0); held.Status != ExecRunning {
		t.Fatalf("first Start = %+v, want running", held)
	}
	const yield = 200 * time.Millisecond
	server := processes[0]
	time.AfterFunc(yield/2, func() { server.finish("") })
	late := service.Start(context.Background(), ExecRequest{Command: "build", CWD: t.TempDir(), Timeout: yield}, yield)
	if late.Status != ExecRunning || late.ID == "" {
		t.Fatalf("late Start = %+v, want running at the yield", late)
	}
}

// Finished background commands nobody observed are forgotten oldest first,
// so abandoned commands cannot accumulate output buffers without bound.
func TestExecServiceForgetsOldestUnobservedFinishedCommands(t *testing.T) {
	var processes []*controlledExecProcess
	launcher := &recordingExecLauncher{
		start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
			p := &controlledExecProcess{waitStarted: make(chan struct{}), done: make(chan execProcessResult, 1)}
			processes = append(processes, p)
			return p, nil
		},
	}
	service := newExecService(launcher, execServiceConfig{concurrency: maxFinishedCommands + 1, outputLimit: 1024, defaultShellPath: "/bin/sh"})
	defer service.Close()
	var ids []string
	for range maxFinishedCommands {
		ids = append(ids, service.Start(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir()}, 0).ID)
	}
	// Finish them in launch order, each after the previous one ended.
	for i, p := range processes {
		service.mu.Lock()
		c := service.commands[ids[i]]
		service.mu.Unlock()
		p.finish("")
		<-c.done
	}
	newest := service.Start(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir()}, 0)
	if _, err := service.Wait(context.Background(), ids[0], 0); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("oldest finished command = %v, want forgotten", err)
	}
	for _, id := range append(ids[1:], newest.ID) {
		if _, err := service.Wait(context.Background(), id, 0); err != nil {
			t.Fatalf("Wait(%s) = %v, want still known", id, err)
		}
	}
}

// A caller gone by the time the yield elapses never gets an ID, so its
// command is stopped rather than left running unseen.
func TestExecServiceStopsCommandOfCallerGoneAtTheYield(t *testing.T) {
	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		process := &controlledExecProcess{waitStarted: make(chan struct{}), done: make(chan execProcessResult, 1)}
		launcher := &recordingExecLauncher{
			start: func(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
				cancel() // both the yield and the cancellation are ready
				return process, nil
			},
		}
		service := testExecService(launcher, 1024)
		if outcome := service.Start(ctx, ExecRequest{Command: "run", CWD: t.TempDir()}, 0); outcome.Status != ExecCancelled || outcome.ID != "" {
			t.Fatalf("Start = %+v, want the command stopped", outcome)
		}
	}
}
