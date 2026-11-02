//go:build windows

package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSupportedExecShellWindows(t *testing.T) {
	for _, shell := range []string{"pwsh", "pwsh.exe", "PowerShell.EXE", `C:\Program Files\PowerShell\7\pwsh.exe`, "bash", "BASH.EXE", `C:\Program Files\Git\bin\bash.exe`} {
		if !supportedExecShell(shell) {
			t.Errorf("supportedExecShell(%q) = false", shell)
		}
	}
	for _, shell := range []string{"cmd.exe", "bash.cmd", "bash.bat", "fish"} {
		if supportedExecShell(shell) {
			t.Errorf("supportedExecShell(%q) = true", shell)
		}
	}
}

func TestShellEnvWithoutExcludedNamesWindows(t *testing.T) {
	got := shellEnvWithoutExcludedNames([]string{
		"Path=C:\\Windows", "private_token=hidden", "PRIVATE_TOKEN=also-hidden",
		"Visible_Key=visible",
	}, []string{"PRIVATE_TOKEN"})
	if want := []string{"Path=C:\\Windows", "Visible_Key=visible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shell environment = %q, want %q", got, want)
	}
}

func TestWindowsExecOutputText(t *testing.T) {
	for _, test := range []struct {
		name     string
		bytes    []byte
		codePage uint32
		want     string
	}{
		{"empty", nil, 437, ""},
		{"UTF-8", []byte("café 日本語 😀"), 437, "café 日本語 😀"},
		{"OEM 437", []byte("caf\x82"), 437, "café"},
		{"OEM 866", []byte{0x8f, 0xe0, 0xa8, 0xa2, 0xa5, 0xe2}, 866, "Привет"},
		{"Windows 1252", []byte("\x93caf\xe9\x94"), 1252, "“café”"},
		{"Shift-JIS", []byte{0x93, 0xfa, 0x96, 0x7b}, 932, "日本"},
		{"embedded NUL", []byte("caf\x82\x00!"), 437, "café\x00!"},
		{"unknown code page", []byte("bad\xff"), 99999, "bad\uFFFD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsExecOutputText(test.bytes, test.codePage); got != test.want {
				t.Fatalf("decoded = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecServiceWindowsPowerShellUnicodeOutput(t *testing.T) {
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(name, func(t *testing.T) {
			shell, err := osexec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			const want = "65001|café 日本語 😀"
			outcome := NewExecService(shell).Run(t.Context(), ExecRequest{
				Command: `[Console]::Out.Write([string][Console]::Out.Encoding.CodePage + '|café 日本語 😀'); ` +
					`[Console]::Error.Write([string][Console]::Error.Encoding.CodePage + '|café 日本語 😀'); exit 7`,
				CWD: t.TempDir(),
			})
			if outcome.Reason != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
				t.Fatalf("outcome = %+v, want exit 7", outcome)
			}
			if outcome.Output.Stdout != want || outcome.Output.Stderr != want {
				t.Fatalf("output = %+v, want Unicode on both streams", outcome.Output)
			}
		})
	}
}

func TestWindowsPowerShell51RedirectedUTF8Output(t *testing.T) {
	shell, err := osexec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell 5.1 is not installed")
	}

	// Seed both Console writers with a legacy encoding before the production
	// prefix runs. This makes the regression visible even on a UTF-8 locale.
	const legacyWriters = `$legacy = [System.Text.Encoding]::GetEncoding(1252)
$out = [System.IO.StreamWriter]::new([Console]::OpenStandardOutput(), $legacy)
$out.AutoFlush = $true
[Console]::SetOut($out)
$err = [System.IO.StreamWriter]::new([Console]::OpenStandardError(), $legacy)
$err.AutoFlush = $true
[Console]::SetError($err)
if ([Console]::Out.Encoding.CodePage -ne 1252 -or [Console]::Error.Encoding.CodePage -ne 1252) {
    throw 'legacy writer setup failed'
}
`
	const probe = `[Console]::Out.Write('日本語 😀')
[Console]::Error.Write('日本語 😀')`
	args := windowsExecShellArgs(shell, probe)
	args[len(args)-1] = legacyWriters + args[len(args)-1]
	command := osexec.CommandContext(t.Context(), shell, args...)
	command.Dir = t.TempDir()
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("redirected PowerShell failed: %v; stderr = %q", err, stderr.String())
	}
	const want = "日本語 😀"
	if stdout.String() != want || stderr.String() != want {
		t.Fatalf("raw output = (%q, %q), want UTF-8 %q on both pipes", stdout.String(), stderr.String(), want)
	}
}

