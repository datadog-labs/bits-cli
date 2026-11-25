package acp

import (
	"cmp"
	"context"
	"slices"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

const (
	configPermissions acp.SessionConfigId = "permissions"
	configReasoning   acp.SessionConfigId = "reasoning"
)

// selectOption is one select config option: its values, and how to read and
// set it on the engine.
type selectOption struct {
	id       acp.SessionConfigId
	name     string
	category acp.SessionConfigOptionCategory
	values   acp.SessionConfigSelectOptionsUngrouped
	get      func(*agent.Engine) string
	set      func(*agent.Engine, string) error
}

// NOTE: no model option: the backend lists no models to choose from.
var selectOptions = []selectOption{
	{
		id: configPermissions, name: "Permissions", category: acp.SessionConfigOptionCategoryMode,
		values: acp.SessionConfigSelectOptionsUngrouped{
			{Value: acp.SessionConfigValueId(agent.ModeManual), Name: "Ask"},
			{Value: acp.SessionConfigValueId(agent.ModeSkipPermissions), Name: "Full access"},
			{Value: acp.SessionConfigValueId(agent.ModeDeny), Name: "Deny"},
		},
		get: func(e *agent.Engine) string { return string(e.PermissionsMode()) },
		set: func(e *agent.Engine, v string) error { return e.SetPermissionsMode(agent.PermissionsMode(v)) },
	},
	{
		id: configReasoning, name: "Reasoning", category: acp.SessionConfigOptionCategoryThoughtLevel,
		values: acp.SessionConfigSelectOptionsUngrouped{
			{Value: "default", Name: "Default"}, {Value: "fast", Name: "Fast"}, {Value: "deep", Name: "Deep"},
		},
		get: func(e *agent.Engine) string { return cmp.Or(e.InferenceMode(), "default") },
		set: func(e *agent.Engine, v string) error {
			if v == "default" {
				v = ""
			}
			return e.SetInferenceMode(v)
		},
	},
}

func configOptions(e *agent.Engine) []acp.SessionConfigOption {
	var out []acp.SessionConfigOption
	for _, o := range selectOptions {
		values := o.values
		out = append(out, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: o.id, Name: o.name, Category: acp.Ptr(o.category), Type: "select",
			CurrentValue: acp.SessionConfigValueId(o.get(e)),
			Options:      acp.SessionConfigSelectOptions{Ungrouped: &values},
		}})
	}
	return out
}

func (a *Agent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	p := params.ValueId
	if p == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "only select options exist"})
	}
	s, err := a.session(p.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	i := slices.IndexFunc(selectOptions, func(o selectOption) bool { return o.id == p.ConfigId })
	if i < 0 {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"configId": p.ConfigId})
	}
	o := selectOptions[i]
	if !slices.ContainsFunc(o.values, func(v acp.SessionConfigSelectOption) bool { return v.Value == p.Value }) {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"configId": p.ConfigId, "value": p.Value})
	}
	if err := o.set(s.env.Engine, string(p.Value)); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: configOptions(s.env.Engine)}, nil
}
