package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
)

// scriptBackend scripts the run surface's backend: round 1 may emit the
// server-injected approval_request gate and client tool calls; every
// follow-up send records its response batch and, unless failOn matches,
// streams the final answer.
type scriptBackend struct {
	rounds       [][]assistant.Message
	failOn       int
	failErr      error
	calls        int
	firstMessage any
	batches      [][]assistant.ClientToolResponse
}

func (b *scriptBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	if b.calls == 1 {
		b.firstMessage = message
	}
	if b.calls > 1 {
		responses, ok := message.([]assistant.ClientToolResponse)
		if !ok {
			return "conversation-1", errors.New("follow-up must carry client tool responses")
		}
		b.batches = append(b.batches, responses)
	}
	for _, m := range b.rounds[min(b.calls, len(b.rounds))-1] {
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "conversation-1"
		response.Data.Attributes.StructuredMessage = m
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
	}
	if b.calls == b.failOn && b.failOn > 0 {
		return "conversation-1", b.failErr
	}
	return "conversation-1", nil
}

func clientCallMessage(msgID, callID, name, input string) assistant.Message {
	content := assistant.ToolCallContent(callID, name, input)
	content.Type = assistant.ContentClientToolCall
	return assistant.AssistantMessage(msgID, content)
}

func runTextMessage(id, text string) assistant.Message {
	return assistant.AssistantMessage(id, assistant.TextContent(text))
}

// runToolSet is the gated shape under test: a local clock tool with a declared
// gate, plus an ungated reader.
func runToolSet(t *testing.T, mode agent.ApprovalMode) *agent.ToolSet {
	t.Helper()
	tools, err := agent.NewToolSet(mode,
		agent.Tool{
			Definition: assistant.ClientTool{Name: "get_local_time"},
			Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
				return agent.ApprovalRequirement{Key: agent.ApprovalKey{Tool: "get_local_time", Resource: "process-clock"}}, true
			},
			Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
				return agent.ToolResult{Output: "noon"}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func runOptions(approval agent.ApprovalMode) cmd.RunOptions {
	return cmd.RunOptions{
		ChatOptions: cmd.ChatOptions{AuthMode: "auto", ApprovalMode: approval},
		Prompt:      "what time is it?",
		Delivery:    "adeep",
		Model:       "test-model",
	}
}

// The fake-backend path proves the noninteractive surface end to end: no TUI,
// no login, strict versioned JSONL, and a completed outcome with exit nil.
func TestRunFakeBackendStreamsVersionedJSONL(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	opts := runOptions(agent.ModeAllowAll)
	opts.Prompt = "random()"
	var out bytes.Buffer

	err := runRunWithStore(context.Background(), opts, stubCredentialStore{}, &out)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("delivery lines = %d, want at least 4:\n%s", len(lines), out.String())
	}
	var finished map[string]any
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not an independent JSON object: %q: %v", line, err)
		}
		if record["schema"] != "bits.delivery.adeep" || record["version"] != float64(1) {
			t.Fatalf("line lacks the delivery discriminator: %q", line)
		}
		if record["type"] == "run.started" {
			if record["requested_model"] != "test-model" {
				t.Fatalf("run.started requested_model = %v", record["requested_model"])
			}
		}
		if record["type"] == "assistant.text" {
			t.Fatalf("assistant text must appear only in run.finished.response: %v", record)
		}
		if record["type"] == "run.finished" {
			finished = record
		}
	}
	if finished == nil || finished["outcome"] != "completed" {
		t.Fatalf("terminal record = %v, want completed", finished)
	}
}

// A scripted fake turn with a client tool call is delivered as two rounds with
// a correlated tool call and result.
func TestRunFakeBackendScriptedRounds(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	opts := runOptions(agent.ModeAllowAll)
	opts.Prompt = "call(\"list_files\", {\"path\": \".\"})\nsay(\"done\")"
	var out bytes.Buffer

	if err := runRunWithStore(context.Background(), opts, stubCredentialStore{}, &out); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	counts := map[string]int{}
	var outcome any
	for line := range strings.Lines(out.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not JSON: %q: %v", line, err)
		}
		typ, _ := record["type"].(string)
		counts[typ]++
		if typ == "run.finished" {
			outcome = record["outcome"]
		}
	}
	if counts["round.started"] != 2 || counts["tool.call"] != 1 || counts["tool.result"] != 1 || outcome != "completed" {
		t.Fatalf("records = %v, outcome = %v:\n%s", counts, outcome, out.String())
	}
}

