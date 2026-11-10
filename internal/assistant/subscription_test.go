package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
)

const fullReplayLine = `{"data":{"type":"assistant-response","attributes":{"conversation_id":"one","structured_message":{"role":"assistant","message_id":"replay_complete","content":{"type":"replay_complete","replay_kind":"full","cursor":null}}}}}` + "\n\n"

func TestSubscriptionReplayCompletionTracksForegroundActivity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []string
		active bool
	}{
		{name: "idle"},
		{name: "active", prefix: []string{activityLine("one", "started")}, active: true},
		{name: "ended", prefix: []string{activityLine("one", "started"), activityLine("one", "ended")}},
		{name: "background", prefix: []string{`{"data":{"attributes":{"structured_message":{"role":"assistant","background_task_id":"task","content":{"type":"turn_status","status":"started"}}}}}` + "\n\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeStream(t, w, tc.prefix...)
				writeStream(t, w, textLine("one", "replayed answer"), fullReplayLine)
			})
			var ready, messages int
			err := c.SubscribeConversation(context.Background(), "one", func(a ConversationActivity) error {
				switch a.Kind {
				case ConversationActivityStarted, ConversationActivityEnded:
				case ConversationActivityReady:
					ready++
					if a.Active != tc.active || messages != 1 {
						t.Errorf("ready active=%v messages=%d, want active=%v after history", a.Active, messages, tc.active)
					}
				case ConversationActivityMessage:
					messages++
					if a.Message.Content.Type == ContentReplayComplete {
						t.Error("replay completion surfaced as conversation content")
					}
				}
				return nil
			})
			if !errors.Is(err, io.EOF) || ready != 1 || messages != 1 {
				t.Fatalf("err=%v ready=%d messages=%d", err, ready, messages)
			}
		})
	}
}

func TestSubscriptionDoesNotTreatUnknownReplayKindAsFullSnapshot(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w, `{"data":{"attributes":{"structured_message":{"role":"assistant","content":{"type":"replay_complete","replay_kind":"future-partial"}}}}}`+"\n\n")
	})
	err := c.SubscribeConversation(context.Background(), "one", func(a ConversationActivity) error {
		t.Errorf("unknown replay kind produced activity: %+v", a)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestSubscriptionTreatsMissingReplayKindAsFullSnapshot(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w, `{"data":{"attributes":{"structured_message":{"role":"assistant","content":{"type":"replay_complete"}}}}}`+"\n\n")
	})
	ready := 0
	err := c.SubscribeConversation(context.Background(), "one", func(a ConversationActivity) error {
		if a.Kind == ConversationActivityReady {
			ready++
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || ready != 1 {
		t.Fatalf("ready=%d err=%v", ready, err)
	}
}

func activityLine(id, status string) string {
	return fmt.Sprintf(`{"data":{"attributes":{"conversation_id":%q,"structured_message":{"message_id":"status","content":{"type":"turn_status","status":%q}}}}}`+"\n", id, status)
}

func TestConversationSubscriptionKeepsTaskHistoryWithoutForegroundActivity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w,
			`{"data":{"attributes":{"structured_message":{"message_id":"copied-task-boundary","background_task_id":"task","content":{"type":"markdown_fragment","content":"task output"}}}}}`+"\n",
			`{"data":{"attributes":{"structured_message":{"background_task_id":"task","content":{"type":"turn_status","status":"started"}}}}}`+"\n",
			fullReplayLine,
			`{"data":{"attributes":{"structured_message":{"background_task_id":"task","content":{"type":"turn_status","status":"ended"}}}}}`+"\n")
	})
	var messages, ready int
	err := c.SubscribeConversation(context.Background(), "one", func(a ConversationActivity) error {
		switch a.Kind {
		case ConversationActivityMessage:
			messages++
			if a.Message.MessageID != "copied-task-boundary" {
				t.Fatal("task boundary message was lost")
			}
		case ConversationActivityReady:
			ready++
			if a.Active {
				t.Fatal("background task made foreground active")
			}
		default:
			t.Fatalf("task produced foreground marker: %+v", a)
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || messages != 1 || ready != 1 {
		t.Fatalf("err=%v messages=%d ready=%d", err, messages, ready)
	}
}
