package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func adaptMultiAgentToolInput(toolName, input, wireID, localID string) (string, error) {
	if toolName != ApprovalRequestTool {
		return input, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return "", fmt.Errorf("decode multi-agent approval input: %w", err)
	}
	var innerID string
	if err := json.Unmarshal(fields["tool_call_id"], &innerID); err != nil || innerID == "" {
		return "", fmt.Errorf("multi-agent approval input has no valid tool_call_id")
	}
	if innerID != wireID {
		return "", fmt.Errorf("multi-agent approval input tool_call_id %q does not match wire call id %q", innerID, wireID)
	}
	fields["tool_call_id"], _ = json.Marshal(localID)
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encode multi-agent approval input: %w", err)
	}
	return string(encoded), nil
}

func multiAgentDashboardPayload(raw json.RawMessage) (*DashboardPayload, error) {
	data := struct {
		Title             string          `json:"title"`
		Description       string          `json:"description"`
		LayoutType        string          `json:"layout_type"`
		ReflowType        string          `json:"reflow_type"`
		Widgets           json.RawMessage `json:"widgets"`
		TemplateVariables json.RawMessage `json:"template_variables"`
	}{LayoutType: "ordered", ReflowType: "fixed", Widgets: json.RawMessage(`[]`), TemplateVariables: json.RawMessage(`[]`)}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode multi-agent dashboard: %w", err)
	}
	return &DashboardPayload{
		Title: data.Title, Description: data.Description, LayoutType: data.LayoutType, ReflowType: data.ReflowType,
		Widgets: data.Widgets, TemplateVariables: data.TemplateVariables,
	}, nil
}

const multiAgentTransportTruncation = "\n[Truncated for transport.]"

// encodeMultiAgentToolResults bounds a transport copy against the server's
// character, UTF-8 text, and encoded request limits. Local results stay complete.
func encodeMultiAgentToolResults(results []multiAgentToolResult) ([]byte, error) {
	if len(results) == 0 || len(results) > 32 {
		return nil, fmt.Errorf("multi-agent tool results must contain between 1 and 32 results")
	}
	bounded := append([]multiAgentToolResult(nil), results...)
	for i := range bounded {
		bounded[i].Output = boundMultiAgentText(bounded[i].Output, 1_000_000, len(bounded[i].Output))
		bounded[i].Title = boundMultiAgentText(bounded[i].Title, 512, len(bounded[i].Title))
		bounded[i].Content = boundMultiAgentText(bounded[i].Content, 1_000_000, len(bounded[i].Content))
	}
	for {
		body, err := json.Marshal(multiAgentToolResultsRequest{Results: bounded})
		if err != nil {
			return nil, fmt.Errorf("encode multi-agent tool results: %w", err)
		}
		textBytes := 0
		for _, result := range bounded {
			textBytes += len(result.Output) + len(result.Title) + len(result.Content)
		}
		if textBytes <= 3_500_000 && len(body) <= 4_000_000 {
			return body, nil
		}
		ratio := min(1.0, 3_500_000/float64(max(1, textBytes)), 4_000_000/float64(len(body))) * 0.98
		changed := false
		for i := range bounded {
			for _, field := range []*string{&bounded[i].Output, &bounded[i].Title, &bounded[i].Content} {
				if len(*field) <= len(multiAgentTransportTruncation) {
					continue
				}
				next := boundMultiAgentText(*field, 1_000_000, int(float64(len(*field))*ratio))
				changed = changed || next != *field
				*field = next
			}
		}
		if !changed {
			return nil, fmt.Errorf("multi-agent tool result metadata exceeds request size limit")
		}
	}
}

func boundMultiAgentText(value string, maxChars, maxBytes int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes && utf8.RuneCountInString(value) <= maxChars {
		return value
	}
	suffix := multiAgentTransportTruncation
	value = strings.TrimSuffix(value, suffix)
	byteBudget := max(0, maxBytes-len(suffix))
	charBudget := max(0, maxChars-utf8.RuneCountInString(suffix))
	end, count := 0, 0
	for offset, char := range value {
		next := offset + utf8.RuneLen(char)
		if count >= charBudget || next > byteBudget {
			break
		}
		end = next
		count++
	}
	return value[:end] + suffix
}
