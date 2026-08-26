package editor

import "strings"

// maxCandidates caps how many completions the fake providers return so the menu
// stays a bounded height.
const maxCandidates = 8

// Completer returns candidates for the active word — the token being typed,
// including its "@"/"/" trigger. It returns nil when nothing applies, which
// keeps the menu closed.
type Completer func(word string) []Candidate

// Candidate is one completion. Label is shown in the menu; Insert replaces the
// active word when the candidate is accepted.
type Candidate struct {
	Label  string
	Insert string
}

// Dispatch routes the active word to a provider by its trigger character. Words
// without an "@"/"/" trigger return nil (menu stays closed).
func Dispatch(word string) []Candidate {
	switch {
	case strings.HasPrefix(word, "@"):
		return FakeFiles(word[1:])
	case strings.HasPrefix(word, "/"):
		return FakeCommands(word[1:])
	default:
		return nil
	}
}

// fakeFiles is a hardcoded path list standing in for a real file index. Phase 6
// ships fake data; a real index lands with client tools.
var fakeFiles = []string{
	"README.md",
	"go.mod",
	"main.go",
	"internal/agent/engine.go",
	"internal/agent/classify.go",
	"internal/assistant/client.go",
	"internal/assistant/types.go",
	"internal/tui/model.go",
	"internal/tui/update.go",
	"internal/tui/chat/transcript.go",
	"internal/tui/chat/render.go",
	"internal/tui/editor/editor.go",
}

// FakeFiles returns @-file candidates whose path contains q (case-insensitive).
func FakeFiles(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, maxCandidates)
	for _, p := range fakeFiles {
		if q != "" && !strings.Contains(strings.ToLower(p), q) {
			continue
		}
		out = append(out, Candidate{Label: p, Insert: "@" + p})
		if len(out) == maxCandidates {
			break
		}
	}
	return out
}

// fakeCommands is the placeholder slash-command set. Execution is deferred; this
// only drives the menu for now. Each entry may carry aliases; a query matching
// any alias surfaces the command under its canonical name, so both spellings
// are discoverable and aliases normalize to canonical on accept.
var fakeCommands = []struct {
	name    string
	aliases []string
	desc    string
}{
	{"help", nil, "show help"},
	{"model", nil, "choose a model"},
	{"new", []string{"clear"}, "start a new conversation"},
	{"resume", nil, "resume a conversation"},
	{"quit", []string{"exit"}, "exit bits"},
}

// FakeCommands returns /-command candidates whose canonical name or any alias
// is prefixed by q. Accepting a candidate inserts the canonical spelling, so
// aliases normalize on accept; the label annotates aliases for discoverability.
func FakeCommands(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, len(fakeCommands))
	for _, c := range fakeCommands {
		if !commandMatches(c.name, c.aliases, q) {
			continue
		}
		label := "/" + c.name + " — " + c.desc
		out = append(out, Candidate{Label: label, Insert: "/" + c.name})
	}
	return out
}

// commandMatches reports whether q prefixes the canonical name or any alias.
func commandMatches(name string, aliases []string, q string) bool {
	if strings.HasPrefix(name, q) {
		return true
	}
	for _, a := range aliases {
		if strings.HasPrefix(a, q) {
			return true
		}
	}
	return false
}
