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

// Once a stream is legacy, a read that alone is valid UTF-8 still decodes
// with the code page: GBK 漏 is c2 a9, which is also UTF-8 ©.
func TestWindowsLegacyText(t *testing.T) {
	if got := windowsLegacyText([]byte{0xc2, 0xa9}, 936); got != "漏" {
		t.Fatalf("decoded = %q, want %q", got, "漏")
	}
	if got := windowsLegacyText(nil, 936); got != "" {
		t.Fatalf("decoded empty = %q", got)
	}
	// A buffer keeps the decision: after an invalid byte, a later read of
	// c2 a9 decodes with the console's legacy code page, never as UTF-8 ©.
	b := newHeadTailBuffer(64)
	_, _ = b.Write([]byte{0xff})
	_, _, cursor := b.since(0, false)
	_, _ = b.Write([]byte{0xc2, 0xa9})
	if got, _, _ := b.since(cursor, false); got == "©" || got != decodeLegacyOutput([]byte{0xc2, 0xa9}) {
		t.Fatalf("later read = %q, want it decoded with the legacy code page", got)
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

// A read holds a legacy character split across writes until it completes.
func TestWindowsIncompleteSuffix(t *testing.T) {
	for _, test := range []struct {
		name     string
		bytes    []byte
		codePage uint32
		want     int
	}{
		{"complete Shift-JIS", []byte{0x93, 0xfa}, 932, 0},
		{"split Shift-JIS", []byte{0x93, 0xfa, 0x96}, 932, 1},
		{"single-byte code page", []byte("caf\x82"), 437, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsIncompleteSuffix(test.bytes, test.codePage); got != test.want {
				t.Fatalf("incomplete suffix = %d, want %d", got, test.want)
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
			outcome := NewExecService(shell).run(t.Context(), ExecRequest{
				Command: `[Console]::Out.Write([string][Console]::Out.Encoding.CodePage + '|café 日本語 😀'); ` +
					`[Console]::Error.Write([string][Console]::Error.Encoding.CodePage + '|café 日本語 😀'); exit 7`,
				CWD: t.TempDir(),
			})
			if outcome.Status != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
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
	args, stdin := windowsExecShellArgs(shell, probe)
	args[len(args)-1] = legacyWriters + args[len(args)-1]
	command := osexec.CommandContext(t.Context(), shell, args...)
	command.Dir = t.TempDir()
	command.Stdin = stdin
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

// PowerShell commands keep native exit codes, their own error positions, and
// no length limit from the command line. Their errors are plain text.
func TestExecServiceWindowsPowerShellCommands(t *testing.T) {
	tests := []struct {
		name, command string
		exitCode      int
		stdout        string
		// stderr is text stderr must contain, or empty if stderr must be.
		stderr string
	}{
		{name: "native exit code", command: "cmd /c exit 7", exitCode: 7},
		{name: "later cmdlet keeps the native exit code", command: "cmd /c exit 7; 'after'", exitCode: 7, stdout: "after"},
		{name: "later native command replaces it", command: "cmd /c exit 7; cmd /c exit 0", exitCode: 0},
		{name: "exit statement", command: "cmd /c exit 7; exit 3", exitCode: 3},
		{name: "negative exit code", command: "[Environment]::Exit(-1073741819)", exitCode: -1073741819},
		{name: "strict mode without a native command", command: "Set-StrictMode -Version Latest; 'ok'", exitCode: 0, stdout: "ok"},
		{name: "suppressed cmdlet error", command: "Get-Item missing -ErrorAction SilentlyContinue", exitCode: 0},
		{name: "terminating error", command: "'ok'\nthrow 'thrown'", exitCode: 1, stdout: "ok", stderr: "throw 'thrown'"},
		{name: "error positions", command: "'ok'\ntry { throw 'thrown' } catch { $_.InvocationInfo.ScriptLineNumber }", exitCode: 0, stdout: "ok\r\n2"},
		{name: "parse error", command: "if (", exitCode: 1, stderr: "At line:1 char:5\r\n+ if ("},
		{name: "formatted objects", command: "[pscustomobject]@{a = 1}", exitCode: 0, stdout: "a\r\n-\r\n1"},
		{name: "native ANSI passes through", command: "cmd /c \"echo $([char]27)[32mgreen\"", exitCode: 0, stdout: "\x1b[32mgreen"},
		{name: "leading using", command: "using namespace System.Text\n[StringBuilder]::new('ok').ToString()", exitCode: 0, stdout: "ok"},
		{name: "stdin at end of file", command: `$line = [Console]::In.ReadLine(); "[$line]"`, exitCode: 0, stdout: "[]"},
		{name: "longer than a command line", command: "('" + strings.Repeat("a", 40000) + "').Length", exitCode: 0, stdout: "40000"},
	}
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(name, func(t *testing.T) {
			shell, err := osexec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					outcome := NewExecService(shell).run(t.Context(), ExecRequest{Command: test.command, CWD: t.TempDir()})
					exitCode := "none"
					if outcome.ExitCode != nil {
						exitCode = strconv.Itoa(*outcome.ExitCode)
					}
					if exitCode != strconv.Itoa(test.exitCode) {
						t.Fatalf("exit code = %s, want %d; outcome = %+v", exitCode, test.exitCode, outcome)
					}
					if got := strings.TrimSpace(outcome.Output.Stdout); got != test.stdout {
						t.Errorf("stdout = %q, want %q", got, test.stdout)
					}
					stderr := outcome.Output.Stderr
					if !strings.Contains(stderr, test.stderr) || (test.stderr == "") != (stderr == "") || strings.Contains(stderr, "\x1b") {
						t.Errorf("stderr = %q, want plain text containing %q", stderr, test.stderr)
					}
				})
			}
		})
	}
}

func TestExecServiceWindowsLegacyOutput(t *testing.T) {
	// Write raw bytes to the inherited handles so PowerShell cannot transcode
	// them first. This models native programs that keep using a legacy code page.
	outcome := NewExecService(testPowerShell(t)).run(t.Context(), ExecRequest{
		Command: `$bytes = [byte[]](0x63, 0x61, 0x66, 0x82); ` +
			`[Console]::OpenStandardOutput().Write($bytes, 0, $bytes.Length); ` +
			`[Console]::OpenStandardError().Write($bytes, 0, $bytes.Length)`,
		CWD: t.TempDir(),
	})
	// The expected glyph is locale-dependent; the fixed code-page cases above
	// independently verify the conversion, while this checks the live pipes.
	want := decodeExecOutput([]byte("caf\x82"))
	if outcome.Status != ExecSucceeded || outcome.Output.Stdout != want || outcome.Output.Stderr != want {
		t.Fatalf("outcome = %+v, want decoded output %q", outcome, want)
	}
}

func TestExecServiceWindowsPowerShellSuccess(t *testing.T) {
	shell := testPowerShell(t)
	t.Setenv("BITS_EXEC_TEST_VALUE", "inherited-value")
	dir := t.TempDir()
	service := newWindowsTestExecService(shell)
	outcome := service.run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write("stdout:$env:BITS_EXEC_TEST_VALUE"); [Console]::Error.Write("stderr")`,
		CWD:     dir,
	})

	if outcome.Status != ExecSucceeded || outcome.ExitCode == nil || *outcome.ExitCode != 0 {
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
	outcome := NewExecService(shell, "BITS_EXEC_TEST_PRIVATE_TOKEN").run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write("$env:bits_exec_test_private_token|$env:BITS_EXEC_TEST_VISIBLE_KEY")`,
		CWD:     t.TempDir(),
	})
	if outcome.Status != ExecSucceeded || outcome.Output.Stdout != "|visible" {
		t.Fatalf("excluded environment outcome = %+v", outcome)
	}
}

