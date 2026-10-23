package adeep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/headless"
)

// roundsBackend streams one scripted round per Send.
type roundsBackend struct {
	rounds [][]assistant.Message
	calls  int
}

func (b *roundsBackend) Send(_ context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	for _, msg := range b.rounds[b.calls-1] {
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "conversation-1"
		response.Data.Attributes.StructuredMessage = msg
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
	}
	return "conversation-1", nil
}

func textMsg(id, text string) assistant.Message {
	return assistant.AssistantMessage(id, assistant.TextContent(text))
}

func usageMsg(id string, value assistant.Usage) assistant.Message {
	msg := textMsg(id, "")
	msg.Results = &assistant.Results{Usage: &value}
	return msg
}

type parsedRecord map[string]any

func parseDelivery(t *testing.T, output string) []parsedRecord {
	t.Helper()
	if output == "" {
		t.Fatal("delivery wrote no records")
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	records := make([]parsedRecord, 0, len(lines))
	for i, line := range lines {
		var record parsedRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not an independent JSON object: %q: %v", i+1, line, err)
		}
		if record["schema"] != Schema || record["version"] != float64(Version) {
			t.Fatalf("line %d lacks schema/version: %v", i+1, record)
		}
		records = append(records, record)
	}
	return records
}

func toolOf(t *testing.T, record parsedRecord) map[string]any {
	t.Helper()
	tool, ok := record["tool"].(map[string]any)
	if !ok {
		t.Fatalf("record has no tool object: %v", record)
	}
	return tool
}

func typeRounds(t *testing.T, records []parsedRecord) []string {
	t.Helper()
	got := make([]string, len(records))
	for i, record := range records {
		typ, ok := record["type"].(string)
		if !ok {
			t.Fatalf("record has no type: %v", record)
		}
		round, _ := record["round"].(float64)
		got[i] = typ + ":" + strconv.Itoa(int(round))
	}
	return got
}

// startedDelivery returns a delivery that has emitted run.started.
func startedDelivery(t *testing.T, w io.Writer) *Delivery {
	t.Helper()
	delivery := New(w)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	return delivery
}

func consume(t *testing.T, delivery *Delivery, events ...agent.Event) {
	t.Helper()
	for _, event := range events {
		if err := delivery.Consume(event); err != nil {
			t.Fatal(err)
		}
	}
}

// testRun is the run every test event belongs to, and that changed every test
// block unless the test says otherwise.
const testRun agent.RunID = 1

func transcript(round int, blocks ...agent.Block) agent.Event {
	return agent.Event{Kind: agent.EventTranscript, Run: testRun, Round: round, Transcript: agent.TranscriptSnapshot{Blocks: blocks}}
}

func textBlock(id string, role assistant.Role, text string) agent.Block {
	return agent.Block{
		ID:       agent.BlockID{Scope: agent.ScopeMessage, Key: id, Kind: assistant.KindText},
		Role:     role,
		Kind:     assistant.KindText,
		Markdown: assistant.TextContent(text).Markdown,
		Run:      testRun,
	}
}

func toolBlock(id, name, input string, status agent.ToolStatus, clientSide bool) agent.Block {
	tool := agent.ToolBlockOf(assistant.ToolCallContent(id, name, input).Tool)
	tool.Status, tool.IsClientSide, tool.Output = status, clientSide, name+" output"
	return agent.Block{ID: agent.BlockID{Scope: agent.ScopeTool, Key: id}, Kind: assistant.KindToolCall, Tool: &tool, Run: testRun}
}

