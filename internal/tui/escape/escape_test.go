package escape

import "testing"

func TestInlineEscapesControls(t *testing.T) {
	if got, want := Inline("a\x00\n\x1b\x7f\u009bb"), "a␀␊␛␡\\u009Bb"; got != want {
		t.Errorf("Inline() = %q, want %q", got, want)
	}
}

func TestMultilinePreservesLineFeeds(t *testing.T) {
	if got, want := Multiline("first\nsecond\x1b[31m\r"), "first\nsecond␛[31m␍"; got != want {
		t.Errorf("Multiline() = %q, want %q", got, want)
	}
}

func TestSingleLineStripsTerminalFormattingAndCollapsesWhitespace(t *testing.T) {
	if got, want := SingleLine(" a\x1b[31mred\x1b[0m\n\u200b b "), "ared b"; got != want {
		t.Errorf("SingleLine() = %q, want %q", got, want)
	}
}

func TestCommonTextPassesThroughUnchanged(t *testing.T) {
	const singleLine = "plain display text"
	const multiline = "first line\nsecond line"
	if got := Inline(singleLine); got != singleLine {
		t.Errorf("Inline() = %q, want unchanged text", got)
	}
	if got := Multiline(multiline); got != multiline {
		t.Errorf("Multiline() = %q, want unchanged text", got)
	}
	if got := SingleLine(singleLine); got != singleLine {
		t.Errorf("SingleLine() = %q, want unchanged text", got)
	}
}
