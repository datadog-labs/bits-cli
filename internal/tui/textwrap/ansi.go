package textwrap

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// FromANSI splits text already styled with SGR escape sequences, such as
// syntax-highlighter output, into segments that each carry the styling in
// effect for their text. Escape sequences other than SGR (cursor movement,
// hyperlinks, …) are dropped: they carry no width and cannot survive being
// split across rows.
func FromANSI(s string) []Segment {
	var segs []Segment
	var text strings.Builder
	pen := "" // SGR sequences in effect since the last reset
	flush := func() {
		if text.Len() == 0 {
			return
		}
		seg := Segment{Text: text.String()}
		if pen != "" {
			seg.Style = sgrStyle(pen)
		}
		segs = append(segs, seg)
		text.Reset()
	}

	var state byte
	for s != "" {
		seq, _, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		s = s[n:]
		switch {
		case !isEscape(seq):
			text.WriteString(seq)
		case isSGR(seq):
			flush()
			params := seq[2 : len(seq)-1]
			switch {
			case params == "" || params == "0":
				pen = ""
			case strings.HasPrefix(params, "0;"):
				pen = seq
			default:
				pen += seq
			}
		}
	}
	flush()
	return segs
}

func isEscape(seq string) bool {
	return seq != "" && (seq[0] == ansi.ESC || seq[0] == ansi.CSI || seq[0] == ansi.OSC || seq[0] == ansi.DCS || seq[0] == ansi.APC)
}

// isSGR reports whether seq is a 7-bit CSI … m sequence without
// intermediates.
func isSGR(seq string) bool {
	if len(seq) < 3 || !strings.HasPrefix(seq, "\x1b[") || seq[len(seq)-1] != 'm' {
		return false
	}
	return strings.Trim(seq[2:len(seq)-1], "0123456789;:") == ""
}

// sgrStyle replays a recorded SGR pen around text.
type sgrStyle string

func (s sgrStyle) Render(strs ...string) string {
	return string(s) + strings.Join(strs, " ") + ansi.ResetStyle
}