// A missing OAuth session fails fast with the `bits login` hint; run never
// opens interactive login or a TUI.
func TestRunMissingOAuthFailsFastWithLoginHint(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	var out bytes.Buffer

	err := runRunWithStore(context.Background(), runOptions(agent.ModeAllowAll), stubCredentialStore{err: auth.ErrNoSession}, &out)
	if err == nil {
		t.Fatal("run succeeded without an OAuth session")
	}
	if !strings.Contains(err.Error(), "run `bits login`") {
		t.Fatalf("error = %q, want a `bits login` hint", err)
	}
	if !strings.Contains(err.Error(), "OAuth login is required") {
		t.Fatalf("error = %q, want the login-required classification", err)
	}
	var exitErr *cmd.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == cmd.ExitUsage {
		t.Fatalf("login-required misclassified as usage: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("login path wrote to the delivery stream: %q", out.String())
	}
}

// Gated denial through the real run action: the denial reaches the backend,
// the model's adjusted answer is delivered, and the process maps the typed
// outcome to exit 3.
func TestRunGatedDenialExitsThree(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{
		{
			clientCallMessage("g1", "gate-1", assistant.ApprovalRequestTool, `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1","approval_message":"Delete it?"}`),
		},
		{
			runTextMessage("a1", "I cannot delete the dashboard."),
		},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	err := runEngineTurn(context.Background(), engine, runToolSet(t, agent.ModeGated), runOptions(agent.ModeGated), &out)
	if err == nil {
		t.Fatal("denial completed with exit 0")
	}
	var exitErr *cmd.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != cmd.ExitApprovalDenied {
		t.Fatalf("error = %v, want exit %d", err, cmd.ExitApprovalDenied)
	}

	if len(backend.batches) != 1 || len(backend.batches[0]) != 1 {
		t.Fatalf("response batches = %v", backend.batches)
	}
	response := backend.batches[0][0]
	if response.ToolCallID != "gate-1" || response.Status != assistant.ToolStatusError {
		t.Fatalf("denial response = %+v", response)
	}
	if !strings.Contains(out.String(), `"type":"run.finished"`) ||
		!strings.Contains(out.String(), `"outcome":"approval_denied"`) {
		t.Fatalf("terminal record missing the approval_denied outcome:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "I cannot delete the dashboard.") {
		t.Fatal("the model's adjusted answer was not delivered")
	}
}

func TestAutoDenyApprovalReportsRejectedDecision(t *testing.T) {
	block := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "cli-1"},
		Tool: &agent.ToolBlock{Status: agent.ToolAwaitingApproval},
	}
	called := false
	err := autoDenyApproval(func(id string, decision agent.ApprovalDecision) bool {
		called = true
		if id != "cli-1" || decision != agent.ApprovalDeny {
			t.Fatalf("decision = %q/%q", id, decision)
		}
		return false
	}, block)
	if !called || err == nil || err.Error() != "failed to auto-deny approval gate for tool call cli-1: command queue full" {
		t.Fatalf("called/error = %v/%v", called, err)
	}
}

// Allow-all approves both a local gated tool and the backend-injected
// approval_request gate; both proceed and the run exits 0.
func TestRunAllowAllApprovesLocalAndServerGates(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{
		{
			clientCallMessage("gate", "gate-1", assistant.ApprovalRequestTool, `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1","approval_message":"Delete it?"}`),
			clientCallMessage("c1", "cli-1", "get_local_time", "{}"),
		},
		{
			runTextMessage("a1", "It is noon."),
		},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	err := runEngineTurn(context.Background(), engine, runToolSet(t, agent.ModeAllowAll), runOptions(agent.ModeAllowAll), &out)
	if err != nil {
		t.Fatalf("allow-all run failed: %v", err)
	}
	if len(backend.batches) != 1 || len(backend.batches[0]) != 2 {
		t.Fatalf("response batches = %v", backend.batches)
	}
	gate, local := backend.batches[0][0], backend.batches[0][1]
	if gate.ToolCallID != "gate-1" || gate.Status != assistant.ToolStatusSuccess {
		t.Fatalf("server gate response = %+v, want approval", gate)
	}
	if local.ToolCallID != "cli-1" || local.Status != assistant.ToolStatusSuccess || local.Metadata.Output != "noon" {
		t.Fatalf("local gated tool response = %+v", local)
	}
	if !strings.Contains(out.String(), `"outcome":"completed"`) {
		t.Fatalf("terminal record is not completed:\n%s", out.String())
	}
}

// Denial followed by a backend failure: the runtime failure wins over exit 3,
// while the denial evidence stays in the stream.
func TestRunRuntimeFailureWinsOverDenial(t *testing.T) {
	backendErr := errors.New("backend failed after denial")
	backend := &scriptBackend{
		rounds: [][]assistant.Message{
			{clientCallMessage("c1", "cli-1", "get_local_time", "{}")},
			{runTextMessage("a1", "unreachable")},
		},
		failOn:  2,
		failErr: backendErr,
	}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	err := runEngineTurn(context.Background(), engine, runToolSet(t, agent.ModeGated), runOptions(agent.ModeGated), &out)
	if !errors.Is(err, backendErr) {
		t.Fatalf("error = %v, want the backend failure to win over exit 3", err)
	}
	var exitErr *cmd.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == cmd.ExitApprovalDenied {
		t.Fatalf("denial status won over the runtime failure: %v", err)
	}
	if !strings.Contains(out.String(), `"status":"denied"`) {
		t.Fatalf("denial evidence was lost from the stream:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"outcome":"failed"`) {
		t.Fatalf("terminal outcome is not failed:\n%s", out.String())
	}
}

// Cancellation maps to the runtime failure exit, with an authoritative
// terminal record.
func TestRunCancellationIsRuntimeFailure(t *testing.T) {
	backend := &blockingRunBackend{started: make(chan struct{})}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runEngineTurn(ctx, engine, runToolSet(t, agent.ModeGated), runOptions(agent.ModeGated), &out)
	}()
	<-backend.started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var exitErr *cmd.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == cmd.ExitApprovalDenied {
		t.Fatalf("cancellation was misclassified as denial: %v", err)
	}
	if !strings.Contains(out.String(), `"outcome":"canceled"`) {
		t.Fatalf("terminal outcome is not canceled:\n%s", out.String())
	}
}

type blockingRunBackend struct{ started chan struct{} }

func (b *blockingRunBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	var response assistant.AssistantResponse
	response.Data.Attributes.ConversationID = "conversation-1"
	response.Data.Attributes.StructuredMessage = runTextMessage("a1", "partial")
	if err := emit(response); err != nil {
		return "conversation-1", err
	}
	close(b.started)
	<-ctx.Done()
	return "conversation-1", ctx.Err()
}

// A broken delivery writer fails the process through RunTurn's
// consumer-cancellation path.
func TestRunWriterFailureIsRuntimeFailure(t *testing.T) {
	writerErr := errors.New("stdout gone")
	backend := &scriptBackend{rounds: [][]assistant.Message{
		{runTextMessage("a1", "answer")},
	}}
	engine := agent.New(backend, assistant.SendOptions{})

	err := runEngineTurn(context.Background(), engine, runToolSet(t, agent.ModeAllowAll), runOptions(agent.ModeAllowAll), failRunWriter{err: writerErr})
	if !errors.Is(err, writerErr) {
		t.Fatalf("error = %v, want the writer failure", err)
	}
	var exitErr *cmd.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == cmd.ExitApprovalDenied {
		t.Fatalf("writer failure was misclassified as denial: %v", err)
	}
}

type failRunWriter struct{ err error }

func (w failRunWriter) Write([]byte) (int, error) { return 0, w.err }

// The one-turn guarantee: a scripted two-round backend is sent exactly the
// initial prompt plus one response batch.
func TestRunExecutesExactlyOneTurn(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{
		{clientCallMessage("c1", "cli-1", "get_local_time", "{}")},
		{runTextMessage("a1", "It is noon.")},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	if err := runEngineTurn(context.Background(), engine, runToolSet(t, agent.ModeAllowAll), runOptions(agent.ModeAllowAll), &out); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 {
		t.Fatalf("backend sends = %d, want exactly 2 (one turn, one response round)", backend.calls)
	}
	if backend.firstMessage != "what time is it?" {
		t.Fatalf("first backend message = %#v, want the literal user prompt", backend.firstMessage)
	}
	if !strings.Contains(out.String(), `"rounds":2`) {
		t.Fatalf("round count missing from the terminal record:\n%s", out.String())
	}
}