func TestExecServiceWindowsLegacyOutput(t *testing.T) {
	// Write raw bytes to the inherited handles so PowerShell cannot transcode
	// them first. This models native programs that keep using a legacy code page.
	outcome := NewExecService(testPowerShell(t)).Run(t.Context(), ExecRequest{
		Command: `$bytes = [byte[]](0x63, 0x61, 0x66, 0x82); ` +
			`[Console]::OpenStandardOutput().Write($bytes, 0, $bytes.Length); ` +
			`[Console]::OpenStandardError().Write($bytes, 0, $bytes.Length)`,
		CWD: t.TempDir(),
	})
	// The expected glyph is locale-dependent; the fixed code-page cases above
	// independently verify the conversion, while this checks the live pipes.
	want := decodeExecOutput([]byte("caf\x82"))
	if outcome.Reason != ExecSucceeded || outcome.Output.Stdout != want || outcome.Output.Stderr != want {
		t.Fatalf("outcome = %+v, want decoded output %q", outcome, want)
	}
}

func TestExecServiceWindowsPowerShellSuccess(t *testing.T) {
	shell := testPowerShell(t)
	t.Setenv("BITS_EXEC_TEST_VALUE", "inherited-value")
	dir := t.TempDir()
	service := newWindowsTestExecService(shell, 5*time.Second)
	outcome := service.Run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write("stdout:$env:BITS_EXEC_TEST_VALUE"); [Console]::Error.Write("stderr")`,
		CWD:     dir,
	})

	if outcome.Reason != ExecSucceeded || outcome.ExitCode == nil || *outcome.ExitCode != 0 {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if outcome.Output.Stdout != "stdout:inherited-value" || outcome.Output.Stderr != "stderr" {
		t.Fatalf("output = %+v", outcome.Output)
	}
}

func TestExecServiceWindowsExcludesEnvironmentCaseInsensitively(t *testing.T) {
	shell := testPowerShell(t)
	t.Setenv("bits_exec_test_private_token", "hidden")
	t.Setenv("BITS_EXEC_TEST_VISIBLE_KEY", "visible")
	outcome := NewExecService(shell, "BITS_EXEC_TEST_PRIVATE_TOKEN").Run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write("$env:bits_exec_test_private_token|$env:BITS_EXEC_TEST_VISIBLE_KEY")`,
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecSucceeded || outcome.Output.Stdout != "|visible" {
		t.Fatalf("excluded environment outcome = %+v", outcome)
	}
}

func TestExecServiceWindowsPowerShellWorkdirAndNonZeroExit(t *testing.T) {
	shell := testPowerShell(t)
	dir := filepath.Join(t.TempDir(), "directory with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outcome := newWindowsTestExecService(shell, 5*time.Second).Run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write((Get-Location).Path); exit 7`,
		CWD:     dir,
	})
	if outcome.Reason != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
		t.Fatalf("outcome = %+v, want exit status 7", outcome)
	}
	// PowerShell can expand an 8.3 path supplied through the temp directory.
	got, err := os.Stat(outcome.Output.Stdout)
	if err != nil {
		t.Fatalf("stat reported working directory %q: %v", outcome.Output.Stdout, err)
	}
	want, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(got, want) {
		t.Fatalf("working directory output = %q, want %q", outcome.Output.Stdout, dir)
	}
}

func TestExecServiceWindowsPowerShellTimeoutKillsDirectProcess(t *testing.T) {
	shell := testPowerShell(t)
	outcome := newWindowsTestExecService(shell, 50*time.Millisecond).Run(context.Background(), ExecRequest{
		Command: "Start-Sleep -Seconds 30",
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome = %+v, want timeout", outcome)
	}
	if outcome.Elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", outcome.Elapsed)
	}
}

func testPowerShell(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := osexec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Fatal("PowerShell is not available")
	return ""
}

func newWindowsTestExecService(shell string, timeout time.Duration) *ExecService {
	return newExecService(newDirectExecLauncher(), execServiceConfig{
		concurrency:      1,
		timeout:          timeout,
		outputLimit:      4096,
		defaultShellPath: shell,
	})
}

func testWindowsBash(t *testing.T) string {
	t.Helper()
	path, err := osexec.LookPath("bash.exe")
	if err != nil {
		t.Skip("Bash is optional; install Git for Windows and add Bash to PATH to run this test")
	}
	return path
}

func TestExecServiceWindowsResolvesRequestedBash(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin with spaces")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(bin, "bash.exe")
	// Resolution only: this file must never be launched.
	if err := os.WriteFile(shell, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("PATH", bin)
	t.Setenv("PATHEXT", ".EXE")
	service := NewExecService("powershell.exe")
	for _, requested := range []string{"bash", "bash.exe", shell, filepath.Join("bin with spaces", "bash.exe")} {
		plan, err := service.resolve(ExecRequest{Command: "ignored", CWD: t.TempDir(), Shell: requested})
		if err != nil || plan.Shell != shell {
			t.Errorf("resolve(%q) = %q, %v; want %q", requested, plan.Shell, err, shell)
		}
	}
	// An explicit missing path must not be replaced by the Bash on PATH.
	missing := filepath.Join(root, "missing", "bash.exe")
	if _, err := service.resolve(ExecRequest{Command: "ignored", CWD: root, Shell: missing}); err == nil {
		t.Fatal("missing explicit Bash path was accepted")
	}
	t.Setenv("PATH", t.TempDir())
	outcome := service.Run(t.Context(), ExecRequest{Command: "ignored", CWD: root, Shell: "bash"})
	if outcome.Reason != ExecLaunchFailed || outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "resolve exec shell") {
		t.Fatalf("missing Bash must fail resolution without falling back: %+v", outcome)
	}
}

