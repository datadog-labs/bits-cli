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

// ApprovalMode decides how the ToolSet treats per-tool approval gates. It is
// consulted once at gate time: ModeAllowAll suppresses every declared gate,
// while ModeGated defers to each tool's ApprovalPolicy unchanged. Tool
// policies stay declarative — they say what needs approval, the mode says
// what happens.
type ApprovalMode string

const (
	// ModeAllowAll runs every tool without consulting its approval policy.
	ModeAllowAll ApprovalMode = "allow-all"
	// ModeGated consults each tool's approval policy and pauses declared gates
	// until an explicit decision.
	ModeGated ApprovalMode = "gated"
)

func (m ApprovalMode) valid() bool {
	switch m {
	case ModeAllowAll, ModeGated:
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
