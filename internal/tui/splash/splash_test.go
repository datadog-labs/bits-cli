package splash

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The probe answers asynchronously, so the form can change after the first
// paint; differing heights would make the caller's block jump.
func TestBothFormsAreExactlyRowsTall(t *testing.T) {
	for name, form := range map[string]string{"wordmark": Wordmark(), "placeholder": Placeholder()} {
		if lines := strings.Count(form, "\n") + 1; lines != Rows {
			t.Errorf("%s is %d lines, want %d", name, lines, Rows)
		}
	}
}

// A ragged line would break the horizontal join with the fact column.
func TestWordmarkIsRectangular(t *testing.T) {
	lines := strings.Split(Wordmark(), "\n")
	want := ansi.StringWidth(lines[0])
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != want {
			t.Errorf("line %d is %d columns wide, want %d", i, got, want)
		}
	}
}
