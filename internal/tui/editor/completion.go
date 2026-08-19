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
// only drives the menu for now.
var fakeCommands = []struct{ name, desc string }{
	{"help", "show help"},
	{"model", "choose a model"},
	{"new", "start a new conversation"},
	{"resume", "resume a conversation"},
	{"clear", "clear the transcript"},
	{"quit", "exit bits"},
}

// FakeCommands returns /-command candidates whose name is prefixed by q.
func FakeCommands(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, len(fakeCommands))
	for _, c := range fakeCommands {
		if !strings.HasPrefix(c.name, q) {
			continue
		}
		out = append(out, Candidate{Label: "/" + c.name + " — " + c.desc, Insert: "/" + c.name})
	}
	return out
}
