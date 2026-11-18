// Package spec defines importable wire contracts for built-in tools and shared
// tool payloads. It deliberately has no execution or TUI dependencies.
package spec

const (
	ReadFile    = "read_file"
	ListFiles   = "list_files"
	GrepFiles   = "grep_files"
	WriteFile   = "write_file"
	EditFile    = "edit_file"
	ExecCommand = "exec_command"
	WaitCommand = "wait_command"
	StopCommand = "stop_command"

	// Remote tools.
	Skill               = "Skill"
	StartBackgroundTask = "startBackgroundTask"

	// ExecDefaultYieldMS is how long exec_command waits before answering
	// with a running command. ExecMaxWaitMS bounds every such wait to half
	// of the 600 s deadline the backend gives a whole multi-agent run.
	ExecDefaultYieldMS int64 = 10_000
	ExecDefaultWaitMS  int64 = 30_000
	ExecMaxWaitMS      int64 = 5 * 60 * 1_000
)

// Identity is the complete identity needed for exact tool classification.
type Identity struct {
	ClientSide bool
	Namespace  string
	Name       string
}

var (
	ServerSkill            = Identity{Name: Skill}
	ServerSpawnAgent       = Identity{Name: "spawn_agent"}
	ServerSendAgentMessage = Identity{Name: "send_agent_message"}
	ServerWaitForAgents    = Identity{Name: "wait_for_agents"}
	// ServerStartBackgroundTask starts server work that reports into the
	// conversation after the turn; its result names the task.
	ServerStartBackgroundTask = Identity{Name: StartBackgroundTask}
	ClientReadFile            = Identity{ClientSide: true, Name: ReadFile}
	ClientListFiles           = Identity{ClientSide: true, Name: ListFiles}
	ClientGrepFiles           = Identity{ClientSide: true, Name: GrepFiles}
	ClientWriteFile           = Identity{ClientSide: true, Name: WriteFile}
	ClientEditFile            = Identity{ClientSide: true, Name: EditFile}
	ClientExecCommand         = Identity{ClientSide: true, Name: ExecCommand}
	ClientWaitCommand         = Identity{ClientSide: true, Name: WaitCommand}
	ClientStopCommand         = Identity{ClientSide: true, Name: StopCommand}
)

// PathInput is the minimal path-bearing projection shared by local tools. It
// lets presentation code decode a path without retaining unrelated input such
// as write content or edit operations.
type PathInput struct {
	Path string `json:"path"`
}

// SkillInput is the request payload for the server-side Skill tool. It lives
// here so presentation code and tool integrations share the same wire shape.
type SkillInput struct {
	SkillName string `json:"skill_name"`
}

// StartBackgroundTaskInput is the task a startBackgroundTask call asks to
// start. Payload holds the fields an investigation may carry; times are Unix
// milliseconds.
type StartBackgroundTaskInput struct {
	Task struct {
		AgentID  string `json:"agent_id"`
		TaskName string `json:"task_name"`
		Payload  struct {
			Description      string   `json:"description"`
			Tags             []string `json:"tags"`
			MonitorID        *int64   `json:"monitor_id"`
			EventID          string   `json:"event_id"`
			EventTS          *int64   `json:"event_ts"`
			IncidentPublicID *int64   `json:"incident_public_id"`
			StartTime        *int64   `json:"start_time"`
			EndTime          *int64   `json:"end_time"`
		} `json:"payload"`
	} `json:"task"`
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
	Shell     string `json:"shell"`
	Workdir   string `json:"workdir"`
	TimeoutMS *int64 `json:"timeout_ms"`
	YieldMS   *int64 `json:"yield_ms"`
}

// WaitCommandInput waits on a command exec_command left running.
type WaitCommandInput struct {
	CommandID string `json:"command_id"`
	WaitMS    *int64 `json:"wait_ms"`
}

// StopCommandInput stops a command exec_command left running.
type StopCommandInput struct {
	CommandID string `json:"command_id"`
}

// ExecStatus identifies whether a command still runs, or why it stopped.
// Output truncation is orthogonal to it and is reported separately.
type ExecStatus string

const (
	ExecSucceeded    ExecStatus = "success"
	ExecNonZeroExit  ExecStatus = "nonzero_exit"
	ExecLaunchFailed ExecStatus = "launch_failure"
	ExecTimedOut     ExecStatus = "timeout"
	ExecCancelled    ExecStatus = "cancellation"
	ExecRunning      ExecStatus = "running"
)

// ExecCommandOutput is the JSON result of exec_command, wait_command, and
// stop_command. Status is always set. CommandID names the command
// exec_command left running; wait_command and stop_command results name it by
// Command instead, for the model and the transcript, which know only the id.
// ExitCode and Signal are populated only when the operating system reported
// them. Streams hold output since the previous result for the same command,
// and the omitted-byte counts are non-zero exactly when Truncated is true.
type ExecCommandOutput struct {
	Status             ExecStatus `json:"status"`
	CommandID          string     `json:"command_id,omitempty"`
	Command            string     `json:"command,omitempty"`
	ExitCode           *int       `json:"exit_code,omitempty"`
	Signal             string     `json:"signal,omitempty"`
	DurationMS         int64      `json:"duration_ms"`
	Truncated          bool       `json:"truncated"`
	OutputIncomplete   bool       `json:"output_incomplete"`
	StdoutOmittedBytes int64      `json:"stdout_omitted_bytes"`
	StderrOmittedBytes int64      `json:"stderr_omitted_bytes"`
	Error              string     `json:"error,omitempty"`
	Stdout             string     `json:"stdout"`
	Stderr             string     `json:"stderr"`
}
