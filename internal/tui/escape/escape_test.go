package escape

import "testing"

func TestInlineEscapesControls(t *testing.T) {
	if got, want := Inline("a\x00\n\x1b\x7f\u009bb"), "a␀␊␛␡\\u009Bb"; got != want {
		t.Errorf("Inline() = %q, want %q", got, want)
	}
}

func TestMultilinePreservesLinesAndRendersOtherControls(t *testing.T) {
	input := "first\r\nM\tpath\rprogress\b\f\vnext\x1b[31m"
	want := "first\nM    path␍progress␈␌␋next␛[31m"
	if got := Multiline(input); got != want {
		t.Errorf("Multiline() = %q, want %q", got, want)
	}
}

func TestMarkdownEscapingStages(t *testing.T) {
	input := "first\t\r\nsecond\r\b\f\v\x1b[31m"
	want := "first\t\nsecond␍␈␌␋␛[31m"
	if got := MarkdownSource(input); got != want {
		t.Errorf("MarkdownSource() = %q, want %q", got, want)
	}

	if got, want := RenderTabs("a\tb\t"), "a    b    "; got != want {
		t.Errorf("RenderTabs() = %q, want %q", got, want)
	}

	input = "\x1b[38;5;252mstyled\x1b[m\t" +
		"\x1b]8;id=1;https://example.com\x07link\x1b]8;;\x07" +
		"\x1b]0;title\x07"
	want = "\x1b[38;5;252mstyled\x1b[m    " +
		"\x1b]8;id=1;https://example.com\x07link\x1b]8;;\x07" +
		"␛]0;title␇"
	if got := StyledMultiline(input); got != want {
		t.Errorf("StyledMultiline() = %q, want %q", got, want)
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
