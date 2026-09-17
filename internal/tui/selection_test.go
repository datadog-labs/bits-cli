package tui

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func testSelectionFrame(content string, width, height int) selectionFrame {
	return newSelectionFrame(content, width, height, nil)
}

func TestSelectionCopiesRowsInEitherDirection(t *testing.T) {
	frame := testSelectionFrame("abcde\nfghij\nklmno", 5, 3)

	tests := []struct {
		name   string
		anchor image.Point
		focus  image.Point
		want   string
	}{
		{name: "forward", anchor: image.Pt(3, 0), focus: image.Pt(1, 1), want: "de\nfg"},
		{name: "backward", anchor: image.Pt(1, 1), focus: image.Pt(3, 0), want: "de\nfg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got selection
			got.begin(frame, tt.anchor.X, tt.anchor.Y)
			if !got.selecting() {
				t.Fatal("begin did not start selection")
			}
			if text := got.finish(frame, tt.focus.X, tt.focus.Y, frame); text != tt.want {
				t.Fatalf("finish() = %q, want %q", text, tt.want)
			}
			if got.selecting() {
				t.Fatal("finish left mouse drag active")
			}
			if !got.selected() {
				t.Fatal("finish did not retain non-empty selection")
			}
		})
	}
}

func TestSelectionRemainsRenderedAfterFinishUntilCleared(t *testing.T) {
	frame := testSelectionFrame("hello", 5, 1)
	var got selection
	got.begin(frame, 1, 0)
	if text := got.finish(frame, 3, 0, frame); text != "ell" {
		t.Fatalf("finish() = %q, want %q", text, "ell")
	}
	if got.selecting() {
		t.Fatal("finished selection still reports active drag")
	}
	if !got.selected() {
		t.Fatal("finished selection was not retained")
	}
	if rendered := got.render(frame); !strings.Contains(rendered, "7") {
		t.Fatalf("render() = %q, want retained reverse highlight", rendered)
	}

	got.clear()
	if got.selected() || got.selecting() {
		t.Fatal("clear did not remove persistent selection")
	}
	if rendered := got.render(frame); rendered != frame.content {
		t.Fatalf("render() after clear = %q, want original content", rendered)
	}

	got.begin(frame, 4, 0)
	if got.selected() {
		t.Fatal("new click retained the previous selection")
	}
	if !got.selecting() {
		t.Fatal("new click did not start a drag")
	}
}

func TestSelectionUsesVirtualRowsAfterScrolling(t *testing.T) {
	visible := newSelectionFrame("FGHIJ\nKLMNO", 5, 2, []int{5, 6})
	document := testSelectionFrame("a0000\nb1111\nc2222\nd3333\ne4444\nfghij\nklmno\np7777", 5, 8)

	var got selection
	got.begin(visible, 1, 0)
	if rendered := got.render(visible); ansi.Strip(rendered) != "FGHIJ\nKLMNO" {
		t.Fatalf("single-point render changed visible frame: %q", rendered)
	}
	if text := got.finish(visible, 1, 1, document); text != "ghij\nkl" {
		t.Fatalf("finish() = %q, want %q", text, "ghij\nkl")
	}
}

func TestSelectionRendersReverseAndCopiesANSIText(t *testing.T) {
	frame := testSelectionFrame("\x1b[31mA界B\x1b[0m", 5, 1)
	var got selection
	got.begin(frame, 2, 0) // The continuation cell of 界.
	got.extend(frame, 3, 0)

	rendered := got.render(frame)
	if ansi.Strip(rendered) != "A界B " {
		t.Fatalf("render() stripped text = %q, want %q", ansi.Strip(rendered), "A界B ")
	}
	if !strings.Contains(rendered, "7") {
		t.Fatalf("render() = %q, want reverse attribute", rendered)
	}
	if text := got.finish(frame, 3, 0, frame); text != "界B" {
		t.Fatalf("finish() = %q, want %q", text, "界B")
	}
}

func TestSelectionUsesGraphemeWidths(t *testing.T) {
	family := "👨‍👩‍👧‍👦"
	frame := testSelectionFrame(family+"x", 3, 1)

	var got selection
	got.begin(frame, 0, 0)
	if text := got.finish(frame, 2, 0, frame); text != family+"x" {
		t.Fatalf("finish() = %q, want %q", text, family+"x")
	}
}

func TestSelectionClickWithoutMovementCopiesNothing(t *testing.T) {
	frame := testSelectionFrame("hello", 5, 1)
	var got selection
	got.begin(frame, 2, 0)
	if text := got.finish(frame, 2, 0, frame); text != "" {
		t.Fatalf("finish() = %q, want empty text", text)
	}
	if got.selecting() {
		t.Fatal("click selection remained active")
	}
	if got.selected() {
		t.Fatal("click selection was retained")
	}
}

func TestSelectionGestureStaysInOriginPane(t *testing.T) {
	frame := testSelectionFrame("aaaaa\nbbbbb\nccccc\nddddd", 5, 4)

	var transcript selection
	transcript.beginGesture(frame, selectionScopeTranscript, 0, 0, 2, 4)
	transcript.extendGesture(frame, 4, 3, 2, 4)
	if transcript.focus != image.Pt(4, 1) || transcript.edge != 1 {
		t.Fatalf("transcript focus/edge = %v/%d, want (4,1)/1", transcript.focus, transcript.edge)
	}
	if text := transcript.finishGesture(frame, 4, 3, 2, 4, frame); text != "aaaaa\nbbbbb" {
		t.Fatalf("transcript selection = %q", text)
	}

	var lower selection
	lower.beginGesture(frame, selectionScopeLower, 4, 3, 2, 4)
	lower.extendGesture(frame, 0, 0, 2, 4)
	if lower.focus != image.Pt(0, 2) || lower.edge != 0 {
		t.Fatalf("lower focus/edge = %v/%d, want (0,2)/0", lower.focus, lower.edge)
	}
	if text := lower.finishGesture(frame, 0, 0, 2, 4, frame); text != "ccccc\nddddd" {
		t.Fatalf("lower selection = %q", text)
	}
}

func TestSelectionAutoScrollReversesWithPointer(t *testing.T) {
	frame := testSelectionFrame("aaaaa\nbbbbb\nccccc", 5, 3)
	var got selection

	got.beginGesture(frame, selectionScopeTranscript, 0, 1, 3, 3)
	got.extendGesture(frame, 4, 2, 3, 3)
	if got.edge != 1 {
		t.Fatalf("forward edge = %d, want 1", got.edge)
	}

	got.extendGesture(frame, 0, 0, 3, 3)
	if got.edge != -1 {
		t.Fatalf("reversed edge = %d, want -1", got.edge)
	}
}
