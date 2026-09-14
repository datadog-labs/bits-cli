// Package spec defines the importable wire contract for built-in local tools.
// It deliberately has no execution or TUI dependencies.
package spec

const (
	ReadFile    = "read_file"
	ListFiles   = "list_files"
	GrepFiles   = "grep_files"
	WriteFile   = "write_file"
	EditFile    = "edit_file"
	ExecCommand = "exec_command"

	ExecDefaultTimeoutMS int64 = 10_000
	ExecMaxTimeoutMS     int64 = 10 * 60 * 1_000
)

// Identity is the complete identity needed for exact tool classification.
type Identity struct {
	ClientSide bool
	Namespace  string
	Name       string
}

var (
	ClientReadFile    = Identity{ClientSide: true, Name: ReadFile}
	ClientListFiles   = Identity{ClientSide: true, Name: ListFiles}
	ClientGrepFiles   = Identity{ClientSide: true, Name: GrepFiles}
	ClientWriteFile   = Identity{ClientSide: true, Name: WriteFile}
	ClientEditFile    = Identity{ClientSide: true, Name: EditFile}
	ClientExecCommand = Identity{ClientSide: true, Name: ExecCommand}
)

// PathInput is the minimal path-bearing projection shared by local tools. It
// lets presentation code decode a path without retaining unrelated input such
// as write content or edit operations.
type PathInput struct {
	Path string `json:"path"`
}

type ReadFileInput struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type ListFilesInput struct {
	Path  string `json:"path"`
	Depth *int   `json:"depth"`
}

type GrepFilesInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	Include       string `json:"include"`
	CaseSensitive bool   `json:"case_sensitive"`
	Offset        int    `json:"offset"`
}

type WriteFileInput struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

type EditFileInput struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"`
}

type Edit struct {
	OldText string  `json:"old_text"`
	NewText *string `json:"new_text"`
}

type ExecCommandInput struct {
	Cmd       string `json:"cmd"`
	Workdir   string `json:"workdir"`
	TimeoutMS *int64 `json:"timeout_ms"`
}

// ExecTerminalReason identifies why a one-shot command stopped. Output
// truncation is orthogonal to this reason and is reported separately.
type ExecTerminalReason string

const (
	ExecSucceeded    ExecTerminalReason = "success"
	ExecNonZeroExit  ExecTerminalReason = "nonzero_exit"
	ExecLaunchFailed ExecTerminalReason = "launch_failure"
	ExecTimedOut     ExecTerminalReason = "timeout"
	ExecCancelled    ExecTerminalReason = "cancellation"
)

// ExecCommandOutput is the JSON result an exec_command tool call returns. Fields
// mirror the terminal outcome of the execution: Status is always set, ExitCode
// and Signal are populated only when the operating system reported them, and
// the omitted-byte counts are non-zero exactly when Truncated is true.
type ExecCommandOutput struct {
	Status             ExecTerminalReason `json:"status"`
	ExitCode           *int               `json:"exit_code,omitempty"`
	Signal             string             `json:"signal,omitempty"`
	DurationMS         int64              `json:"duration_ms"`
	Truncated          bool               `json:"truncated"`
	OutputIncomplete   bool               `json:"output_incomplete"`
	StdoutOmittedBytes int64              `json:"stdout_omitted_bytes"`
	StderrOmittedBytes int64              `json:"stderr_omitted_bytes"`
	Error              string             `json:"error,omitempty"`
	Stdout             string             `json:"stdout"`
	Stderr             string             `json:"stderr"`
}
