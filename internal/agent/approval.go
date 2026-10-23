package agent

import (
	"encoding/json"
	"fmt"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type ApprovalDecision string

const (
	ApprovalDeny      ApprovalDecision = "deny"
	ApprovalAllowOnce ApprovalDecision = "allow_once"
)

func (d ApprovalDecision) valid() bool {
	switch d {
	case ApprovalDeny, ApprovalAllowOnce:
		return true
	default:
		return false
	}
}

// PermissionsMode decides what the engine does with a tool's declared approval
// gate. ModeSkipPermissions runs gated calls, ModeDeny rejects them, and
// ModeManual waits for approval unless a session grant applies. The tool's
// ApprovalPolicy only declares whether a gate is needed.
type PermissionsMode string

const (
	// ModeManual consults each tool's approval policy and pauses declared gates
	// until an explicit decision.
	ModeManual PermissionsMode = "manual"
	// ModeSkipPermissions runs every tool without asking, whatever its gate.
	ModeSkipPermissions PermissionsMode = "skip-permissions"
	// ModeDeny denies every declared gate without asking.
	ModeDeny PermissionsMode = "deny"
)

func (m PermissionsMode) valid() bool {
	switch m {
	case ModeManual, ModeSkipPermissions, ModeDeny:
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
	// Gated is the server call an approval_request asks to approve, which a
	// surface can present instead of the request itself; nil for a call that
	// is approved for itself.
	Gated *ToolCall
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
	args, err := json.Marshal(input.ToolArgs)
	if err != nil {
		return ApprovalRequirement{}, false
	}
	return ApprovalRequirement{
		Key: ApprovalKey{Tool: assistant.ApprovalRequestTool, Resource: input.ToolName},
		Prompt: ApprovalPrompt{
			Title:  title,
			Detail: detail,
			Gated:  &ToolCall{ID: input.ToolCallID, Name: input.ToolName, Input: string(args)},
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

func modeDeniedResult() ToolResult {
	return ToolResult{
		Title:   "Permission denied",
		Output:  "this action was denied by the run's permissions mode",
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

// permissionGate is the actor's permission state: the mode and the session
// grants. The approval policy only declares a call's gate; the mode decides
// what it means.
type permissionGate struct {
	mode   PermissionsMode
	grants map[ApprovalKey]struct{}
}

// verdict is how a gated call proceeds.
type verdict uint8

const (
	verdictPark verdict = iota // wait for an answer
	verdictRun
	verdictDeny // the mode denies it
)

// admit decides how a gated call with key proceeds. Deny mode wins over
// grants and answers; skip-permissions runs every call; manual runs a call
// answered allowOnce or covered by a grant, and parks the rest.
func (g *permissionGate) admit(key ApprovalKey, allowOnce bool) verdict {
	switch g.mode {
	case ModeDeny:
		return verdictDeny
	case ModeSkipPermissions:
		return verdictRun
	case ModeManual:
	}
	if _, granted := g.grants[key]; granted || allowOnce {
		return verdictRun
	}
	return verdictPark
}

// setMode stores mode, revoking every grant when it is manual.
func (g *permissionGate) setMode(mode PermissionsMode) error {
	if !mode.valid() {
		return fmt.Errorf("invalid permissions mode %q; expected manual, skip-permissions, or deny", mode)
	}
	g.mode = mode
	if mode == ModeManual {
		clear(g.grants)
	}
	return nil
}

func (g *permissionGate) grant(key ApprovalKey) {
	if g.grants == nil {
		g.grants = make(map[ApprovalKey]struct{})
	}
	g.grants[key] = struct{}{}
}
