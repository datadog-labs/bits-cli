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

func TestBidiControlsAreNeutralizedWithVisibleMarkers(t *testing.T) {
	bidi := "\u202A\u202B\u202C\u202D\u202E\u2066\u2067\u2068\u2069\u200E\u200F\u061C"
	want := "\\u202A\\u202B\\u202C\\u202D\\u202E\\u2066\\u2067\\u2068\\u2069\\u200E\\u200F\\u061C"
	if got := Inline("safe" + bidi + "rm -rf x"); got != "safe"+want+"rm -rf x" {
		t.Errorf("Inline() = %q", got)
	}
	if got := Multiline("a" + bidi + "b"); got != "a"+want+"b" {
		t.Errorf("Multiline() = %q", got)
	}
	if got := MarkdownSource("a" + bidi + "b"); got != "a"+want+"b" {
		t.Errorf("MarkdownSource() = %q", got)
	}
	styled := "\x1b[38;5;252m" + bidi + "\x1b[m"
	if got := StyledMultiline(styled); got != "\x1b[38;5;252m"+want+"\x1b[m" {
		t.Errorf("StyledMultiline() = %q", got)
	}
}

func TestBidiNeutralizationKeepsJoinersAndVariationSelectors(t *testing.T) {
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467"        // family ZWJ sequence
	flag := "\U0001F3F3\uFE0F\u200D\U0001F308"                    // rainbow flag with VS16
	persian := "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645" // ZWNJ inside Persian text
	for _, value := range []string{family, flag, persian} {
		if got := Inline(value); got != value {
			t.Errorf("Inline(%q) = %q, want unchanged", value, got)
		}
		if got := Multiline(value); got != value {
			t.Errorf("Multiline(%q) = %q, want unchanged", value, got)
		}
		if got := StyledMultiline(value); got != value {
			t.Errorf("StyledMultiline(%q) = %q, want unchanged", value, got)
		}
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
