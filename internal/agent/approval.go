package agent

import (
	"encoding/json"
	"fmt"

	"github.com/DataDog/bits-cli/internal/assistant"
)

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

// DenyPolicy decides what the turn does after a denial is answered on the
// wire. Denial responses are always sent; only the aftermath is policy.
type DenyPolicy uint8

const (
	// DenyStop ends the turn after the wire answer. The zero value.
	DenyStop DenyPolicy = iota
	// DenyContinue lets the model adjust, keeping the typed denial.
	DenyContinue
)

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

type serverGateInput struct {
	ToolName        string         `json:"tool_name"`
	ToolArgs        map[string]any `json:"tool_args"`
	ToolCallID      string         `json:"tool_call_id"`
	ApprovalMessage string         `json:"approval_message"`
}

func parseServerGateInput(call ToolCall) (serverGateInput, error) {
	var input serverGateInput
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return serverGateInput{}, fmt.Errorf("decode approval request: %w", err)
	}
	if input.ToolName == "" {
		return serverGateInput{}, fmt.Errorf("approval request has no tool_name")
	}
	if input.ToolArgs == nil {
		return serverGateInput{}, fmt.Errorf("approval request has no tool_args object")
	}
	if input.ToolCallID == "" {
		return serverGateInput{}, fmt.Errorf("approval request has no tool_call_id")
	}
	if input.ToolCallID != call.ID {
		return serverGateInput{}, fmt.Errorf("approval request tool_call_id %q does not match outer call id %q", input.ToolCallID, call.ID)
	}
	return input, nil
}

func serverGateApprovalRequirement(call ToolCall) (ApprovalRequirement, bool) {
	input, err := parseServerGateInput(call)
	if err != nil {
		return ApprovalRequirement{}, false
	}
	title := input.ApprovalMessage
	if title == "" {
		title = fmt.Sprintf("Allow the assistant to perform the %q action?", input.ToolName)
	}
	detail := "tool: " + input.ToolName
	return ApprovalRequirement{
		Key: ApprovalKey{Tool: assistant.ApprovalRequestTool, Resource: input.ToolName},
		Prompt: ApprovalPrompt{
			Title:  title,
			Detail: detail,
		},
	}, true
}

func approvalDeniedResult(call ToolCall) ToolResult {
	if call.Name == assistant.ApprovalRequestTool {
		return serverDeniedResult()
	}
	return deniedResult()
}

func deniedResult() ToolResult {
	return ToolResult{
		Title:   "Permission denied",
		Output:  "local execution was denied by the user",
		IsError: true,
		Denied:  true,
	}
}

func serverDeniedResult() ToolResult {
	return ToolResult{
		Title:   "Permission denied",
		Output:  "the user denied this action",
		IsError: true,
		Denied:  true,
	}
}

func invalidServerGateResult() ToolResult {
	return ToolResult{
		Title:   "Invalid approval request",
		Output:  "the server approval request was invalid",
		IsError: true,
		Denied:  true,
	}
}

func approvedResult() ToolResult {
	return ToolResult{
		Title:  "Approved",
		Output: "the write was approved",
	}
}

func cancelledResult() ToolResult {
	return ToolResult{
		Title:     "Cancelled",
		Output:    "tool execution was cancelled by the user",
		IsError:   true,
		Cancelled: true,
	}
}
