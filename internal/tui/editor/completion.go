package editor

import "strings"

const maxLocalCandidates = 5

// fakeFiles preserves the original local-file completion behavior until
// BCLI-59 adds workspace discovery.
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

// CandidateKind is open so later completion sources do not require changing
// editor navigation or rendering.
type CandidateKind string

const (
	CandidateFile    CandidateKind = "file"
	CandidateEntity  CandidateKind = "datadog_entity"
	CandidateCommand CandidateKind = "command"
)

// Attachment preserves the canonical identity selected from remote search.
// Candidate and search-flow IDs are retained for a future approved feedback
// path; prompt text is never parsed to recreate these fields.
type Attachment struct {
	Type               string
	ID                 string
	Label              string
	CandidateID        string
	SearchFlowID       string
	Rank               int
	RankScore          *float64
	TrackingAttributes map[string]string
}

// Candidate is one completion row. Kind and ID are stable source identities;
// Label and Detail are presentation; Insert edits the prompt. Attachment is
// non-nil only when selection should add structured turn context.
type Candidate struct {
	Kind       CandidateKind
	ID         string
	Label      string
	Detail     string
	Insert     string
	Attachment *Attachment
}

// RemoteState describes the non-blocking Datadog half of an @ menu.
type RemoteState int

const (
	RemoteIdle RemoteState = iota
	RemoteLoading
	RemoteReady
	RemoteError
)

// FileCandidates returns hardcoded local paths containing q,
// case-insensitively. BCLI-59 will replace this provider with workspace files.
func FileCandidates(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, min(maxLocalCandidates, len(fakeFiles)))
	for _, path := range fakeFiles {
		if q != "" && !strings.Contains(strings.ToLower(path), q) {
			continue
		}
		out = append(out, Candidate{
			Kind: CandidateFile, ID: path, Label: "+ " + path,
			Insert: "@" + path,
		})
		if len(out) == maxLocalCandidates {
			break
		}
	}
	return out
}

var fakeCommands = []struct {
	name    string
	aliases []string
	desc    string
}{
	{"help", nil, "show help"},
	{"model", nil, "choose a model"},
	{"new", []string{"clear"}, "start a new conversation"},
	{"resume", nil, "resume a conversation"},
	{"status", nil, "show session status"},
	{"web", nil, "open this conversation in Datadog"},
	{"logout", nil, "sign out from your Datadog account"},
	{"quit", []string{"exit"}, "exit bits"},
}

// FakeCommands returns slash-command candidates whose canonical name or alias
// starts with q.
func FakeCommands(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, len(fakeCommands))
	for _, command := range fakeCommands {
		if !commandMatches(command.name, command.aliases, q) {
			continue
		}
		out = append(out, Candidate{
			Kind: CandidateCommand, ID: command.name,
			Label: "/" + command.name, Detail: command.desc,
			Insert: "/" + command.name,
		})
	}
	return out
}

func commandMatches(name string, aliases []string, q string) bool {
	if strings.HasPrefix(name, q) {
		return true
	}
	for _, alias := range aliases {
		if strings.HasPrefix(alias, q) {
			return true
		}
	}
	return false
}
