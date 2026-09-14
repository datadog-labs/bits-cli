package exec

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveExecShell(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		available  map[string]string
		want       string
	}{
		{
			name:       "configured shell",
			configured: "/opt/custom/sh",
			available:  map[string]string{"/opt/custom/sh": "/opt/custom/sh", "/bin/sh": "/bin/sh"},
			want:       "/opt/custom/sh",
		},
		{
			name:       "configured shell resolved through path",
			configured: "zsh",
			available:  map[string]string{"zsh": "/usr/bin/zsh", "/bin/sh": "/bin/sh"},
			want:       "/usr/bin/zsh",
		},
		{
			name:       "unavailable configured shell falls back",
			configured: "/missing/shell",
			available:  map[string]string{"/bin/sh": "/bin/sh"},
			want:       "/bin/sh",
		},
		{
			name:       "empty configured shell falls back",
			configured: "",
			available:  map[string]string{"/bin/sh": "/bin/sh"},
			want:       "/bin/sh",
		},
		{
			name:       "nothing available",
			configured: "/missing/shell",
			available:  map[string]string{},
			want:       "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookPath := func(file string) (string, error) {
				if resolved, ok := test.available[file]; ok {
					return resolved, nil
				}
				return "", errors.New("not found")
			}
			if got := resolveExecShell(test.configured, lookPath); got != test.want {
				t.Fatalf("resolveExecShell(%q) = %q, want %q", test.configured, got, test.want)
			}
		})
	}
}

func TestExecServiceRejectsInvalidRequestBeforeLaunch(t *testing.T) {
	launcher := &recordingExecLauncher{}
	service := testExecService(launcher, time.Second, 1024)
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
		outcome := service.Run(context.Background(), request)
		if outcome.Reason != ExecLaunchFailed || outcome.Err == nil {
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
		concurrency:  1,
		timeout:      time.Minute,
		outputLimit:  1024,
		resolveShell: func() string { return "/bin/sh" },
	})
	cwd := t.TempDir()
	worker := make(chan ExecOutcome, 1)
	go func() {
		worker <- service.Run(context.Background(), ExecRequest{Command: "run", CWD: cwd})
	}()
	<-started

	invalid := make(chan ExecOutcome, 1)
	go func() {
		invalid <- service.Run(context.Background(), ExecRequest{CWD: cwd})
	}()
	select {
	case outcome := <-invalid:
		if outcome.Reason != ExecLaunchFailed || outcome.Err == nil {
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
	if outcome := <-worker; outcome.Reason != ExecSucceeded {
		t.Fatalf("valid worker outcome = %+v, want success", outcome)
	}
}

func TestExecServiceShellResolutionPrecedesCancelledAdmission(t *testing.T) {
	launcher := &recordingExecLauncher{}
	service := newExecService(launcher, execServiceConfig{
		concurrency:  1,
		timeout:      time.Second,
		outputLimit:  1024,
		resolveShell: func() string { return "" },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := service.Run(ctx, ExecRequest{Command: "true", CWD: t.TempDir()})
	if outcome.Reason != ExecLaunchFailed || outcome.Err == nil {
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
	outcome := testExecService(launcher, time.Second, 1024).Run(context.Background(), ExecRequest{
		Command: "true",
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecLaunchFailed || !errors.Is(outcome.Err, wantErr) {
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
		concurrency:  4,
		timeout:      3 * time.Second,
		outputLimit:  1234,
		resolveShell: func() string { return "/custom/shell" },
	})
	base := t.TempDir()
	outcome := service.Run(context.Background(), ExecRequest{
		Command: "printf hi",
		CWD:     base + string(filepath.Separator) + "child" + string(filepath.Separator) + "..",
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
	if outcome.Reason != ExecSucceeded || outcome.Output.Stdout != "out" || outcome.Output.Stderr != "err" {
		t.Fatalf("outcome = %+v, want successful labeled output", outcome)
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
	service := testExecService(launcher, time.Minute, 1024)
	cwd := t.TempDir()

	var workers sync.WaitGroup
	for range execConcurrencyLimit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			outcome := service.Run(context.Background(), ExecRequest{Command: "run", CWD: cwd})
			if outcome.Reason != ExecSucceeded {
				t.Errorf("admitted outcome reason = %q, want success", outcome.Reason)
			}
		}()
	}
	for range execConcurrencyLimit {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("four commands were not admitted")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan ExecOutcome, 1)
	go func() {
		queued <- service.Run(ctx, ExecRequest{Command: "queued", CWD: cwd})
	}()
	// The four start notifications prove all permits are occupied. The fifth
	// call therefore cannot reach the launcher before cancellation.
	cancel()
	outcome := <-queued
	if outcome.Reason != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
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
	service := testExecService(launcher, time.Minute, 1024)
	cwd := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan ExecOutcome, 1)
	go func() {
		outcomes <- service.Run(ctx, ExecRequest{Command: "run", CWD: cwd})
	}()
	<-process.waitStarted
	cancel()

	outcome := <-outcomes
	if outcome.Reason != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
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
	service := testExecService(launcher, 10*time.Millisecond, 1024)
	outcome := service.Run(context.Background(), ExecRequest{Command: "run", CWD: t.TempDir()})

	if outcome.Reason != ExecTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome = %+v, want timeout", outcome)
	}
	if !process.terminated.Load() || !process.killed.Load() {
		t.Fatal("timeout did not escalate termination to a kill")
	}
	if !process.waitReturned.Load() {
		t.Fatal("Run returned before reaping the killed direct process")
	}
}

func testExecService(launcher execLauncher, timeout time.Duration, outputLimit int) *ExecService {
	return newExecService(launcher, execServiceConfig{
		concurrency:  execConcurrencyLimit,
		timeout:      timeout,
		outputLimit:  outputLimit,
		resolveShell: func() string { return "/bin/sh" },
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
