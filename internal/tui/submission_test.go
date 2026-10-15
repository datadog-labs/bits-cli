package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
)

func TestQueuedBatchWaitsForCloseAndPreservesDraft(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 3)}
	m := New(agent.New(backend, agent.Config{}))
	setConversationInput(m, "first")
	m.submit()
	firstGeneration := m.op.gen
	setConversationInput(m, "second")
	m.editor.AddAttachment(editor.Attachment{Type: "service", ID: "api", Label: "API"})
	m.submit()
	setConversationInput(m, "third")
	m.editor.AddAttachment(editor.Attachment{Type: "service", ID: "api", Label: "API"})
	m.submit()
	setConversationInput(m, "unfinished draft")
	for ev := range m.op.events {
		m.applyEvent(ev)
	}
	if m.editor.QueueLen() != 2 || m.op.gen != firstGeneration || len(backend.requests) != 1 {
		t.Fatal("queue dispatched before channel closure was handled")
	}
	m.handleTurnClosed(turnClosedMsg{generation: firstGeneration})
	if m.editor.QueueLen() != 0 || m.op.gen == firstGeneration {
		t.Fatal("next prompt did not start")
	}
	if m.editor.Value() != "unfinished draft" {
		t.Fatal("batch dispatch reset draft")
	}
	setConversationInput(m, "fourth")
	m.submit()
	setConversationInput(m, "unfinished draft")
	m.handleTurnClosed(turnClosedMsg{generation: firstGeneration})
	if m.op.gen != firstGeneration+1 || m.editor.QueueLen() != 1 {
		t.Fatal("stale close changed the active batch")
	}
	drainConversationRemote(t, m)
	prompts := m.transcript.UserPrompts()
	if len(prompts) != 3 || prompts[0].Text != "first" || prompts[1].Text != "second\n\nthird" || prompts[2].Text != "fourth" {
		t.Fatalf("prompts = %+v", prompts)
	}
	if len(prompts[1].Context) != 1 || prompts[1].Context[0].ID != "api" {
		t.Fatalf("attachments crossed prompt boundaries: %+v", prompts)
	}
	if m.editor.Value() != "unfinished draft" || m.editor.QueueLen() != 0 {
		t.Fatal("dispatch changed current draft")
	}
}

func TestQueuePausesAtInterruptedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop stopLevel
	}{
		{"incomplete turn", stopNone},
		{"stopped tools", stopTools},
		{"cancelled turn", stopAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.op = operation{kind: opTurn, gen: 1, events: make(chan agent.Event), stop: tc.stop, completed: tc.stop != stopNone}
			m.editor.Enqueue(editor.Prompt{Text: "later"})
			m.handleTurnClosed(turnClosedMsg{generation: 1})
			if !m.editor.QueuePaused() || m.editor.QueueLen() != 1 || m.op.busy() {
				t.Fatal("interruption did not retain and pause queue")
			}
			// Starting a new conversation must discard these unsent prompts.
			m.startNewConversation()
			if m.editor.QueueLen() != 0 {
				t.Fatal("queue leaked into new conversation")
			}
		})
	}
}

func TestQueuedBatchPreservesSkillBoundaries(t *testing.T) {
	prompts := []editor.Prompt{
		{Text: "one"},
		{Text: "two"},
		{Text: "/skill:review Keep CASE"},
		{Text: "three"},
		{Text: "four"},
	}
	for _, want := range []struct {
		text  string
		count int
	}{
		{"one\n\ntwo", 2}, {"/skill:review Keep CASE", 1}, {"three\n\nfour", 2},
	} {
		got, count := queuedBatch(prompts)
		if got.Text != want.text || count != want.count {
			t.Fatalf("batch = %q, %d", got.Text, count)
		}
		prompts = prompts[count:]
	}
}

func TestOversizedImageBatchStaysQueuedUntilCorrected(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Enqueue(editor.Prompt{Text: "keep this"})
	images := editor.Prompt{Text: "with images"}
	for i := range 5 {
		images.Attachments = append(images.Attachments, editor.Attachment{Type: string(assistant.EntityImageURL), ID: fmt.Sprintf("file:///image-%d.png", i)})
	}
	m.editor.Enqueue(images)
	if cmd := m.dispatchQueued(); cmd != nil || m.op.busy() || m.editor.QueueLen() != 2 || !m.editor.QueuePaused() {
		t.Fatal("invalid batch was consumed or started")
	}
	if !strings.Contains(latestNotice(m).Text, "at most 4") {
		t.Fatalf("notice = %q", latestNotice(m).Text)
	}
	m.editor.RemoveQueued(1)
	m.submit() // Empty Enter explicitly resumes the corrected batch.
	drainConversationRemote(t, m)
	if m.editor.QueueLen() != 0 || m.editor.QueuePaused() || len(backend.requests) != 1 {
		t.Fatal("corrected batch did not resume")
	}
}

func TestFailedTurnPreservesAndPausesQueuedPrompts(t *testing.T) {
	failure := errors.New("backend failed")
	m := New(agent.New(&failedConversationBackend{err: failure}, agent.Config{}))
	setConversationInput(m, "first")
	m.submit()
	setConversationInput(m, "later")
	m.submit()
	drainConversationRemote(t, m)
	prompts := m.editor.QueuedPrompts()
	if len(prompts) != 1 || prompts[0].Text != "later" || !m.editor.QueuePaused() || !errors.Is(latestNotice(m).Err, failure) {
		t.Fatal("backend error did not retain and pause pending prompts")
	}
}
