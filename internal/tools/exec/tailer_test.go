package exec

import "testing"

func TestHeadTailBufferAndCombinedOutputLimit(t *testing.T) {
	stdoutLimit, stderrLimit := splitOutputLimit(10)
	stdout := newHeadTailBuffer(stdoutLimit)
	stderr := newHeadTailBuffer(stderrLimit)
	_, _ = stdout.Write([]byte("abc"))
	_, _ = stdout.Write([]byte("defgh"))
	_, _ = stdout.Write([]byte("ijklmnop"))
	_, _ = stderr.Write([]byte("0123456789ABCDEF"))

	output := boundedExecOutput(stdout, stderr)
	if output.Stdout != "abcop" {
		// Each stream gets five bytes: a three-byte head and two-byte tail.
		t.Fatalf("stdout = %q, want %q", output.Stdout, "abcop")
	}
	if output.Stderr != "012EF" {
		t.Fatalf("stderr = %q, want %q", output.Stderr, "012EF")
	}
	if got := len(output.Stdout) + len(output.Stderr); got != 10 {
		t.Fatalf("retained bytes = %d, want 10", got)
	}
	if output.StdoutOmittedBytes != 11 || output.StderrOmittedBytes != 11 || !output.Truncated {
		t.Fatalf("output metadata = %+v, want 11 omitted bytes per stream", output)
	}
}

func TestCombinedOutputLimitDoesNotDonateQuietStreamBudget(t *testing.T) {
	stdoutLimit, stderrLimit := splitOutputLimit(10)
	stdout := newHeadTailBuffer(stdoutLimit)
	stderr := newHeadTailBuffer(stderrLimit)
	_, _ = stdout.Write([]byte("ok"))
	_, _ = stderr.Write([]byte("0123456789ABCDEF"))

	output := boundedExecOutput(stdout, stderr)
	if output.Stdout != "ok" || output.Stderr != "012EF" {
		t.Fatalf("output = %+v, want full stdout and fixed-budget head/tail stderr", output)
	}
	if output.StdoutOmittedBytes != 0 || output.StderrOmittedBytes != 11 {
		t.Fatalf("omitted bytes = (%d, %d), want (0, 11)", output.StdoutOmittedBytes, output.StderrOmittedBytes)
	}
	if got := stdout.limit() + stderr.limit(); got != 10 {
		t.Fatalf("live collector capacity = %d, want combined cap 10", got)
	}
}

func TestSplitOutputLimit(t *testing.T) {
	stdout, stderr := splitOutputLimit(11)
	if stdout != 6 || stderr != 5 {
		t.Fatalf("splitOutputLimit(11) = (%d, %d), want (6, 5)", stdout, stderr)
	}
}
