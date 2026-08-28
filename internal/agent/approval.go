package agent

type ApprovalDecision string

const (
	ApprovalDeny         ApprovalDecision = "deny"
	ApprovalAllowOnce    ApprovalDecision = "allow_once"
	ApprovalAllowSession ApprovalDecision = "allow_session"
)

func (d ApprovalDecision) valid() bool {
	switch d {
	case ApprovalDeny, ApprovalAllowOnce, ApprovalAllowSession:
		return true
	default:
		return false
	}
}

type ApprovalKey struct {
	Tool     string
	Resource string
}

type ApprovalPrompt struct {
	Title  string
	Detail string
}

type ApprovalRequirement struct {
	Key    ApprovalKey
	Prompt ApprovalPrompt
}

type ApprovalPolicy func(ToolCall) (ApprovalRequirement, bool)

func deniedResult() ToolResult {
	return ToolResult{
		Title:   "Permission denied",
		Output:  "local execution was denied by the user",
		IsError: true,
	}
}

func cancelledResult() ToolResult {
	return ToolResult{
		Title:   "Cancelled",
		Output:  "tool execution was cancelled by the user",
		IsError: true,
	}
}
