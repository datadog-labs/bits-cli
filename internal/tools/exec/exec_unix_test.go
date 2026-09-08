//go:build linux || darwin

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecServiceUnixSuccessAndInheritedEnvironment(t *testing.T) {
	t.Setenv("BITS_EXEC_TEST_VALUE", "inherited-value")
	dir := t.TempDir()
	service := newUnixTestExecService(time.Second, 4096)
	outcome := service.Run(context.Background(), ExecRequest{
		Command: `printf 'stdout:%s' "$BITS_EXEC_TEST_VALUE"; printf 'stderr' >&2; if read ignored; then exit 9; fi`,
		CWD:     dir,
	})

	if outcome.Reason != ExecSucceeded || outcome.ExitCode == nil || *outcome.ExitCode != 0 {
		t.Fatalf("outcome = %+v, want exit success", outcome)
	}
	if outcome.Signal != "" || outcome.Err != nil {
		t.Fatalf("outcome signal/error = (%q, %v), want neither", outcome.Signal, outcome.Err)
	}
	if outcome.Output.Stdout != "stdout:inherited-value" || outcome.Output.Stderr != "stderr" {
		t.Fatalf("output = %+v", outcome.Output)
	}
	if outcome.Output.Truncated {
		t.Fatalf("small output was truncated: %+v", outcome.Output)
	}
}

func TestExecServiceUnixNonZeroExit(t *testing.T) {
	service := newUnixTestExecService(time.Second, 4096)
	outcome := service.Run(context.Background(), ExecRequest{Command: "exit 7", CWD: t.TempDir()})
	if outcome.Reason != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
		t.Fatalf("outcome = %+v, want exit status 7", outcome)
	}
	if outcome.Err != nil {
		t.Fatalf("non-zero exit has infrastructure error %v", outcome.Err)
	}
}