func TestExecServiceWindowsBashEnvironmentAndExit(t *testing.T) {
	shell := testWindowsBash(t)
	t.Setenv("BITS_EXEC_TEST_VALUE", "spaces 'quotes' $dollar")
	t.Setenv("BITS_EXEC_TEST_PRIVATE", "hidden")
	outcome := NewExecService(testPowerShell(t), "BITS_EXEC_TEST_PRIVATE").Run(t.Context(), ExecRequest{
		Shell: shell,
		Command: `printf '%s|%s' "$BITS_EXEC_TEST_VALUE" "${BITS_EXEC_TEST_PRIVATE-unset}"; ` +
			`printf stderr >&2; if read -r ignored; then exit 9; fi; exit 7`,
		CWD: t.TempDir(),
	})
	if outcome.Reason != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
		t.Fatalf("outcome = %+v, want exit 7", outcome)
	}
	if outcome.Output.Stdout != "spaces 'quotes' $dollar|unset" || outcome.Output.Stderr != "stderr" {
		t.Fatalf("output = %+v", outcome.Output)
	}
}

func TestExecJobProcessTree(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("PowerShell", func(t *testing.T) {
		command := "& '" + strings.ReplaceAll(executable, "'", "''") + "' '-test.run=^TestExecJobHelper$' '--' '0'"
		testExecJobProcessTree(t, testPowerShell(t), command)
	})
	t.Run("Bash", func(t *testing.T) {
		command := "'" + strings.ReplaceAll(filepath.ToSlash(executable), "'", "'\\''") + "' '-test.run=^TestExecJobHelper$' '--' '0'"
		testExecJobProcessTree(t, testWindowsBash(t), command)
	})
}

func testExecJobProcessTree(t *testing.T, shell, command string) {
	t.Helper()
	for _, mode := range []string{"cancel", "timeout", "shell-exit"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("BITS_EXEC_JOB_HELPER_DIR", dir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan ExecOutcome, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Error("execution did not finish during cleanup")
				}
			})
			go func() {
				defer close(finished)
				done <- newWindowsTestExecService(shell, 15*time.Second).Run(ctx, ExecRequest{
					Command: command,
					CWD:     dir,
				})
			}()
			// Hold process handles before cancellation so PID reuse cannot make
			// assertions pass accidentally. Each helper spawns the next level
			// immediately, exercising inheritance before the shell can exit.
			var processes []windows.Handle
			for level := range 3 {
				var pid int
				awaitExecJobCondition(t, func() bool {
					data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(level)))
					if err != nil {
						return false
					}
					pid, err = strconv.Atoi(string(data))
					return err == nil
				})
				handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = windows.TerminateProcess(handle, 1)
					_ = windows.CloseHandle(handle)
				})
				processes = append(processes, handle)
			}
			want := ExecCancelled
			switch mode {
			case "cancel":
				cancel()
			case "timeout":
				want = ExecTimedOut
			case "shell-exit":
				want = ExecSucceeded
				if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case outcome := <-done:
				if outcome.Reason != want {
					t.Fatalf("outcome = %+v, want %s", outcome, want)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("execution did not finish")
			}
			for level, handle := range processes {
				status, err := windows.WaitForSingleObject(handle, 5000)
				if err != nil || status != windows.WAIT_OBJECT_0 {
					t.Errorf("descendant %d survived: wait status %d, error %v", level, status, err)
				}
			}
		})
	}
}

// TestExecJobHelper runs only in child test binaries. Descendants deliberately
// outlive their parent and inherit its output handles.
func TestExecJobHelper(t *testing.T) {
	dir := os.Getenv("BITS_EXEC_JOB_HELPER_DIR")
	if dir == "" {
		return
	}
	level, err := strconv.Atoi(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	if level < 2 {
		child := osexec.Command(os.Args[0], "-test.run=^TestExecJobHelper$", "--", strconv.Itoa(level+1))
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Process.Release() }()
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(level)), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if level == 0 {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper safety timeout")
}

func TestExecJobConcurrentCloseAndTerminate(t *testing.T) {
	job, err := newExecJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := job.terminate(); err != nil {
				t.Errorf("terminate: %v", err)
			}
		})
		wg.Go(job.close)
	}
	wg.Wait()
}

func TestExecJobLaunchFailure(t *testing.T) {
	_, err := newDirectExecLauncher().Start(ExecPlan{
		Shell: filepath.Join(t.TempDir(), "missing.exe"),
		CWD:   t.TempDir(),
	}, os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("missing executable should fail to launch")
	}
}

func awaitExecJobCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for descendant readiness")
}
