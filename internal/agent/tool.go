package agent

import (
	"context"
	"fmt"

	"github.com/DataDog/bits-cli/internal/assistant"
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
}

type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

type Tool struct {
	Definition assistant.ClientTool
	Handler    ToolHandler
	Approval   ApprovalPolicy
}

type ToolSet struct {
	mode        ApprovalMode
	definitions []assistant.ClientTool
	tools       map[string]registeredTool
}

type registeredTool struct {
	handler  ToolHandler
	approval ApprovalPolicy
}

func NewToolSet(mode ApprovalMode, tools ...Tool) (*ToolSet, error) {
	if !mode.valid() {
		return nil, fmt.Errorf("invalid approval mode %q; expected allow-all or gated", mode)
	}
	set := &ToolSet{
		mode:        mode,
		definitions: make([]assistant.ClientTool, 0, len(tools)),
		tools:       make(map[string]registeredTool, len(tools)),
	}
	for _, tool := range tools {
		name := tool.Definition.Name
		if name == "" {
			return nil, fmt.Errorf("tool name is empty")
		}
		if tool.Handler == nil {
			return nil, fmt.Errorf("tool %q handler is nil", name)
		}
		if _, exists := set.tools[name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", name)
		}
		set.definitions = append(set.definitions, tool.Definition)
		set.tools[name] = registeredTool{handler: tool.Handler, approval: tool.Approval}
	}
	return set, nil
}

func (s *ToolSet) Definitions() []assistant.ClientTool {
	if s == nil {
		return nil
	}
	return append([]assistant.ClientTool(nil), s.definitions...)
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

// Approval reports whether call must wait for an approval decision before
// it runs. The set's ApprovalMode is consulted first: ModeAllowAll suppresses
// every declared gate, ModeGated defers to the tool's ApprovalPolicy.
func (s *ToolSet) Approval(call ToolCall) (ApprovalRequirement, bool) {
	if s == nil || s.mode != ModeGated {
		return ApprovalRequirement{}, false
	}
	tool, ok := s.tools[call.Name]
	if !ok || tool.approval == nil {
		return ApprovalRequirement{}, false
	}
	return tool.approval(call)
}
