package viz

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// NOTE: Keep direct MCP for legacy charts while they still open in ddviz.
// Legacy messages supply a definition without a tool result or a relay session.
func (c *localClient) fetchLegacyWidget(ctx context.Context, widget *assistant.LegacyWidget) (assistant.AppPayload, Status) {
	chart := widget.Chart(time.Now())
	if chart == nil || c.callLegacyTool == nil {
		return assistant.AppPayload{}, notShown
	}
	args := struct {
		Definition json.RawMessage `json:"widget_definition"`
		From       string          `json:"from_ts,omitempty"`
		To         string          `json:"to_ts,omitempty"`
	}{Definition: chart.Definition}
	if !chart.End.IsZero() {
		// get_widget expects Unix seconds. Expand the range to full seconds.
		args.From = strconv.FormatInt(chart.Start.Unix(), 10)
		end := chart.End.Unix()
		if chart.End.Nanosecond() != 0 {
			end++
		}
		args.To = strconv.FormatInt(end, 10)
	}
	input, err := json.Marshal(args)
	if err != nil {
		return assistant.AppPayload{}, failure(err)
	}
	const name = "get_widget"
	raw, err := c.callLegacyTool(ctx, name, input)
	if err != nil {
		return assistant.AppPayload{}, failure(err)
	}
	var result struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError || len(result.StructuredContent) == 0 || string(result.StructuredContent) == "null" {
		return assistant.AppPayload{}, Status{Phase: Failed, Reason: "Datadog did not return a renderable widget.", Retry: RetryNotUseful}
	}
	// Only the frame's presentation fields are needed for this direct result.
	return assistant.AppPayload{ToolName: name, ToolInput: input, Result: raw}, Status{Phase: Succeeded}
}
