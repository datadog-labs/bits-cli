package spec

// Tools the server defines and runs. The client only presents them or acts on
// their results, so each payload keeps just the fields the client reads.

const (
	Skill               = "Skill"
	StartBackgroundTask = "startBackgroundTask"
	// ApprovalRequest is the client call the server injects to gate a backend
	// write. It is never advertised in client_tools; the client answers it
	// from its own approval policy.
	ApprovalRequest = "approval_request"
)

var (
	ServerSkill            = Identity{Name: Skill}
	ServerSpawnAgent       = Identity{Name: "spawn_agent"}
	ServerSendAgentMessage = Identity{Name: "send_agent_message"}
	ServerWaitForAgents    = Identity{Name: "wait_for_agents"}
	// ServerStartBackgroundTask starts server work that reports into the
	// conversation after the turn; its result names the task.
	ServerStartBackgroundTask = Identity{Name: StartBackgroundTask}
)

// ApprovalRequestInput names the server call an approval_request asks to
// approve.
type ApprovalRequestInput struct {
	ToolName        string         `json:"tool_name"`
	ToolArgs        map[string]any `json:"tool_args"`
	ToolCallID      string         `json:"tool_call_id"`
	ApprovalMessage string         `json:"approval_message"`
}

type SkillInput struct {
	SkillName string `json:"skill_name"`
}

type SpawnAgentInput struct {
	Prompt string `json:"prompt"`
}

type SendAgentMessageInput struct {
	Message string `json:"message"`
}

// WaitForAgentsInput lists the subagents a call waits for. Mode is "any" when
// the first of them to finish ends the wait.
type WaitForAgentsInput struct {
	AgentIDs []string `json:"agent_ids"`
	Mode     string   `json:"mode"`
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

type StartBackgroundTaskOutput struct {
	TaskID string `json:"task_id"`
}
