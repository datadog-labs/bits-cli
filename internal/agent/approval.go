package agent

import (
	"encoding/json"
	"fmt"
	"sync"

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

// PermissionsMode decides how the ToolSet treats per-tool permission gates.
// It is consulted once at gate time: ModeSkipPermissions suppresses every
// declared gate, while ModeManual defers to each tool's ApprovalPolicy
// unchanged. Tool policies stay declarative — they say what needs approval, the
// mode says what happens.
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

// permissionGate orders permission changes against gate decisions. Its lock
// covers the mode store, the session grants, the key of each parked call (so
// a grant can name the call), and the start of every gated call: a change
// lands wholly before or after a decision and the start it leads to. It is
// never held across a channel send or an approval policy, so neither the UI
// nor the engine goroutine ever waits on the other through it.
type permissionGate struct {
	mu     sync.Mutex
	grants map[ApprovalKey]struct{}
	parked map[string]ApprovalKey // by call ID
}

// verdict is how a gated call proceeds.
type verdict uint8

const (
	verdictPark verdict = iota // wait for an answer
	verdictRun                 // started under the gate lock
	verdictDeny                // the mode denies it
)

// admit decides how the gated call parked or dispatched as callID proceeds,
// and calls start before releasing the lock when it may run. Deny mode wins
// over grants and answers; skip-permissions runs every call; manual runs a
// call answered allowOnce or covered by a grant, and parks the rest. start
// must not block.
func (g *permissionGate) admit(tools *ToolSet, callID string, key ApprovalKey, allowOnce bool, start func()) verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch tools.PermissionsMode() {
	case ModeDeny:
		delete(g.parked, callID)
		return verdictDeny
	case ModeSkipPermissions:
	default:
		if _, granted := g.grants[key]; !granted && !allowOnce {
			if g.parked == nil {
				g.parked = make(map[string]ApprovalKey)
			}
			g.parked[callID] = key
			return verdictPark
		}
	}
	delete(g.parked, callID)
	start()
	return verdictRun
}

// setMode stores mode, revoking every grant when it is manual.
func (g *permissionGate) setMode(tools *ToolSet, mode PermissionsMode) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := tools.setPermissionsMode(mode); err != nil {
		return err
	}
	if mode == ModeManual {
		clear(g.grants)
	}
	return nil
}

// grantParked grants the key of the call parked as callID, if any.
func (g *permissionGate) grantParked(callID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key, ok := g.parked[callID]
	if !ok {
		return false
	}
	if g.grants == nil {
		g.grants = make(map[ApprovalKey]struct{})
	}
	g.grants[key] = struct{}{}
	return true
}

func (g *permissionGate) unpark(callID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.parked, callID)
}

// clearGrants revokes every grant; calls stay parked.
func (g *permissionGate) clearGrants() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.grants)
}
