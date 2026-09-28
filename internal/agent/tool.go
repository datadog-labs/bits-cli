package agent

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

type ToolCall struct {
	ID    string
	Name  string
	Input string
}

type ToolResult struct {
	Title string
	// Output is the model-visible result
	Output string
	// Display is optional additionnal data
	Display string
	IsError bool
	// Denied marks an approval refusal, not a tool failure.
	Denied bool
	// Cancelled marks a user or stop cancellation, not a tool failure.
	Cancelled bool
	// RenderState is an optional terminal update for the renderer's opaque,
	// process-local state. A nil update preserves the current state; a
	// non-nil update replaces it, and a nil State intentionally clears it.
	RenderState *RenderStateUpdate
}

type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

// ToolInputUpdate describes one folded update to a client tool's streamed
// input. RawPrefix is bounded by the transcript; FinalInput is populated only
// when HasFinalInput is true, including when the final input is empty.
type ToolInputUpdate struct {
	ToolCallID       string
	Name             string
	Delta            string
	RawPrefix        string
	PreviewTruncated bool
	FinalInput       string
	HasFinalInput    bool
}

// ToolInputReducer turns streamed client-tool input into opaque,
// process-local renderer state. Reducers must return fresh values rather than
// mutating prior, because prior may already be published in a transcript
// snapshot.
type ToolInputReducer func(context.Context, ToolInputUpdate, any) any

// RenderStateUpdate is a three-state terminal render-state operation. A nil
// *RenderStateUpdate means no update; a non-nil update with a nil State clears
// the existing state.
type RenderStateUpdate struct {
	State any
}

// ReplaceRenderState returns a terminal update replacing the current opaque
// renderer state with state.
func ReplaceRenderState(state any) *RenderStateUpdate {
	return &RenderStateUpdate{State: state}
}

// ClearRenderState returns a terminal update clearing the current opaque
// renderer state.
func ClearRenderState() *RenderStateUpdate {
	return &RenderStateUpdate{}
}

type Tool struct {
	// Resumable opts a handler into restarting an unanswered call from history.
	// Only handlers safe to restart without repeating side effects should opt in.
	Resumable    bool
	Definition   assistant.ClientTool
	Handler      ToolHandler
	Approval     ApprovalPolicy
	InputReducer ToolInputReducer
}

type ToolSet struct {
	mode        atomic.Pointer[PermissionsMode]
	definitions []assistant.ClientTool
	tools       map[string]registeredTool
}

type registeredTool struct {
	resumable    bool
	handler      ToolHandler
	approval     ApprovalPolicy
	inputReducer ToolInputReducer
}

func NewToolSet(mode PermissionsMode, tools ...Tool) (*ToolSet, error) {
	set := &ToolSet{
		definitions: make([]assistant.ClientTool, 0, len(tools)),
		tools:       make(map[string]registeredTool, len(tools)),
	}
	if err := set.SetPermissionsMode(mode); err != nil {
		return nil, err
	}
	for _, tool := range tools {
		name := tool.Definition.Name
		if name == "" {
			return nil, fmt.Errorf("tool name is empty")
		}
		if name == assistant.ApprovalRequestTool {
			return nil, fmt.Errorf("tool %q collides with the server approval gate", name)
		}
		if tool.Handler == nil {
			return nil, fmt.Errorf("tool %q handler is nil", name)
		}
		if _, exists := set.tools[name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", name)
		}
		set.definitions = append(set.definitions, tool.Definition)
		set.tools[name] = registeredTool{resumable: tool.Resumable, handler: tool.Handler, approval: tool.Approval, inputReducer: tool.InputReducer}
	}
	return set, nil
}

// ReduceInput invokes the named tool's input reducer, if one is registered.
// The returned bool reports whether a reducer exists; a reducer may
// intentionally return nil as its next state.
func (s *ToolSet) ReduceInput(ctx context.Context, update ToolInputUpdate, prior any) (any, bool) {
	if s == nil {
		return nil, false
	}
	tool, ok := s.tools[update.Name]
	if !ok || tool.inputReducer == nil {
		return nil, false
	}
	return tool.inputReducer(ctx, update, prior), true
}

