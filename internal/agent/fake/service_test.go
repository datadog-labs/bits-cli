package fake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

var errStopReading = errors.New("stop reading")

// subscribeUntil follows the conversation and returns what it saw, by
// summary, until done reports the stream has shown enough.
func subscribeUntil(t *testing.T, f *Fake, id string, onReplayed func(), done func(assistant.Message, bool) bool) []string {
	t.Helper()
	var got []string
	replayed := false
	err := f.Subscribe(context.Background(), id, assistant.SubscribeOptions{}, func(msg assistant.Message) error {
		if msg.Content.Type == assistant.ContentReplayComplete {
			replayed = true
			if onReplayed != nil {
				onReplayed()
			}
		}
		key, head, _ := summarize(msg)
		switch {
		case msg.Role == "user", msg.Content.Type == assistant.ContentTurnStatus, msg.Content.Type == assistant.ContentReplayComplete:
			// The user's prompt is echoed live under another id.
		case key == "":
			got = append(got, head)
		default:
			got = append(got, head+":"+msg.MessageID)
		}
		if done(msg, replayed) {
			return errStopReading
		}
		return nil
	})
	if !errors.Is(err, errStopReading) {
		t.Fatalf("subscribe = %v", err)
	}
	return got
}

func turnStatus(msg assistant.Message) string {
	if msg.Content.TurnStatus == nil {
		return ""
	}
	return msg.Content.TurnStatus.Status
}

func saved(t *testing.T, f *Fake, id string) []assistant.Message {
	t.Helper()
	history, err := f.ConversationHistory(context.Background(), assistant.ConversationHistoryInput{ConversationID: id})
	if err != nil {
		t.Fatal(err)
	}
	return history.Data.Attributes.Messages
}

// What a subscription showed live is what a later subscription replays,
// message for message, apart from live-only markers and the early echo.
func TestSubscribeReplaysWhatLiveShowed(t *testing.T) {
	f := &Fake{}
	opts := assistant.SendOptions{ConversationID: "c"}
	if _, err := send(t, f, `say("first")`, opts); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	live := subscribeUntil(t, f, "c", func() {
		go func() { _, err := send(t, f, `say("second answer"); tool("search", {}, out="hit")`, opts); sent <- err }()
	}, func(msg assistant.Message, _ bool) bool { return turnStatus(msg) == "ended" })
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	replay := subscribeUntil(t, f, "c", nil, func(msg assistant.Message, _ bool) bool {
		return msg.Content.Type == assistant.ContentReplayComplete
	})
	// Live text arrives in fragments the saved copy keeps apart too, and
	// streamed tool input is never saved.
	keep := func(s []string) []string {
		return slices.Compact(slices.DeleteFunc(slices.Clone(s), func(s string) bool {
			return strings.HasPrefix(s, "started:") || strings.HasPrefix(s, "delta:")
		}))
	}
	if got, want := keep(replay), keep(live); !slices.Equal(got, want) {
		t.Fatalf("replay = %q\nlive  = %q", got, want)
	}
}

// An idle conversation replays without a running turn.
func TestIdleReplayHasNoTurnStatus(t *testing.T) {
	f := &Fake{}
	if _, err := send(t, f, `say("done")`, assistant.SendOptions{ConversationID: "c"}); err != nil {
		t.Fatal(err)
	}
	var statuses []string
	subscribeUntil(t, f, "c", nil, func(msg assistant.Message, _ bool) bool {
		if s := turnStatus(msg); s != "" {
			statuses = append(statuses, s)
		}
		return msg.Content.Type == assistant.ContentReplayComplete
	})
	if len(statuses) != 0 {
		t.Fatalf("idle replay carried turn status %q", statuses)
	}
}

// A dropped connection fails the send, but the turn finishes and is saved.
func TestDropKeepsTurnRunning(t *testing.T) {
	f := &Fake{}
	_, err := send(t, f, `say("before"); drop(); say("after")`, assistant.SendOptions{ConversationID: "c"})
	if !errors.Is(err, assistant.ErrStreamBroken) {
		t.Fatalf("send = %v, want a broken stream", err)
	}
	running := false
	subscribeUntil(t, f, "c", nil, func(msg assistant.Message, replayed bool) bool {
		switch turnStatus(msg) {
		case "started":
			running = true
		case "ended":
			return true
		}
		return replayed && !running
	})
	var texts []string
	for _, msg := range saved(t, f, "c") {
		if msg.Role == "assistant" && msg.Content.Type == assistant.ContentMarkdownFragment {
			texts = append(texts, msg.Content.TextBody())
		}
	}
	if joined := strings.Join(texts, ""); !strings.Contains(joined, "before") || !strings.Contains(joined, "after") {
		t.Fatalf("saved answer = %q, want both halves", joined)
	}
}

// A turn that pauses on client tools ends, so a prompt queued meanwhile runs
// first and the tool answers resume the paused turn after it.
func TestToolPauseLetsQueuedPromptRun(t *testing.T) {
	f := &Fake{}
	opts := assistant.SendOptions{ConversationID: "c"}
	if _, err := send(t, f, `send_from_elsewhere('say("other")'); r = call("x", {}); say("resumed " + r.status)`, opts); err != nil {
		t.Fatal(err)
	}
	var call string
	for _, msg := range saved(t, f, "c") {
		if msg.Content.Type == assistant.ContentClientToolCall {
			call = msg.Content.Tool.ToolCallID
		}
	}
	answer := assistant.ClientToolResponse{ToolCallID: call, Status: assistant.ToolStatusSuccess}
	if _, err := send(t, f, []assistant.ClientToolResponse{answer}, opts); err != nil {
		t.Fatal(err)
	}
	var order []string
	var last string
	for _, msg := range saved(t, f, "c") {
		switch {
		case msg.Content.Type == assistant.ContentClientToolResponse:
			order = append(order, "answer")
		case msg.Role == "assistant" && msg.Content.TextBody() != "":
			if n := len(order); n > 0 && msg.MessageID == last {
				order[n-1] += msg.Content.TextBody()
			} else {
				order = append(order, msg.Content.TextBody())
			}
		}
		last = msg.MessageID
	}
	if want := []string{"other", "answer", "resumed success"}; !slices.Equal(order, want) {
		t.Fatalf("saved order = %q, want %q", order, want)
	}
}
