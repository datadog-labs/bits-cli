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
	Title   string
	Output  string
	IsError bool
}

type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

type Tool struct {
	Definition assistant.ClientTool
	Handler    ToolHandler
}

type ToolSet struct {
	definitions []assistant.ClientTool
	handlers    map[string]ToolHandler
}

func NewToolSet(tools ...Tool) (*ToolSet, error) {
	set := &ToolSet{
		definitions: make([]assistant.ClientTool, 0, len(tools)),
		handlers:    make(map[string]ToolHandler, len(tools)),
	}
	for _, tool := range tools {
		name := tool.Definition.Name
		if name == "" {
			return nil, fmt.Errorf("tool name is empty")
		}
		if tool.Handler == nil {
			return nil, fmt.Errorf("tool %q handler is nil", name)
		}
		if _, exists := set.handlers[name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", name)
		}
		set.definitions = append(set.definitions, tool.Definition)
		set.handlers[name] = tool.Handler
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
		if handler, ok := s.handlers[call.Name]; ok {
			return handler(ctx, call)
		}
	}
	return ToolResult{
		Title:   "Unknown tool",
		Output:  "no client tool named " + call.Name + " is registered",
		IsError: true,
	}, nil
}