// NeedsStreamedInput reports whether any registered client tool consumes
// streamed input. The engine uses this to derive the request-wide server
// capability; ordinary tools need not opt in individually on the wire.
func (s *ToolSet) NeedsStreamedInput() bool {
	if s == nil {
		return false
	}
	for _, tool := range s.tools {
		if tool.inputReducer != nil {
			return true
		}
	}
	return false
}

// NormalizeResult applies the terminal render-state safety rule for a tool.
// Reducer-backed tools clear speculative state when a terminal result omits a
// render-state update. Explicit updates, including an explicit clear, are
// preserved. Unknown and non-reducer tools are left unchanged.
func (s *ToolSet) NormalizeResult(call ToolCall, result ToolResult) ToolResult {
	if s == nil {
		return result
	}
	tool, ok := s.tools[call.Name]
	if ok && tool.inputReducer != nil && result.RenderState == nil {
		result.RenderState = ClearRenderState()
	}
	return result
}

func (s *ToolSet) Definitions() []assistant.ClientTool {
	if s == nil {
		return nil
	}
	return append([]assistant.ClientTool(nil), s.definitions...)
}

// Has reports whether name is registered as a client tool for this turn.
func (s *ToolSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.tools[name]
	return ok
}

// PermissionsMode reports the non-secret permissions mode selected for this
// tool set.
func (s *ToolSet) PermissionsMode() PermissionsMode {
	if s == nil {
		return ""
	}
	if mode := s.mode.Load(); mode != nil {
		return *mode
	}
	return ""
}

// SetPermissionsMode switches the mode for the rest of the session.
func (s *ToolSet) SetPermissionsMode(mode PermissionsMode) error {
	if !mode.valid() {
		return fmt.Errorf("invalid permissions mode %q; expected manual or skip-permissions", mode)
	}
	s.mode.Store(&mode)
	return nil
}

func (s *ToolSet) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	if s != nil {
		if tool, ok := s.tools[call.Name]; ok {
			return tool.handler(ctx, call)
		}
	}
	if call.Name == spec.AskUserQuestion {
		return ToolResult{Title: "Unsupported tool", Output: "ask_user_question is unsupported in noninteractive sessions", IsError: true}, nil
	}
	return ToolResult{
		Title:   "Unknown tool",
		Output:  "no client tool named " + call.Name + " is registered",
		IsError: true,
	}, nil
}

type gateSnapshot struct {
	set  *ToolSet
	mode PermissionsMode
}

func (s *ToolSet) snapshotPermissions() gateSnapshot {
	if s == nil {
		return gateSnapshot{}
	}
	return gateSnapshot{set: s, mode: s.PermissionsMode()}
}

// ApprovesServerGate reports whether the server write gate is approved without
// a local decision. Manual mode routes the gate through Approval instead.
func (s *ToolSet) ApprovesServerGate() bool {
	return s.snapshotPermissions().approvesServerGate()
}

func (g gateSnapshot) approvesServerGate() bool {
	return g.set != nil && g.mode == ModeSkipPermissions
}

// Approval reports whether call must wait for an approval decision before
// it runs. The set's PermissionsMode is consulted first: ModeSkipPermissions
// suppresses every declared gate, while ModeManual exposes the server gate and
// defers to each registered tool's ApprovalPolicy.
func (s *ToolSet) Approval(call ToolCall) (ApprovalRequirement, bool) {
	return s.snapshotPermissions().approval(call)
}

func (g gateSnapshot) approval(call ToolCall) (ApprovalRequirement, bool) {
	if g.set == nil || g.mode != ModeManual {
		return ApprovalRequirement{}, false
	}
	if call.Name == assistant.ApprovalRequestTool {
		return serverGateApprovalRequirement(call)
	}
	tool, ok := g.set.tools[call.Name]
	if !ok || tool.approval == nil {
		return ApprovalRequirement{}, false
	}
	return tool.approval(call)
}
