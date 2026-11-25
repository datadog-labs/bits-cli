package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// maxAppBodyBytes fits the server's 8 MiB cap on an app's tool result, plus
// the fields a restored app wraps around it.
const maxAppBodyBytes = 16 << 20

// RestoreApp reopens the app a conversation saved under messageID: the
// server runs its tool again and opens a new relay session.
func (c *Client) RestoreApp(ctx context.Context, conversationID, messageID string) (AppPayload, error) {
	body := struct {
		ConversationID string `json:"conversation_id"`
		MessageID      string `json:"message_id"`
	}{conversationID, messageID}
	resp, err := c.do(ctx, http.MethodPost, "/api/unstable/assistant/mcp-apps/restore", body)
	if err != nil {
		return AppPayload{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var content Content
	if err := decodeJSONBody(&content, resp.Body, maxAppBodyBytes); err != nil {
		return AppPayload{}, fmt.Errorf("decode restored app: %w", err)
	}
	if content.Type != ContentApp {
		return AppPayload{}, errors.New("restore returned no app")
	}
	return *content.App, nil
}

// CallAppTool calls a tool on behalf of an app's frame through its relay
// session, and returns the MCP CallToolResult. The server decides which tools
// the app may call. ErrGone reports an expired session.
func (c *Client) CallAppTool(ctx context.Context, relayHandle, name string, arguments json.RawMessage) (json.RawMessage, error) {
	// The server takes an object: JSON null would be refused.
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage(`{}`)
	}
	body := struct {
		RelayHandle string          `json:"relay_handle"`
		Name        string          `json:"name"`
		Arguments   json.RawMessage `json:"arguments"`
	}{relayHandle, name, arguments}
	resp, err := c.do(ctx, http.MethodPost, "/api/unstable/assistant/mcp-apps/call-tool", body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var result json.RawMessage
	if err := decodeJSONBody(&result, resp.Body, maxAppBodyBytes); err != nil {
		return nil, fmt.Errorf("decode app tool result: %w", err)
	}
	return result, nil
}