func TestExecServiceWindowsPowerShellWorkdirAndNonZeroExit(t *testing.T) {
	shell := testPowerShell(t)
	dir := filepath.Join(t.TempDir(), "directory with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outcome := newWindowsTestExecService(shell).run(context.Background(), ExecRequest{
		Command: `[Console]::Out.Write((Get-Location).Path); exit 7`,
		CWD:     dir,
	})
	if outcome.Status != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
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
	outcome := newWindowsTestExecService(shell).run(context.Background(), ExecRequest{
		Command: "Start-Sleep -Seconds 30",
		CWD:     t.TempDir(),
		Timeout: 50 * time.Millisecond,
	})
	if outcome.Status != ExecTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
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

func newWindowsTestExecService(shell string) *ExecService {
	return newExecService(newDirectExecLauncher(), execServiceConfig{
		concurrency:      1,
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
	outcome := service.run(t.Context(), ExecRequest{Command: "ignored", CWD: root, Shell: "bash"})
	if outcome.Status != ExecLaunchFailed || outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "resolve exec shell") {
		t.Fatalf("missing Bash must fail resolution without falling back: %+v", outcome)
	}
}

func TestExecServiceWindowsBashEnvironmentAndExit(t *testing.T) {
	shell := testWindowsBash(t)
	t.Setenv("BITS_EXEC_TEST_VALUE", "spaces 'quotes' $dollar")
	t.Setenv("BITS_EXEC_TEST_PRIVATE", "hidden")
	outcome := NewExecService(testPowerShell(t), "BITS_EXEC_TEST_PRIVATE").run(t.Context(), ExecRequest{
		Shell: shell,
		Command: `printf '%s|%s' "$BITS_EXEC_TEST_VALUE" "${BITS_EXEC_TEST_PRIVATE-unset}"; ` +
			`printf stderr >&2; if read -r ignored; then exit 9; fi; exit 7`,
		CWD: t.TempDir(),
	})
	if outcome.Status != ExecNonZeroExit || outcome.ExitCode == nil || *outcome.ExitCode != 7 {
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
				done <- newWindowsTestExecService(shell).run(ctx, ExecRequest{
					Command: command,
					CWD:     dir,
					Timeout: 15 * time.Second,
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
				if outcome.Status != want {
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