func TestExecServiceUnixNaturalSignalExit(t *testing.T) {
	service := newUnixTestExecService(time.Second, 4096)
	outcome := service.Run(context.Background(), ExecRequest{
		Command: "kill -TERM $$",
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecNonZeroExit {
		t.Fatalf("reason = %q, want non-zero exit", outcome.Reason)
	}
	if outcome.ExitCode != nil {
		t.Fatalf("exit code = %d, want unavailable for signal exit", *outcome.ExitCode)
	}
	if outcome.Signal != syscall.SIGTERM.String() {
		t.Fatalf("signal = %q, want %q", outcome.Signal, syscall.SIGTERM.String())
	}
	if outcome.Err != nil {
		t.Fatalf("natural signal exit has infrastructure error %v", outcome.Err)
	}
}

func TestExecServiceUnixLaunchFailure(t *testing.T) {
	service := newUnixTestExecService(time.Second, 4096)
	missing := filepath.Join(t.TempDir(), "missing")
	outcome := service.Run(context.Background(), ExecRequest{Command: "true", CWD: missing})
	if outcome.Reason != ExecLaunchFailed || outcome.Err == nil {
		t.Fatalf("outcome = %+v, want launch failure for missing cwd", outcome)
	}
}

func TestExecServiceUnixFallsBackWhenConfiguredShellIsUnavailable(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	outcome := NewExecService().Run(context.Background(), ExecRequest{
		Command: "printf fallback",
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecSucceeded || outcome.Output.Stdout != "fallback" {
		t.Fatalf("outcome = %+v, want /bin/sh fallback success", outcome)
	}
}

func TestExecServiceUnixDrainsBothStreamsUnderPressure(t *testing.T) {
	service := newUnixTestExecService(5*time.Second, execOutputLimit)
	// The two background pipelines concurrently write more than half the
	// combined cap to each descriptor. Completion proves neither pipe can
	// block the child while the other stream is being consumed.
	command := `(yes O | head -c 786432) & (yes E | head -c 786432 >&2) & wait`
	outcome := service.Run(context.Background(), ExecRequest{Command: command, CWD: t.TempDir()})

	if outcome.Reason != ExecSucceeded {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if !outcome.Output.Truncated {
		t.Fatal("pressure output was not marked truncated")
	}
	if got := len(outcome.Output.Stdout) + len(outcome.Output.Stderr); got != execOutputLimit {
		t.Fatalf("retained output = %d bytes, want %d", got, execOutputLimit)
	}
	if outcome.Output.StdoutOmittedBytes != 262144 || outcome.Output.StderrOmittedBytes != 262144 {
		t.Fatalf("omitted bytes = (%d, %d), want (262144, 262144)",
			outcome.Output.StdoutOmittedBytes, outcome.Output.StderrOmittedBytes)
	}
}

func TestExecServiceUnixReportsOutputIncompleteAfterForcedPipeClosure(t *testing.T) {
	service := newUnixTestExecService(time.Second, 4096)
	outcome := service.Run(context.Background(), ExecRequest{
		// The direct shell exits successfully while its background child keeps
		// stdout open. Cmd.Wait closes that lingering pipe after WaitDelay, so
		// the late output cannot be reported or counted.
		Command: `(sleep 0.25; printf late) &`,
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecSucceeded || outcome.ExitCode == nil || *outcome.ExitCode != 0 {
		t.Fatalf("outcome = %+v, want successful direct child", outcome)
	}
	if !outcome.Output.Incomplete {
		t.Fatalf("output = %+v, want forced-pipe-closure indicator", outcome.Output)
	}
	if outcome.Output.Truncated || outcome.Output.StdoutOmittedBytes != 0 || outcome.Output.StderrOmittedBytes != 0 {
		t.Fatalf("output = %+v, want no byte-limit truncation", outcome.Output)
	}
}

func TestExecServiceUnixTimeoutTerminatesProcessGroup(t *testing.T) {
	service := newUnixTestExecService(40*time.Millisecond, 4096)
	outcome := service.Run(context.Background(), ExecRequest{
		Command: "trap '' TERM; sleep 2",
		CWD:     t.TempDir(),
	})

	if outcome.Reason != ExecTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome = %+v, want timeout", outcome)
	}
	if outcome.Signal == "" {
		t.Fatalf("timeout outcome lacks the final process signal: %+v", outcome)
	}
	if outcome.Elapsed < 40*time.Millisecond {
		t.Fatalf("elapsed = %s, shorter than configured timeout", outcome.Elapsed)
	}
	if outcome.Elapsed > time.Second {
		t.Fatalf("elapsed = %s; process group was not killed promptly", outcome.Elapsed)
	}
}

func TestExecServiceUnixCallerCancellation(t *testing.T) {
	dir := t.TempDir()
	service := newUnixTestExecService(5*time.Second, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan ExecOutcome, 1)
	go func() {
		outcomes <- service.Run(ctx, ExecRequest{
			Command: "touch started; sleep 2",
			CWD:     dir,
		})
	}()

	marker := filepath.Join(dir, "started")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	outcome := <-outcomes
	if outcome.Reason != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("outcome = %+v, want caller cancellation", outcome)
	}
	if outcome.Elapsed > time.Second {
		t.Fatalf("elapsed = %s; cancelled process group was not terminated promptly", outcome.Elapsed)
	}
}

func TestExecServiceUnixCancellationKillsSurvivingGroupDescendant(t *testing.T) {
	dir := t.TempDir()
	service := newUnixTestExecService(5*time.Second, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan ExecOutcome, 1)
	go func() {
		outcomes <- service.Run(ctx, ExecRequest{
			// The direct shell uses the default TERM action. Its background
			// child ignores TERM and redirects all descriptors away from the
			// captured pipes, so direct-child Wait can finish during grace.
			Command: `(trap '' TERM; while :; do printf x >> descendant.alive; sleep 0.02; done) </dev/null >/dev/null 2>&1 & printf '%s' "$!" > descendant.pid; wait`,
			CWD:     dir,
		})
	}()

	pidPath := filepath.Join(dir, "descendant.pid")
	pid, err := waitForPIDFile(pidPath, time.Second)
	if err != nil {
		cancel()
		<-outcomes
		t.Fatal(err)
	}
	cleanupNeeded := true
	t.Cleanup(func() {
		if cleanupNeeded {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	markerPath := filepath.Join(dir, "descendant.alive")
	if err := waitForNonEmptyFile(markerPath, time.Second); err != nil {
		cancel()
		<-outcomes
		t.Fatal(err)
	}

	cancel()
	var outcome ExecOutcome
	select {
	case outcome = <-outcomes:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("cancelled command did not return")
	}
	if outcome.Reason != ExecCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("outcome = %+v, want caller cancellation", outcome)
	}
	before, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	after, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("descendant pid %d survived process-group cleanup: marker grew from %d to %d bytes",
			pid, before.Size(), after.Size())
	}
	// The activity marker proves the process is no longer executing. Avoid
	// signalling its numeric PID again after success, when it may be reaped
	// and reused before test cleanup runs.
	cleanupNeeded = false
}

func TestExecServiceUnixReapsDirectChild(t *testing.T) {
	service := newUnixTestExecService(time.Second, 4096)
	outcome := service.Run(context.Background(), ExecRequest{
		Command: `printf '%s' $$`,
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecSucceeded {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(outcome.Output.Stdout))
	if err != nil {
		t.Fatalf("parse shell pid %q: %v", outcome.Output.Stdout, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("signal reaped pid %d = %v, want ESRCH", pid, err)
	}
}

func newUnixTestExecService(timeout time.Duration, outputLimit int) *ExecService {
	return newExecService(newDirectExecLauncher(), execServiceConfig{
		concurrency:  execConcurrencyLimit,
		timeout:      timeout,
		outputLimit:  outputLimit,
		resolveShell: func() string { return "/bin/sh" },
	})
}

func waitForPIDFile(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents))); parseErr == nil && pid > 0 {
				return pid, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, errors.New("descendant pid file was not populated")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForNonEmptyFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("descendant activity marker was not populated")
		}
		time.Sleep(time.Millisecond)
	}
}
