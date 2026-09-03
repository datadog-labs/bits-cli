// Package filediff provides the pure, filesystem-independent semantics shared
// by editor diffs and the edit_file handler.
package filediff

import (
	"sort"
	"strings"
)

// Edit is one exact replacement requested by edit_file.
type Edit struct {
	OldText string  `json:"old_text"`
	NewText *string `json:"new_text"`
}

// Replacement is one resolved, non-overlapping replacement in normalized text.
// Start and Length are byte offsets, matching Go string slicing semantics.
type Replacement struct {
	Start   int
	Length  int
	NewText string
}

// MatchErrorKind identifies why an edit cannot be resolved against a snapshot.
type MatchErrorKind uint8

const (
	MatchEmptyOldText MatchErrorKind = iota + 1
	MatchMissingNewText
	MatchNotFound
	MatchAmbiguous
	MatchOverlap
)

// MatchError reports an invalid edit without coupling pure matching semantics
// to a tool result or a workspace path. Index and Total are meaningful for all
// errors except MatchOverlap; Count is meaningful for MatchAmbiguous.
type MatchError struct {
	Kind  MatchErrorKind
	Index int
	Total int
	Count int
}

func (e *MatchError) Error() string {
	switch e.Kind {
	case MatchEmptyOldText:
		return "old_text must not be empty"
	case MatchMissingNewText:
		return "new_text is required"
	case MatchNotFound:
		return "old_text was not found; it must match the file exactly, including whitespace and newlines"
	case MatchAmbiguous:
		return "old_text matched multiple regions; add surrounding context so it matches exactly one"
	case MatchOverlap:
		return "edits overlap; each edit must target a disjoint region of the original file"
	default:
		return "invalid edit"
	}
}

// MatchEdits resolves every edit against one snapshot of base. It returns
// replacements sorted by position, or a typed error for the first empty,
// missing, ambiguous, or overlapping edit. No edit is applied unless all
// targets are unique and disjoint.
func MatchEdits(base string, edits []Edit) ([]Replacement, error) {
	repls := make([]Replacement, 0, len(edits))
	for i, edit := range edits {
		oldText := NormalizeToLF(edit.OldText)
		if oldText == "" {
			return nil, &MatchError{Kind: MatchEmptyOldText, Index: i, Total: len(edits)}
		}
		if edit.NewText == nil {
			return nil, &MatchError{Kind: MatchMissingNewText, Index: i, Total: len(edits)}
		}
		count := countMatches(base, oldText)
		if count == 0 {
			return nil, &MatchError{Kind: MatchNotFound, Index: i, Total: len(edits)}
		}
		if count > 1 {
			return nil, &MatchError{Kind: MatchAmbiguous, Index: i, Total: len(edits), Count: count}
		}
		repls = append(repls, Replacement{
			Start:   strings.Index(base, oldText),
			Length:  len(oldText),
			NewText: NormalizeToLF(*edit.NewText),
		})
	}

	sort.Slice(repls, func(a, b int) bool { return repls[a].Start < repls[b].Start })
	for i := 1; i < len(repls); i++ {
		if repls[i-1].Start+repls[i-1].Length > repls[i].Start {
			return nil, &MatchError{Kind: MatchOverlap}
		}
	}
	return repls, nil
}

// ApplyReplacements rewrites base by copying the gaps between sorted,
// non-overlapping replacements and substituting each matched region.
func ApplyReplacements(base string, replacements []Replacement) string {
	var b strings.Builder
	prev := 0
	for _, replacement := range replacements {
		b.WriteString(base[prev:replacement.Start])
		b.WriteString(replacement.NewText)
		prev = replacement.Start + replacement.Length
	}
	b.WriteString(base[prev:])
	return b.String()
}

// LineEndingStyle reports the file's one supported line-ending convention. It
// returns ok=false for mixed CRLF/LF or any bare CR rather than silently
// normalizing untouched content during a write.
func LineEndingStyle(text string) (ending string, ok bool) {
	crlf := strings.Count(text, "\r\n")
	bareLF := strings.Count(text, "\n") - crlf
	bareCR := strings.Count(text, "\r") - crlf
	if bareCR > 0 || (crlf > 0 && bareLF > 0) {
		return "", false
	}
	if crlf > 0 {
		return "\r\n", true
	}
	return "\n", true
}

// NormalizeToLF normalizes input for exact edit matching and diff rendering.
func NormalizeToLF(text string) string {
	if !strings.ContainsRune(text, '\r') {
		return text
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// RestoreLineEndings restores a normalized text value to the supplied style.
func RestoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// countMatches counts overlapping occurrences, so a self-overlapping old_text
// is never mistakenly treated as unique.
func countMatches(text, sub string) int {
	n := 0
	for i := 0; ; {
		j := strings.Index(text[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + 1
	}
}
