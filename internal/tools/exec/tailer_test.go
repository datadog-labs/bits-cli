package exec

import (
	"testing"
	"unicode/utf8"
)

func TestBoundedExecOutputPreservesSplitUTF8(t *testing.T) {
	const value = "café 日本語 😀"
	for _, limit := range []int{0, 6, 9, 64} {
		stdout := newHeadTailBuffer(limit)
		// Split every multi-byte character across writes, including characters
		// that will be omitted from the retained head/tail output.
		for _, b := range []byte(value) {
			_, _ = stdout.Write([]byte{b})
		}
		output := readExecOutput(stdout, newHeadTailBuffer(0), new([2]int64), true)
		if !utf8.ValidString(output.Stdout) {
			t.Errorf("limit %d: output = %q, want valid UTF-8", limit, output.Stdout)
		}
		if omitted := int64(len(value) - min(limit, len(value))); output.StdoutOmittedBytes != omitted {
			t.Errorf("limit %d: omitted %d bytes, want %d", limit, output.StdoutOmittedBytes, omitted)
		}
	}
}

func TestHeadTailBufferAndCombinedOutputLimit(t *testing.T) {
	stdoutLimit, stderrLimit := splitOutputLimit(10)
	stdout := newHeadTailBuffer(stdoutLimit)
	stderr := newHeadTailBuffer(stderrLimit)
	_, _ = stdout.Write([]byte("abc"))
	_, _ = stdout.Write([]byte("defgh"))
	_, _ = stdout.Write([]byte("ijklmnop"))
	_, _ = stderr.Write([]byte("0123456789ABCDEF"))

	output := readExecOutput(stdout, stderr, new([2]int64), true)
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

	output := readExecOutput(stdout, stderr, new([2]int64), true)
	if output.Stdout != "ok" || output.Stderr != "012EF" {
		t.Fatalf("output = %+v, want full stdout and fixed-budget head/tail stderr", output)
	}
	if output.StdoutOmittedBytes != 0 || output.StderrOmittedBytes != 11 {
		t.Fatalf("omitted bytes = (%d, %d), want (0, 11)", output.StdoutOmittedBytes, output.StderrOmittedBytes)
	}
	if got := cap(stdout.head) + len(stdout.tail) + cap(stderr.head) + len(stderr.tail); got != 10 {
		t.Fatalf("live collector capacity = %d, want combined cap 10", got)
	}
}

// Reads past a cursor return each byte once, report what the buffer dropped
// in between, and leave a character split across writes for the next read.
func TestHeadTailBufferReadsSinceCursor(t *testing.T) {
	// Windows decodes a final partial character with the console code page.
	partial := decodeExecOutput([]byte("\xe6\x97"))
	type read struct {
		write   string
		final   bool
		want    string
		omitted int64
	}
	tests := []struct {
		name  string
		limit int
		reads []read
	}{
		{name: "contiguous", limit: 64, reads: []read{
			{write: "abc", want: "abc"},
			{write: "def", want: "def"},
			{final: true, want: ""},
		}},
		{name: "gap between reads", limit: 6, reads: []read{
			{write: "ab", want: "ab"},
			{write: "cdefghij", want: "chij", omitted: 4},
			{write: "k", final: true, want: "k"},
		}},
		{name: "split character", limit: 64, reads: []read{
			{write: "caf\xc3", want: "caf"},
			{write: "\xa9!", want: "é!"},
		}},
		{name: "final flushes partial character", limit: 64, reads: []read{
			{write: "ok\xe6\x97", want: "ok"},
			{final: true, want: partial},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newHeadTailBuffer(test.limit)
			var cursor int64
			for i, r := range test.reads {
				_, _ = b.Write([]byte(r.write))
				var got string
				var omitted int64
				got, omitted, cursor = b.since(cursor, r.final)
				if got != r.want || omitted != r.omitted {
					t.Fatalf("read %d = (%q, %d), want (%q, %d)", i, got, omitted, r.want, r.omitted)
				}
			}
		})
	}
}

func TestSplitOutputLimit(t *testing.T) {
	stdout, stderr := splitOutputLimit(11)
	if stdout != 6 || stderr != 5 {
		t.Fatalf("splitOutputLimit(11) = (%d, %d), want (6, 5)", stdout, stderr)
	}
}
