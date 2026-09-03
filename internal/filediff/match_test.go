package filediff

import (
	"errors"
	"reflect"
	"testing"
)

func stringPtr(value string) *string { return &value }

func TestMatchEdits(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		edits   []Edit
		want    []Replacement
		wantErr *MatchError
	}{
		{
			name:  "resolves disjoint edits against the original snapshot",
			base:  "one\ntwo\n",
			edits: []Edit{{OldText: "one", NewText: stringPtr("two")}, {OldText: "two", NewText: stringPtr("three")}},
			want:  []Replacement{{Start: 0, Length: 3, NewText: "two"}, {Start: 4, Length: 3, NewText: "three"}},
		},
		{name: "empty old text", base: "x", edits: []Edit{{NewText: stringPtr("x")}}, wantErr: &MatchError{Kind: MatchEmptyOldText, Index: 0, Total: 1}},
		{name: "missing new text", base: "x", edits: []Edit{{OldText: "x"}}, wantErr: &MatchError{Kind: MatchMissingNewText, Index: 0, Total: 1}},
		{name: "missing text", base: "x", edits: []Edit{{OldText: "y", NewText: stringPtr("z")}}, wantErr: &MatchError{Kind: MatchNotFound, Index: 0, Total: 1}},
		{name: "ambiguous text", base: "x\nx\n", edits: []Edit{{OldText: "x", NewText: stringPtr("y")}}, wantErr: &MatchError{Kind: MatchAmbiguous, Index: 0, Total: 1, Count: 2}},
		{name: "self overlapping text is ambiguous", base: "a\n\n\nb", edits: []Edit{{OldText: "\n\n", NewText: stringPtr("\n")}}, wantErr: &MatchError{Kind: MatchAmbiguous, Index: 0, Total: 1, Count: 2}},
		{name: "overlap", base: "abcdef", edits: []Edit{{OldText: "abcd", NewText: stringPtr("x")}, {OldText: "cdef", NewText: stringPtr("y")}}, wantErr: &MatchError{Kind: MatchOverlap}},
		{name: "normalizes edit line endings", base: "a\nb\n", edits: []Edit{{OldText: "a\r\nb", NewText: stringPtr("A\r\nB")}}, want: []Replacement{{Start: 0, Length: 3, NewText: "A\nB"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchEdits(tt.base, tt.edits)
			if tt.wantErr != nil {
				var matchErr *MatchError
				if !errors.As(err, &matchErr) || !reflect.DeepEqual(matchErr, tt.wantErr) {
					t.Fatalf("MatchEdits error = %#v, want %#v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MatchEdits = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyReplacements(t *testing.T) {
	replacements := []Replacement{{Start: 0, Length: 3, NewText: "ONE"}, {Start: 8, Length: 5, NewText: "THREE"}}
	if got := ApplyReplacements("one two three", replacements); got != "ONE two THREE" {
		t.Fatalf("ApplyReplacements = %q", got)
	}
}

func TestLineEndingHelpers(t *testing.T) {
	for _, tt := range []struct {
		text   string
		ending string
		ok     bool
	}{
		{text: "a\nb\n", ending: "\n", ok: true},
		{text: "a\r\nb\r\n", ending: "\r\n", ok: true},
		{text: "a\nb\r\n", ok: false},
		{text: "a\rb", ok: false},
	} {
		ending, ok := LineEndingStyle(tt.text)
		if ending != tt.ending || ok != tt.ok {
			t.Fatalf("LineEndingStyle(%q) = (%q, %v), want (%q, %v)", tt.text, ending, ok, tt.ending, tt.ok)
		}
	}
	if got := NormalizeToLF("a\r\nb\rc"); got != "a\nb\nc" {
		t.Fatalf("NormalizeToLF = %q", got)
	}
	if got := RestoreLineEndings("a\nb\n", "\r\n"); got != "a\r\nb\r\n" {
		t.Fatalf("RestoreLineEndings = %q", got)
	}
}
