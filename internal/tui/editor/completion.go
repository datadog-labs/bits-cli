package editor

import "strings"

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

// FileState describes the non-blocking local half of an @ menu.
type FileState int

const (
	FileIdle FileState = iota
	FileIndexing
	FileReady
	FileError
)

var commands = []struct {
	name    string
	aliases []string
	desc    string
}{
	{"help", nil, "show help"},
	{"new", []string{"clear"}, "start a new conversation"},
	{"resume", nil, "resume a conversation"},
	{"settings", nil, "open assistant settings"},
	{"status", nil, "show session status"},
	{"permissions", nil, "show or switch the permissions mode"},
	{"copy", nil, "copy the latest assistant response"},
	{"web", nil, "open this conversation in Datadog"},
	{"logout", nil, "sign out from your Datadog account"},
	{"quit", []string{"exit"}, "exit bits"},
}

// CommandCandidates returns slash-command candidates whose canonical name or alias
// starts with q.
func CommandCandidates(q string) []Candidate {
	q = strings.ToLower(q)
	out := make([]Candidate, 0, len(commands))
	for _, command := range commands {
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
