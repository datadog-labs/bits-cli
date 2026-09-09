package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
)

type recordingExecRunner struct {
	mu       sync.Mutex
	requests []exectool.ExecRequest
	outcome  exectool.ExecOutcome
}

func (r *recordingExecRunner) Run(_ context.Context, request exectool.ExecRequest) exectool.ExecOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return r.outcome
}

func (r *recordingExecRunner) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *recordingExecRunner) lastRequest() exectool.ExecRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

func TestExecCommandDefinitionIsStrictAndTruthful(t *testing.T) {
	tool := newExecCommandTool(t.TempDir(), &recordingExecRunner{})
	if tool.Definition.Name != toolExec {
		t.Fatalf("name = %q, want %q", tool.Definition.Name, toolExec)
	}
	if tool.InputReducer != nil {
		t.Fatal("exec_command registered a streamed-input reducer")
	}
	encoded, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schema := string(encoded)
	for _, want := range []string{`"required":["cmd"]`, `"additionalProperties":false`, `"workdir"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema %s does not contain %s", schema, want)
		}
	}
	for _, want := range []string{"unsandboxed", "environment", "filesystem", "network", "credentials", "agent sockets", "race", "best effort"} {
		if !strings.Contains(strings.ToLower(tool.Definition.Description), want) {
			t.Errorf("description does not disclose %q: %s", want, tool.Definition.Description)
		}
	}
}

func TestExecCommandHandlerValidatesInput(t *testing.T) {
	runner := &recordingExecRunner{}
	tool := newExecCommandTool(t.TempDir(), runner)
	tests := []string{
		`not json`,
		`{}`,
		`{"cmd":""}`,
	}
	for _, input := range tests {
		result, err := tool.Handler(context.Background(), agent.ToolCall{Name: toolExec, Input: input})
		if err != nil {
			t.Fatalf("Handler(%q) error = %v", input, err)
		}
		if !result.IsError || !strings.Contains(result.Output, "invalid input") {
			t.Errorf("Handler(%q) = %+v, want input error", input, result)
		}
	}
	if got := runner.requestCount(); got != 0 {
		t.Fatalf("runner calls = %d for invalid inputs, want 0", got)
	}
}

func TestExecCommandResolvesWorkdirAtTheHandlerBoundary(t *testing.T) {
	turnCWD := t.TempDir()
	abs := t.TempDir()
	runner := &recordingExecRunner{outcome: exectool.ExecOutcome{Reason: exectool.ExecSucceeded}}
	tool := newExecCommandTool(turnCWD, runner)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", input: `{"cmd":"pwd"}`, want: turnCWD},
		{name: "empty defaults", input: `{"cmd":"pwd","workdir":""}`, want: turnCWD},
		{name: "relative", input: `{"cmd":"pwd","workdir":"child/../target"}`, want: filepath.Join(turnCWD, "target")},
		{name: "absolute", input: `{"cmd":"pwd","workdir":` + mustJSONString(t, abs) + `}`, want: abs},
		{name: "outside workspace", input: `{"cmd":"pwd","workdir":"../outside"}`, want: filepath.Clean(filepath.Join(turnCWD, "../outside"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := runner.requestCount()
			result, err := tool.Handler(context.Background(), agent.ToolCall{Name: toolExec, Input: test.input})
			if err != nil || result.IsError {
				t.Fatalf("Handler() = (%+v, %v)", result, err)
			}
			if got := runner.requestCount(); got != before+1 {
				t.Fatalf("runner calls = %d, want %d", got, before+1)
			}
			request := runner.lastRequest()
			if request.Command != "pwd" || request.CWD != test.want || !filepath.IsAbs(request.CWD) {
				t.Fatalf("request = %+v, want command pwd and cwd %q", request, test.want)
			}
		})
	}
}

func TestExecCommandApprovalShowsFinalCommandAndEffectiveWorkdir(t *testing.T) {
	turnCWD := t.TempDir()
	tool := newExecCommandTool(turnCWD, &recordingExecRunner{})
	requirement, needed := tool.Approval(agent.ToolCall{Input: `{"cmd":"go test ./...","workdir":"subdir"}`})
	if !needed {
		t.Fatal("valid exec command did not require approval")
	}
	if requirement.Key.Tool != toolExec || requirement.Key.Resource == "" {
		t.Fatalf("approval key = %+v", requirement.Key)
	}
	same, needed := tool.Approval(agent.ToolCall{Input: `{"cmd":"go test ./...","workdir":"subdir"}`})
	if !needed || same.Key != requirement.Key {
		t.Fatalf("same launch tuple produced a different approval authority: %+v vs %+v", requirement.Key, same.Key)
	}
	different, needed := tool.Approval(agent.ToolCall{Input: `{"cmd":"go test ./other","workdir":"subdir"}`})
	if !needed || different.Key == requirement.Key {
		t.Fatalf("different launch tuple reused approval authority %+v", requirement.Key)
	}
	differentCWD, needed := tool.Approval(agent.ToolCall{Input: `{"cmd":"go test ./...","workdir":"other"}`})
	if !needed || differentCWD.Key == requirement.Key {
		t.Fatalf("different working directory reused approval authority %+v", requirement.Key)
	}
	for _, want := range []string{"cmd: go test ./...", "cwd: " + filepath.Join(turnCWD, "subdir"), "unsandboxed"} {
		if !strings.Contains(requirement.Prompt.Detail, want) {
			t.Errorf("approval detail does not contain %q: %s", want, requirement.Prompt.Detail)
		}
	}
	allowAll, err := agent.NewToolSet(agent.ModeAllowAll, tool)
	if err != nil {
		t.Fatal(err)
	}
	if _, needed := allowAll.Approval(agent.ToolCall{Name: toolExec, Input: `{"cmd":"true"}`}); needed {
		t.Fatal("allow-all did not suppress the exec command approval gate")
	}
}

func TestExecCommandTranslatesEveryTerminalOutcome(t *testing.T) {
	exit0, exit7 := 0, 7
	tests := []struct {
		name      string
		outcome   exectool.ExecOutcome
		wantError bool
		cancelled bool
	}{
		{name: "success", outcome: exectool.ExecOutcome{Reason: exectool.ExecSucceeded, ExitCode: &exit0}},
		{name: "non-zero", outcome: exectool.ExecOutcome{Reason: exectool.ExecNonZeroExit, ExitCode: &exit7}, wantError: true},
		{name: "signal", outcome: exectool.ExecOutcome{Reason: exectool.ExecNonZeroExit, Signal: "terminated"}, wantError: true},
		{name: "launch failure", outcome: exectool.ExecOutcome{Reason: exectool.ExecLaunchFailed, Err: errors.New("start failed")}, wantError: true},
		{name: "timeout", outcome: exectool.ExecOutcome{Reason: exectool.ExecTimedOut, Err: context.DeadlineExceeded}, wantError: true},
		{name: "cancellation", outcome: exectool.ExecOutcome{Reason: exectool.ExecCancelled, Err: context.Canceled}, wantError: true, cancelled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.outcome.Elapsed = 1500 * time.Millisecond
			test.outcome.Output = exectool.ExecOutput{
				Stdout: "out", Stderr: "err", Truncated: true, Incomplete: true,
				StdoutOmittedBytes: 11, StderrOmittedBytes: 12,
			}
			runner := &recordingExecRunner{outcome: test.outcome}
			result, err := newExecCommandTool(t.TempDir(), runner).Handler(context.Background(), agent.ToolCall{Input: `{"cmd":"run"}`})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != test.wantError || result.Cancelled != test.cancelled {
				t.Fatalf("flags = error:%v cancelled:%v", result.IsError, result.Cancelled)
			}
			var model execCommandResult
			if err := json.Unmarshal([]byte(result.Output), &model); err != nil {
				t.Fatalf("model output is invalid JSON: %v\n%s", err, result.Output)
			}
			if model.Status != test.outcome.Reason || model.DurationMS != 1500 || model.Stdout != "out" || model.Stderr != "err" {
				t.Fatalf("model result = %+v", model)
			}
			if !model.Truncated || !model.OutputIncomplete || model.StdoutOmittedBytes != 11 || model.StderrOmittedBytes != 12 {
				t.Fatalf("truncation metadata = %+v", model)
			}
			if test.outcome.ExitCode != nil && (model.ExitCode == nil || *model.ExitCode != *test.outcome.ExitCode) {
				t.Fatalf("exit code = %v, want %d", model.ExitCode, *test.outcome.ExitCode)
			}
			if test.outcome.Signal != "" && (model.ExitCode != nil || model.Signal != test.outcome.Signal) {
				t.Fatalf("signal result = exit:%v signal:%q, want nil/%q", model.ExitCode, model.Signal, test.outcome.Signal)
			}
		})
	}
}

func TestExecCommandSerializesTerminalMetadataBeforeStreams(t *testing.T) {
	output := marshalExecCommandResult(exectool.ExecOutcome{
		Reason:  exectool.ExecSucceeded,
		Elapsed: time.Second,
		Output: exectool.ExecOutput{
			Stdout: "first\nsecond", Stderr: "error", Truncated: true, Incomplete: true,
			StdoutOmittedBytes: 100, StderrOmittedBytes: 200,
		},
	})
	stdoutAt := strings.Index(output, `"stdout":`)
	for _, field := range []string{`"status":`, `"duration_ms":`, `"truncated":true`, `"output_incomplete":true`, `"stdout_omitted_bytes":100`, `"stderr_omitted_bytes":200`} {
		if at := strings.Index(output, field); at < 0 || at > stdoutAt {
			t.Errorf("terminal metadata %s is not serialized before stdout: %s", field, output)
		}
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type execApprovalBackend struct {
	t         *testing.T
	input     string
	preview   string
	responses []assistant.ClientToolResponse
	calls     int
}

func (b *execApprovalBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls == 1 {
		if b.preview != "" {
			started := assistant.Content{Type: assistant.ContentToolCallStarted, Tool: &assistant.ToolPayload{
				ToolCallID: "exec-1", ToolName: toolExec, IsClientSide: true,
			}}
			var response assistant.AssistantResponse
			response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("exec-start", started)
			if err := emit(response); err != nil {
				return "conversation-1", err
			}
			delta := assistant.Content{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{
				ToolCallID: "exec-1", PartialJSON: b.preview,
			}}
			response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("exec-delta", delta)
			if err := emit(response); err != nil {
				return "conversation-1", err
			}
		}
		content := assistant.ToolCallContent("exec-1", toolExec, b.input)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("exec-message", content)
		return "conversation-1", emit(response)
	}
	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func TestExecCommandGatedEngineDoesNotLaunchBeforeApproval(t *testing.T) {
	runner := &recordingExecRunner{outcome: exectool.ExecOutcome{Reason: exectool.ExecSucceeded}}
	backend := &execApprovalBackend{
		t:       t,
		preview: `{"cmd":"touch speculative"}`,
		input:   `{"cmd":"true"}`,
	}
	set, err := agent.NewToolSet(agent.ModeGated, newExecCommandTool(t.TempDir(), runner))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := agent.New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, agent.TurnInput{Message: "run", Tools: set, OnDeny: agent.DenyContinue})

	approved := false
	for event := range events {
		if event.Kind != agent.EventTranscript {
			continue
		}
		pending := event.Transcript.PendingApprovals()
		if len(pending) == 0 {
			continue
		}
		if got := runner.requestCount(); got != 0 {
			t.Fatalf("runner calls before approval = %d", got)
		}
		if !engine.Decide(pending[0].ToolCallID(), agent.ApprovalAllowOnce) {
			t.Fatal("approval decision was not accepted")
		}
		approved = true
		break
	}
	if !approved {
		t.Fatal("turn ended without requesting approval")
	}
	for range events {
	}
	if got := runner.requestCount(); got != 1 {
		t.Fatalf("runner calls after approval = %d, want 1", got)
	}
	if got := runner.lastRequest().Command; got != "true" {
		t.Fatalf("runner command = %q, want authoritative final input, not streamed preview", got)
	}
	if len(backend.responses) != 1 || backend.responses[0].Status != assistant.ToolStatusSuccess {
		t.Fatalf("wire responses = %+v", backend.responses)
	}
}

func TestExecCommandDenialNeverLaunches(t *testing.T) {
	runner := &recordingExecRunner{outcome: exectool.ExecOutcome{Reason: exectool.ExecSucceeded}}
	backend := &execApprovalBackend{t: t, input: `{"cmd":"true"}`}
	set, err := agent.NewToolSet(agent.ModeGated, newExecCommandTool(t.TempDir(), runner))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := agent.New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, agent.TurnInput{Message: "run", Tools: set, OnDeny: agent.DenyContinue})
	denied := false
	for event := range events {
		if event.Kind != agent.EventTranscript || len(event.Transcript.PendingApprovals()) == 0 {
			continue
		}
		if !engine.Decide("exec-1", agent.ApprovalDeny) {
			t.Fatal("denial decision was not accepted")
		}
		denied = true
		break
	}
	if !denied {
		t.Fatal("turn ended without requesting approval")
	}
	for range events {
	}
	if got := runner.requestCount(); got != 0 {
		t.Fatalf("runner calls after denial = %d, want 0", got)
	}
	if len(backend.responses) != 1 || backend.responses[0].Status != assistant.ToolStatusError ||
		!strings.Contains(backend.responses[0].Metadata.Output, "denied") {
		t.Fatalf("denial wire response = %+v", backend.responses)
	}
}

type execSessionScopeBackend struct {
	t       *testing.T
	inputs  []string
	batches [][]assistant.ClientToolResponse
	calls   int
}

func (b *execSessionScopeBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	if b.calls > 0 {
		responses, ok := message.([]assistant.ClientToolResponse)
		if !ok {
			b.t.Fatalf("follow-up has type %T", message)
		}
		b.batches = append(b.batches, responses)
	}
	b.calls++
	if b.calls <= len(b.inputs) {
		callID := fmt.Sprintf("exec-%d", b.calls)
		content := assistant.ToolCallContent(callID, toolExec, b.inputs[b.calls-1])
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("message-"+callID, content)
		return "conversation-1", emit(response)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func TestExecCommandSessionGrantIsScopedToExactLaunchTuple(t *testing.T) {
	runner := &recordingExecRunner{outcome: exectool.ExecOutcome{Reason: exectool.ExecSucceeded}}
	backend := &execSessionScopeBackend{
		t:      t,
		inputs: []string{`{"cmd":"printf first"}`, `{"cmd":"printf second"}`},
	}
	set, err := agent.NewToolSet(agent.ModeGated, newExecCommandTool(t.TempDir(), runner))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := agent.New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, agent.TurnInput{Message: "run both", Tools: set, OnDeny: agent.DenyContinue})

	if !waitForExecApproval(t, events, "exec-1") {
		t.Fatal("first command did not request approval")
	}
	if !engine.Decide("exec-1", agent.ApprovalAllowSession) {
		t.Fatal("session approval was not accepted")
	}
	if !waitForExecApproval(t, events, "exec-2") {
		t.Fatal("second command reused the first command's session authority")
	}
	if got := runner.requestCount(); got != 1 {
		t.Fatalf("runner calls while second command awaits approval = %d, want 1", got)
	}
	if !engine.Decide("exec-2", agent.ApprovalAllowOnce) {
		t.Fatal("second approval was not accepted")
	}
	for range events {
	}
	if got := runner.requestCount(); got != 2 {
		t.Fatalf("runner calls after both approvals = %d, want 2", got)
	}
}

func waitForExecApproval(t *testing.T, events <-chan agent.Event, callID string) bool {
	t.Helper()
	for event := range events {
		if event.Kind != agent.EventTranscript {
			continue
		}
		for _, pending := range event.Transcript.PendingApprovals() {
			if pending.ToolCallID() == callID {
				return true
			}
		}
	}
	return false
}
