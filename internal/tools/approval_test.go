package tools

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func TestEditorApprovalIdentifiesTargetAndPreservesWorkspaceGrant(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	set, err := agent.NewToolSet(agent.ModeManual, NewEditorTools(ws)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, input, title, key string
	}{
		{spec.ReadFile, `{"path":"src/main.go"}`, `Read "src/main.go"?`, approvalKeyWorkspaceRead},
		{spec.ListFiles, `{"path":"src"}`, `List files in "src"?`, approvalKeyWorkspaceRead},
		{spec.ListFiles, `{}`, `List files in "."?`, approvalKeyWorkspaceRead},
		{spec.GrepFiles, `{"path":"src/main.go","pattern":"TODO"}`, `Search files in "src/main.go"?`, approvalKeyWorkspaceRead},
		{spec.GrepFiles, `{"pattern":"TODO"}`, `Search files in "."?`, approvalKeyWorkspaceRead},
		{spec.WriteFile, `{"path":"docs/new file.md","content":"private content"}`, `Create or overwrite "docs/new file.md"?`, approvalKeyWorkspaceWrite},
		{spec.EditFile, `{"path":"配置.go","edits":[{"old_text":"private content","new_text":"replacement"}]}`, `Edit "配置.go"?`, approvalKeyWorkspaceWrite},
		{spec.ReadFile, `{"path":"a\n\u001b[31m.txt"}`, `Read "a\n\x1b[31m.txt"?`, approvalKeyWorkspaceRead},
	} {
		t.Run(tt.name+"/"+tt.title, func(t *testing.T) {
			req, needs := set.Approval(agent.ToolCall{Name: tt.name, Input: tt.input})
			if !needs {
				t.Fatal("editor tool did not require approval")
			}
			if req.Prompt.Title != tt.title {
				t.Errorf("title = %q, want %q", req.Prompt.Title, tt.title)
			}
			if req.Key != (agent.ApprovalKey{Tool: tt.key, Resource: ws.Path()}) {
				t.Errorf("grant changed from workspace scope: %+v", req.Key)
			}
			if !strings.Contains(req.Prompt.Detail, ws.Path()) {
				t.Errorf("workspace missing from detail: %q", req.Prompt.Detail)
			}
			if strings.Contains(req.Prompt.Title+req.Prompt.Detail, "private content") {
				t.Fatal("approval prompt included file content")
			}
		})
	}
}

func TestEditorApprovalMalformedTargetRemainsGated(t *testing.T) {
	for _, tt := range []struct {
		name, title string
		policy      agent.ApprovalPolicy
	}{
		{spec.ReadFile, "Share workspace files?", workspaceReadApproval("/workspace")},
		{spec.WriteFile, "Modify workspace files?", workspaceWriteApproval("/workspace")},
	} {
		for _, input := range []string{`{`, `{}`, `null`, `{"path":null}`, `{"path":123}`, `{"path":"partial",`, `{"path":""}`} {
			t.Run(tt.name+"/"+input, func(t *testing.T) {
				req, needs := tt.policy(agent.ToolCall{Name: tt.name, Input: input})
				if !needs || req.Prompt.Title != tt.title {
					t.Fatalf("invalid input should retain generic approval: needs=%t prompt=%+v", needs, req.Prompt)
				}
			})
		}
	}
}