// A real two-round turn run through headless.Run maps to the documented record
// sequence; assistant text appears only in run.finished.
func TestDeliveryStreamsMinimalTwoRoundLifecycle(t *testing.T) {
	inputTokens, outputTokens := 30, 70
	clientCall := assistant.ToolCallContent("cli-1", "get_local_time", "{}")
	clientCall.Type = assistant.ContentClientToolCall
	backend := &roundsBackend{rounds: [][]assistant.Message{
		{
			textMsg("a1", "checking "),
			textMsg("a1", "logs"),
			assistant.AssistantMessage("s1", assistant.ToolCallContent("srv-1", "search_logs", `{"query":"errors"}`)),
			assistant.AssistantMessage("s1", assistant.ToolResultContent("srv-1", "search_logs", assistant.ToolStatusSuccess, "3 hits")),
			assistant.AssistantMessage("c1", clientCall),
			usageMsg("u1", assistant.Usage{TokensUsed: 100, InputTokens: &inputTokens, OutputTokens: &outputTokens}),
		},
		{textMsg("a2", "done."), usageMsg("u2", assistant.Usage{TokensUsed: 50})},
	}}
	tools, err := agent.NewToolSet(agent.Tool{
		Definition: assistant.ClientTool{Name: "get_local_time"},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			return agent.ToolResult{Title: "Local time", Output: `{"timestamp":"2026-09-01T12:00:00Z"}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	outcome, err := headless.Run(t.Context(), agent.New(backend, agent.Config{PermissionsMode: agent.ModeSkipPermissions}), agent.TurnInput{Message: "investigate", Tools: tools}, startedDelivery(t, &out))
	if err != nil || outcome != headless.OutcomeCompleted {
		t.Fatalf("outcome/error = %q, %v", outcome, err)
	}

	records := parseDelivery(t, out.String())
	want := []string{
		"run.started:0", "round.started:1",
		"tool.call:1", "tool.result:1", "tool.call:1", "usage:1", "conversation.updated:1", "tool.result:1",
		"round.finished:1", "round.started:2", "usage:2", "conversation.updated:2",
		"round.finished:2", "run.finished:0",
	}
	if got := typeRounds(t, records); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("records = %v, want %v", got, want)
	}
	for i, want := range map[int]map[string]any{
		2: {"id": "srv-1", "name": "search_logs", "side": "server", "arguments": `{"query":"errors"}`},
		3: {"id": "srv-1", "status": "success", "result": "3 hits"},
		4: {"id": "cli-1", "name": "get_local_time", "side": "client", "arguments": "{}"},
		7: {"id": "cli-1", "status": "success", "result": `{"timestamp":"2026-09-01T12:00:00Z"}`},
	} {
		tool := toolOf(t, records[i])
		for key, value := range want {
			if tool[key] != value {
				t.Fatalf("record %d tool = %v, want %s = %v", i+1, tool, key, value)
			}
		}
	}
	usage, _ := records[5]["usage"].(map[string]any)
	if usage["tokens_used"] != float64(100) || usage["input_tokens"] != float64(30) || usage["output_tokens"] != float64(70) {
		t.Fatalf("usage = %v", usage)
	}
	finished := records[len(records)-1]
	if finished["outcome"] != "completed" || finished["conversation_id"] != "conversation-1" ||
		finished["response"] != "checking logs\n\ndone." || finished["rounds"] != float64(2) {
		t.Fatalf("run.finished = %v", finished)
	}
}

// Every terminal tool in a snapshot gets one result with its mapped status. A
// call still pending is flushed by Finish with its exact arguments and no
// invented result.
func TestDeliveryToolRecords(t *testing.T) {
	const malformed = "{\"query\":\nnot-json"
	var out bytes.Buffer
	delivery := startedDelivery(t, &out)
	consume(t, delivery, transcript(1,
		toolBlock("read-1", "read", "{}", agent.ToolSuccess, true),
		toolBlock("write-1", "write", "{}", agent.ToolError, true),
		toolBlock("gate-1", assistant.ApprovalRequestTool, "{}", agent.ToolDenied, true),
		toolBlock("write-2", "write", "{}", agent.ToolCancelled, true),
		toolBlock("pending-1", "search_logs", malformed, agent.ToolRunning, false),
		toolBlock("pending-2", "search_logs", "", agent.ToolRunning, false),
	))
	if err := delivery.Finish(headless.Finish{Outcome: headless.OutcomeFailed, Err: errors.New("failed")}); err != nil {
		t.Fatal(err)
	}

	calls, results := map[string]string{}, map[string]string{}
	for _, record := range parseDelivery(t, out.String()) {
		switch record["type"] {
		case "tool.call":
			tool := toolOf(t, record)
			calls[tool["id"].(string)] = tool["name"].(string) + "/" + tool["side"].(string) + "/" + tool["arguments"].(string)
		case "tool.result":
			tool := toolOf(t, record)
			if _, called := calls[tool["id"].(string)]; !called {
				t.Fatalf("result before its call: %v", record)
			}
			results[tool["id"].(string)] = tool["status"].(string)
		}
	}
	wantCalls := map[string]string{
		"read-1": "read/client/{}", "write-1": "write/client/{}", "gate-1": "approval_request/client/{}", "write-2": "write/client/{}",
		"pending-1": "search_logs/server/" + malformed, "pending-2": "search_logs/server/",
	}
	wantResults := map[string]string{"read-1": "success", "write-1": "error", "gate-1": "denied", "write-2": "cancelled"}
	for id, want := range wantCalls {
		if calls[id] != want {
			t.Errorf("call %s = %q, want %q", id, calls[id], want)
		}
	}
	for id := range calls {
		if results[id] != wantResults[id] {
			t.Errorf("result %s = %q, want %q", id, results[id], wantResults[id])
		}
	}
}

// The response includes only assistant text first seen during this run, even
// when the engine's snapshots carry history restored before it.
func TestDeliveryResponseOnlyIncludesThisRun(t *testing.T) {
	prior := textBlock("prior", assistant.RoleAssistant, "prior answer")
	prior.Run = 0
	user := textBlock("user", assistant.RoleUser, "new question")
	current := textBlock("current", assistant.RoleAssistant, "current answer")
	for name, events := range map[string][]agent.Event{
		"unobserved blocks": {transcript(1, current)},
		"restored history":  {transcript(0, prior, user), transcript(1, prior, user, current)},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			delivery := startedDelivery(t, &out)
			consume(t, delivery, events...)
			if err := delivery.Finish(headless.Finish{Outcome: headless.OutcomeCompleted, Blocks: []agent.Block{prior, user, current}}); err != nil {
				t.Fatal(err)
			}
			records := parseDelivery(t, out.String())
			if response := records[len(records)-1]["response"]; response != "current answer" {
				t.Fatalf("response = %v, want only this run's answer", response)
			}
		})
	}
}

// Rounds follow the backend sends the engine reports: rounds that emitted no
// content still count, and a turn rejected before any send reports none.
func TestDeliveryRounds(t *testing.T) {
	userEcho := transcript(0, textBlock("user", assistant.RoleUser, "investigate"))
	for _, tt := range []struct {
		name   string
		events []agent.Event
		want   []string
		rounds float64
	}{
		{
			name:   "suppressed stopped rounds",
			events: []agent.Event{{Kind: agent.EventDone, Round: 3}},
			want:   []string{"run.started:0", "round.started:1", "round.finished:1", "round.started:2", "round.finished:2", "round.started:3", "round.finished:3", "run.finished:0"},
			rounds: 3,
		},
		{
			name:   "no backend send",
			events: []agent.Event{userEcho, {Kind: agent.EventError, Err: errors.New("turn active")}},
			want:   []string{"run.started:0", "run.finished:0"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			delivery := startedDelivery(t, &out)
			consume(t, delivery, tt.events...)
			if err := delivery.Finish(headless.Finish{Outcome: headless.OutcomeCompleted}); err != nil {
				t.Fatal(err)
			}
			records := parseDelivery(t, out.String())
			if got := typeRounds(t, records); strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("records = %v, want %v", got, tt.want)
			}
			if rounds := records[len(records)-1]["rounds"]; rounds != tt.rounds {
				t.Fatalf("rounds = %v, want %v", rounds, tt.rounds)
			}
		})
	}
}

// run.started and the single run.finished carry timestamps, and a failed
// outcome carries its error.
func TestDeliveryRunRecords(t *testing.T) {
	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Second)
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{StartedAt: startedAt, RequestedModel: "test-model"}); err != nil {
		t.Fatal(err)
	}
	finish := headless.Finish{EndedAt: endedAt, Outcome: headless.OutcomeTimedOut, Err: context.DeadlineExceeded}
	if err := delivery.Finish(finish); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(finish); err == nil {
		t.Fatal("second Finish succeeded")
	}
	records := parseDelivery(t, out.String())
	if len(records) != 2 {
		t.Fatalf("records = %v, want run.started and one run.finished", records)
	}
	if started := records[0]; started["started_at"] != startedAt.Format(time.RFC3339Nano) || started["requested_model"] != "test-model" {
		t.Fatalf("run.started = %v", started)
	}
	finished := records[1]
	detail, _ := finished["error"].(map[string]any)
	if finished["outcome"] != "timed_out" || finished["ended_at"] != endedAt.Format(time.RFC3339Nano) || detail["message"] != context.DeadlineExceeded.Error() {
		t.Fatalf("run.finished = %v", finished)
	}
}

func TestDeliveryShortWriteFails(t *testing.T) {
	delivery := New(shortWriter{})
	if err := delivery.Start(headless.Start{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Start error = %v, want short write", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestDeliveryWriterFailureLatchesAndFinishEmitsNoTerminal(t *testing.T) {
	writerErr := errors.New("stdout failed")
	writer := &recoveringWriter{writesLeft: 2, err: writerErr}
	var out bytes.Buffer
	delivery := New(io.MultiWriter(writer, &out))
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Consume(transcript(1, toolBlock("read-1", "read", "{}", agent.ToolSuccess, true))); !errors.Is(err, writerErr) {
		t.Fatalf("Consume error = %v, want the writer failure", err)
	}
	// The writer recovers; Finish must still fail without emitting a terminal
	// record, so a consumer can never see a well-formed but incomplete stream.
	finishErr := delivery.Finish(headless.Finish{Outcome: headless.OutcomeCompleted})
	if !errors.Is(finishErr, writerErr) {
		t.Fatalf("Finish error = %v, want the latched writer failure", finishErr)
	}
	if strings.Contains(out.String(), "run.finished") {
		t.Fatalf("terminal record emitted after a writer failure: %s", out.String())
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventDone}); err == nil {
		t.Fatal("Consume after Finish succeeded")
	}
}

// recoveringWriter fails once its successful writes run out, then recovers.
type recoveringWriter struct {
	writesLeft int
	err        error
	failed     bool
}

func (w *recoveringWriter) Write(p []byte) (int, error) {
	if w.writesLeft == 0 && !w.failed {
		w.failed = true
		return 0, w.err
	}
	w.writesLeft--
	return len(p), nil
}

func TestDeliveryLifecycleTransitions(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Consume(agent.Event{}); err == nil {
		t.Fatal("Consume before Start succeeded")
	}
	if err := delivery.Finish(headless.Finish{}); err == nil {
		t.Fatal("Finish before Start succeeded")
	}
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Start(headless.Start{}); err == nil {
		t.Fatal("second Start succeeded")
	}
	if err := delivery.Finish(headless.Finish{Outcome: headless.OutcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventDone}); err == nil {
		t.Fatal("Consume after Finish succeeded")
	}
}
