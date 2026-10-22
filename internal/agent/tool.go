package agent

import (
	"context"
	"fmt"

	"github.com/datadog-labs/bits-cli/internal/assistant"
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
	Resumable bool
	// ContextOnResume sends local environment and skills context with the
	// first response to a restored call. Use for cross-surface handoffs.
	ContextOnResume bool
	Definition      assistant.ClientTool
	Handler         ToolHandler
	Approval        ApprovalPolicy
	InputReducer    ToolInputReducer
	// Hidden registers the handler without advertising the tool to the model.
	Hidden bool
}

// ToolSet is an immutable registry of client tools. Tools declare their
// approval policy; the engine's permissions mode decides what it means.
type ToolSet struct {
	definitions []assistant.ClientTool
	tools       map[string]registeredTool
}

type registeredTool struct {
	resumable       bool
	contextOnResume bool
	handler         ToolHandler
	approval        ApprovalPolicy
	inputReducer    ToolInputReducer
}

func NewToolSet(tools ...Tool) (*ToolSet, error) {
	set := &ToolSet{
		definitions: make([]assistant.ClientTool, 0, len(tools)),
		tools:       make(map[string]registeredTool, len(tools)),
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
		definition := tool.Definition
		// Reducers consume provisional input; opt in only their owning tool.
		definition.StreamInput = definition.StreamInput || tool.InputReducer != nil
		if !tool.Hidden {
			set.definitions = append(set.definitions, definition)
		}
		set.tools[name] = registeredTool{resumable: tool.Resumable, contextOnResume: tool.ContextOnResume, handler: tool.Handler, approval: tool.Approval, inputReducer: tool.InputReducer}
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

func (s *ToolSet) needsContextOnResume(calls []ToolCall) bool {
	if s == nil {
		return false
	}
	for _, call := range calls {
		if s.tools[call.Name].contextOnResume {
			return true
		}
	}
	return false
}

func (s *ToolSet) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	if s != nil {
		if tool, ok := s.tools[call.Name]; ok {
			return tool.handler(ctx, call)
		}
	}
	return ToolResult{
		Title:   "Unknown tool",
		Output:  "no client tool named " + call.Name + " is registered",
		IsError: true,
	}, nil
}

// Approval reports the gate call declares, whatever the mode: the server
// gate's, or its tool's ApprovalPolicy.
func (s *ToolSet) Approval(call ToolCall) (ApprovalRequirement, bool) {
	if s == nil {
		return ApprovalRequirement{}, false
	}
	if call.Name == assistant.ApprovalRequestTool {
		return serverGateApprovalRequirement(call)
	}
	tool, ok := s.tools[call.Name]
	if !ok || tool.approval == nil {
		return ApprovalRequirement{}, false
	}
	return tool.approval(call)
}
