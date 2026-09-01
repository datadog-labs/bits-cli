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

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/headless"
)

type turnBackend struct {
	rounds    [][]assistant.Message
	failOn    int
	failErr   error
	responses [][]assistant.ClientToolResponse
	calls     int
}

func (b *turnBackend) Send(ctx context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	if b.calls > 1 {
		responses, ok := message.([]assistant.ClientToolResponse)
		if !ok {
			return "conversation-1", errors.New("follow-up must carry client tool responses")
		}
		b.responses = append(b.responses, responses)
	}
	if b.calls <= len(b.rounds) {
		for _, msg := range b.rounds[b.calls-1] {
			var response assistant.AssistantResponse
			response.Data.Attributes.ConversationID = "conversation-1"
			response.Data.Attributes.StructuredMessage = msg
			if err := emit(response); err != nil {
				return "conversation-1", err
			}
		}
	}
	if b.failOn == b.calls {
		return "conversation-1", b.failErr
	}
	if b.calls > len(b.rounds) {
		return "conversation-1", errors.New("script exhausted")
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

func clientCallMsg(msgID, callID, name, input string) assistant.Message {
	content := assistant.ToolCallContent(callID, name, input)
	content.Type = assistant.ContentClientToolCall
	return assistant.AssistantMessage(msgID, content)
}

func serverCallMsg(msgID, callID, name, input string) assistant.Message {
	return assistant.AssistantMessage(msgID, assistant.ToolCallContent(callID, name, input))
}

func serverResultMsg(msgID, callID, name, output string) assistant.Message {
	return assistant.AssistantMessage(msgID, assistant.ToolResultContent(callID, name, assistant.ToolStatusSuccess, output))
}

func clientToolSet(t *testing.T, mode agent.ApprovalMode) *agent.ToolSet {
	t.Helper()
	tools, err := agent.NewToolSet(mode,
		agent.Tool{
			Definition: assistant.ClientTool{Name: "get_local_time"},
			Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
				return agent.ToolResult{Title: "Local time", Output: `{"timestamp":"2026-09-01T12:00:00Z"}`}, nil
			},
		},
		agent.Tool{
			Definition: assistant.ClientTool{Name: "write"},
			Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
				return agent.ApprovalRequirement{Key: agent.ApprovalKey{Tool: "write", Resource: "workspace"}}, true
			},
			Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
				return agent.ToolResult{Output: "written"}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func runDelivery(t *testing.T, engine *agent.Engine, tools *agent.ToolSet, out io.Writer) (agent.TurnResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	delivery := New(out)
	if err := delivery.Start(headless.Start{StartedAt: time.Now(), RequestedModel: "test-model"}); err != nil {
		return agent.TurnResult{}, err
	}
	consume := func(event agent.Event) error {
		if err := delivery.Consume(event); err != nil {
			return err
		}
		if event.Kind == agent.EventBlock {
			block := event.Update.Changed
			if block.Tool != nil && block.Tool.Status == agent.ToolAwaitingApproval {
				if !engine.Decide(block.ToolCallID(), agent.ApprovalDeny) {
					return errors.New("approval decision for " + block.ToolCallID() + " was not queued")
				}
			}
		}
		return nil
	}
	result, err := engine.RunTurn(ctx, agent.TurnInput{Message: "investigate", Tools: tools, OnDeny: agent.DenyContinue}, consume)
	if finishErr := delivery.Finish(headless.Finish{EndedAt: time.Now(), Result: result, Err: err}); finishErr != nil {
		return result, finishErr
	}
	return result, err
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

func typeRound(t *testing.T, record parsedRecord) string {
	t.Helper()
	typ, ok := record["type"].(string)
	if !ok {
		t.Fatalf("record has no type: %v", record)
	}
	round := 0
	if value, ok := record["round"].(float64); ok {
		round = int(value)
	}
	return typ + ":" + strconv.Itoa(round)
}

func toolStatusOf(t *testing.T, record parsedRecord) string {
	t.Helper()
	status, ok := toolOf(t, record)["status"].(string)
	if !ok {
		t.Fatalf("tool record has no status: %v", record)
	}
	return status
}

func toolIDOf(t *testing.T, record parsedRecord) string {
	t.Helper()
	id, ok := toolOf(t, record)["id"].(string)
	if !ok {
		t.Fatalf("tool record has no id: %v", record)
	}
	return id
}

func deliveryTextBlock(id, text string) agent.Block {
	content := assistant.TextContent(text)
	return agent.Block{
		ID:       agent.BlockID{Scope: agent.ScopeMessage, Key: id, Kind: assistant.KindText},
		Role:     assistant.RoleAssistant,
		Kind:     assistant.KindText,
		Markdown: content.Markdown,
	}
}

func TestDeliveryResponseOnlyIncludesObservedRunBlocks(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	current := deliveryTextBlock("current", "current answer")
	if err := delivery.Consume(agent.Event{
		Kind:   agent.EventBlock,
		Round:  1,
		Update: agent.TranscriptUpdate{Changed: current},
	}); err != nil {
		t.Fatal(err)
	}
	prior := deliveryTextBlock("prior", "prior answer")
	if err := delivery.Finish(headless.Finish{Result: agent.TurnResult{
		Outcome: agent.TurnOutcomeCompleted,
		Blocks:  []agent.Block{prior, current},
	}}); err != nil {
		t.Fatal(err)
	}
	records := parseDelivery(t, out.String())
	finished := records[len(records)-1]
	if finished["response"] != "current answer" {
		t.Fatalf("response = %v, want only the block observed during this run", finished["response"])
	}
}

func TestDeliveryAccountsForSuppressedStoppedRounds(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventTurnDone, Round: 3}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(headless.Finish{Result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted}}); err != nil {
		t.Fatal(err)
	}
	records := parseDelivery(t, out.String())
	want := []string{
		"run.started:0", "round.started:1", "round.finished:1",
		"round.started:2", "round.finished:2", "round.started:3",
		"round.finished:3", "run.finished:0",
	}
	if len(records) != len(want) {
		t.Fatalf("records = %d, want %d:\n%s", len(records), len(want), out.String())
	}
	for i := range want {
		if got := typeRound(t, records[i]); got != want[i] {
			t.Fatalf("record %d = %s, want %s", i+1, got, want[i])
		}
	}
	if rounds := records[len(records)-1]["rounds"]; rounds != float64(3) {
		t.Fatalf("rounds = %v, want 3", rounds)
	}
}

func TestDeliveryStreamsMinimalTwoRoundLifecycle(t *testing.T) {
	inputTokens, outputTokens := 30, 70
	backend := &turnBackend{rounds: [][]assistant.Message{
		{
			textMsg("a1", "checking "),
			textMsg("a1", "logs"),
			serverCallMsg("s1", "srv-1", "search_logs", `{"query":"errors"}`),
			serverResultMsg("s1", "srv-1", "search_logs", "3 hits"),
			clientCallMsg("c1", "cli-1", "get_local_time", "{}"),
			usageMsg("u1", assistant.Usage{TokensUsed: 100, InputTokens: &inputTokens, OutputTokens: &outputTokens}),
		},
		{textMsg("a2", "done."), usageMsg("u2", assistant.Usage{TokensUsed: 50})},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	result, err := runDelivery(t, engine, clientToolSet(t, agent.ModeAllowAll), &out)
	if err != nil || result.Outcome != agent.TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}

	records := parseDelivery(t, out.String())
	want := []string{
		"run.started:0", "round.started:1",
		"tool.call:1", "tool.result:1", "tool.call:1", "usage:1", "conversation.updated:1", "tool.result:1",
		"round.finished:1", "round.started:2", "usage:2", "conversation.updated:2",
		"round.finished:2", "run.finished:0",
	}
	if len(records) != len(want) {
		t.Fatalf("records = %d, want %d:\n%s", len(records), len(want), out.String())
	}
	for i := range want {
		if got := typeRound(t, records[i]); got != want[i] {
			t.Fatalf("record %d = %s, want %s", i+1, got, want[i])
		}
	}
	for _, record := range records {
		if record["type"] == "assistant.text" {
			t.Fatalf("assistant text leaked outside run.finished: %v", record)
		}
	}

	serverCall := toolOf(t, records[2])
	if serverCall["id"] != "srv-1" || serverCall["name"] != "search_logs" || serverCall["side"] != "server" || serverCall["arguments"] != `{"query":"errors"}` {
		t.Fatalf("server call = %v", serverCall)
	}
	serverResult := toolOf(t, records[3])
	if serverResult["id"] != "srv-1" || serverResult["status"] != "success" || serverResult["result"] != "3 hits" {
		t.Fatalf("server result = %v", serverResult)
	}
	clientCall := toolOf(t, records[4])
	if clientCall["id"] != "cli-1" || clientCall["name"] != "get_local_time" || clientCall["side"] != "client" || clientCall["arguments"] != "{}" {
		t.Fatalf("client call = %v", clientCall)
	}
	clientResult := toolOf(t, records[7])
	if clientResult["id"] != "cli-1" || clientResult["status"] != "success" || clientResult["result"] != `{"timestamp":"2026-09-01T12:00:00Z"}` {
		t.Fatalf("client result = %v", clientResult)
	}
	usage, ok := records[5]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage record has no usage object: %v", records[5])
	}
	if usage["tokens_used"] != float64(100) || usage["input_tokens"] != float64(30) || usage["output_tokens"] != float64(70) {
		t.Fatalf("usage = %v", usage)
	}
	finished := records[len(records)-1]
	if finished["outcome"] != "completed" || finished["conversation_id"] != "conversation-1" ||
		finished["response"] != "checking logs\n\ndone." || finished["rounds"] != float64(2) {
		t.Fatalf("run.finished = %v", finished)
	}
}

func TestDeliveryDenialsAndSiblingResultAreCorrelated(t *testing.T) {
	backend := &turnBackend{rounds: [][]assistant.Message{
		{
			clientCallMsg("c1", "deny-1", "write", `{"path":"one"}`),
			clientCallMsg("c2", "deny-2", "write", `{"path":"two"}`),
			clientCallMsg("c3", "read-1", "get_local_time", "{}"),
		},
		{textMsg("a2", "adjusted answer")},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	result, err := runDelivery(t, engine, clientToolSet(t, agent.ModeGated), &out)
	if err != nil || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 3 {
		t.Fatalf("wire responses = %+v", backend.responses)
	}

	statuses := map[string]string{}
	terminal := 0
	for _, record := range parseDelivery(t, out.String()) {
		switch record["type"] {
		case "tool.result":
			statuses[toolIDOf(t, record)] = toolStatusOf(t, record)
		case "run.finished":
			terminal++
			if record["outcome"] != "approval_denied" || record["response"] != "adjusted answer" {
				t.Fatalf("run.finished = %v", record)
			}
		}
	}
	if statuses["deny-1"] != "denied" || statuses["deny-2"] != "denied" || statuses["read-1"] != "success" {
		t.Fatalf("tool statuses = %v", statuses)
	}
	if terminal != 1 {
		t.Fatalf("terminal records = %d, want 1", terminal)
	}
}

func TestDeliveryPendingCallAndExactMalformedPayloads(t *testing.T) {
	backendErr := errors.New("stream failed")
	malformed := "{\"query\":\nnot-json"
	backend := &turnBackend{
		rounds:  [][]assistant.Message{{serverCallMsg("s1", "pending-1", "search_logs", malformed), textMsg("a1", "partial")}},
		failOn:  1,
		failErr: backendErr,
	}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer

	result, err := runDelivery(t, engine, clientToolSet(t, agent.ModeAllowAll), &out)
	if !errors.Is(err, backendErr) || result.Outcome != agent.TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	records := parseDelivery(t, out.String())
	calls, results, terminals := 0, 0, 0
	for _, record := range records {
		switch record["type"] {
		case "tool.call":
			calls++
			if toolOf(t, record)["arguments"] != malformed {
				t.Fatalf("arguments were not preserved exactly: %v", record)
			}
		case "tool.result":
			results++
		case "run.finished":
			terminals++
			if record["outcome"] != "failed" || record["response"] != "partial" {
				t.Fatalf("run.finished = %v", record)
			}
		}
	}
	if calls != 1 || results != 0 || terminals != 1 {
		t.Fatalf("calls/results/terminals = %d/%d/%d, want 1/0/1", calls, results, terminals)
	}
}

func TestDeliveryFlushesEmptyArgumentPendingCallAtFinish(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	block := agent.ToolBlockOf(assistant.ToolCallContent("pending-1", "search_logs", "").Tool)
	if err := delivery.Consume(agent.Event{
		Kind:  agent.EventBlock,
		Round: 1,
		Update: agent.TranscriptUpdate{Changed: agent.Block{
			ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "pending-1"},
			Kind: assistant.KindToolCall,
			Tool: &block,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(headless.Finish{Result: agent.TurnResult{Outcome: agent.TurnOutcomeFailed}, Err: errors.New("failed")}); err != nil {
		t.Fatal(err)
	}
	records := parseDelivery(t, out.String())
	if len(records) != 5 || records[2]["type"] != "tool.call" || toolOf(t, records[2])["arguments"] != "" {
		t.Fatalf("pending empty-input call was not flushed honestly:\n%s", out.String())
	}
}

func TestDeliveryTimedOutAndFinishIsSingleTerminal(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	finish := headless.Finish{
		Result: agent.TurnResult{Outcome: agent.TurnOutcomeDeadlineExceeded},
		Err:    context.DeadlineExceeded,
	}
	if err := delivery.Finish(finish); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(finish); err == nil {
		t.Fatal("second Finish succeeded")
	}
	records := parseDelivery(t, out.String())
	terminals := 0
	for _, record := range records {
		if record["type"] == "run.finished" {
			terminals++
			if record["outcome"] != "timed_out" {
				t.Fatalf("outcome = %v", record["outcome"])
			}
			detail, ok := record["error"].(map[string]any)
			if !ok || detail["message"] != context.DeadlineExceeded.Error() {
				t.Fatalf("error = %v", record["error"])
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal records = %d, want 1", terminals)
	}
}

func TestDeliveryWriterFailureUsesConsumerFailure(t *testing.T) {
	writerErr := errors.New("stdout failed")
	writer := &failAfterWriter{writesLeft: 2, err: writerErr}
	delivery := New(writer)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	backend := &turnBackend{rounds: [][]assistant.Message{{serverCallMsg("s1", "srv-1", "search_logs", "{}")}}}
	engine := agent.New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), agent.TurnInput{
		Message: "investigate",
		Tools:   clientToolSet(t, agent.ModeAllowAll),
	}, delivery.Consume)
	if !errors.Is(err, writerErr) || result.Outcome != agent.TurnOutcomeConsumerFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
}

type failAfterWriter struct {
	writesLeft int
	err        error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.writesLeft == 0 {
		return 0, w.err
	}
	w.writesLeft--
	return len(p), nil
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
	if err := delivery.Consume(agent.Event{Kind: agent.EventUsage, Round: 1, Usage: &assistant.Usage{}}); err == nil {
		t.Fatal("writer failure was not returned")
	}
	// The writer recovers; Finish must still fail without emitting a terminal
	// record, so a consumer can never see a well-formed but incomplete stream.
	finishErr := delivery.Finish(headless.Finish{Result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted}})
	if !errors.Is(finishErr, writerErr) {
		t.Fatalf("Finish error = %v, want the latched writer failure", finishErr)
	}
	if strings.Contains(out.String(), "run.finished") {
		t.Fatalf("terminal record emitted after a writer failure: %s", out.String())
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventTurnDone}); err == nil {
		t.Fatal("Consume after Finish succeeded")
	}
}

type recoveringWriter struct {
	writesLeft int
	err        error
}

func (w *recoveringWriter) Write(p []byte) (int, error) {
	if w.writesLeft == 0 {
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
	if err := delivery.Finish(headless.Finish{Result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted}}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventTurnDone}); err == nil {
		t.Fatal("Consume after Finish succeeded")
	}
}

func TestDeliveryEmitsRunTimestamps(t *testing.T) {
	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Second)
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{StartedAt: startedAt}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(headless.Finish{EndedAt: endedAt, Result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted}}); err != nil {
		t.Fatal(err)
	}
	records := parseDelivery(t, out.String())
	if records[0]["started_at"] != startedAt.Format(time.RFC3339Nano) {
		t.Fatalf("started_at = %v", records[0]["started_at"])
	}
	finished := records[len(records)-1]
	if finished["ended_at"] != endedAt.Format(time.RFC3339Nano) {
		t.Fatalf("ended_at = %v", finished["ended_at"])
	}
}

func TestDeliveryReportsServerGateAndCancelledSiblings(t *testing.T) {
	backend := &turnBackend{rounds: [][]assistant.Message{
		{
			clientCallMsg("g1", "gate-1", assistant.ApprovalRequestTool, `{"action":"delete_dashboard"}`),
			clientCallMsg("s1", "sibling-1", "write", `{"value":"exact"}`),
		},
		{textMsg("a2", "adjusted answer")},
	}}
	engine := agent.New(backend, assistant.SendOptions{})
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}

	// DenyStop mirrors the interactive surface: the gate is denied, the pending
	// sibling is cancelled into the same batch, and the drain closes the round.
	result, err := engine.RunTurn(t.Context(), agent.TurnInput{
		Message: "write something",
		Tools:   clientToolSet(t, agent.ModeGated),
	}, delivery.Consume)
	if finishErr := delivery.Finish(headless.Finish{Result: result, Err: err}); finishErr != nil {
		t.Fatal(finishErr)
	}
	if err != nil || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}

	gateCall, gateStatus, siblingStatus, outcome := "", "", "", ""
	for _, record := range parseDelivery(t, out.String()) {
		switch record["type"] {
		case "tool.call":
			if toolIDOf(t, record) == "gate-1" {
				tool := toolOf(t, record)
				name, _ := tool["name"].(string)
				side, _ := tool["side"].(string)
				gateCall = name + "/" + side
			}
		case "tool.result":
			switch toolIDOf(t, record) {
			case "gate-1":
				gateStatus = toolStatusOf(t, record)
			case "sibling-1":
				siblingStatus = toolStatusOf(t, record)
			}
		case "run.finished":
			outcome, _ = record["outcome"].(string)
		}
	}
	if gateCall != "approval_request/client" {
		t.Fatalf("gate call = %q, want approval_request/client", gateCall)
	}
	if gateStatus != "denied" || siblingStatus != "cancelled" {
		t.Fatalf("gate/sibling status = %q/%q, want denied/cancelled", gateStatus, siblingStatus)
	}
	if outcome != "approval_denied" {
		t.Fatalf("outcome = %q, want approval_denied", outcome)
	}
}

// A turn that never reaches a backend send must report no round at all,
// including through the engine's local echo of the user's message.
func TestDeliveryReportsNoRoundWithoutBackendSend(t *testing.T) {
	var out bytes.Buffer
	delivery := New(&out)
	if err := delivery.Start(headless.Start{}); err != nil {
		t.Fatal(err)
	}
	userEcho := agent.Block{Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: assistant.TextContent("investigate").Markdown}
	if err := delivery.Consume(agent.Event{
		Kind:   agent.EventBlock,
		Round:  1,
		Update: agent.TranscriptUpdate{Changed: userEcho},
	}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Consume(agent.Event{Kind: agent.EventError, Round: 0, Err: errors.New("turn active")}); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Finish(headless.Finish{
		Result: agent.TurnResult{Outcome: agent.TurnOutcomeFailed},
		Err:    errors.New("turn active"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, record := range parseDelivery(t, out.String()) {
		typ, _ := record["type"].(string)
		if typ != "run.started" && typ != "run.finished" {
			t.Fatalf("unexpected record without a backend send: %v", record)
		}
		if typ == "run.finished" {
			if rounds, _ := record["rounds"].(float64); rounds != 0 {
				t.Fatalf("rounds = %v, want 0", rounds)
			}
		}
	}
}
