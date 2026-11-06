package assistant

import (
	"context"
	"io"
)

type ConversationActivityKind uint8

const (
	ConversationActivityReady ConversationActivityKind = iota + 1
	ConversationActivityStarted
	ConversationActivityEnded
	ConversationActivityMessage
)

type ConversationActivity struct {
	Kind    ConversationActivityKind
	Active  bool
	Message Message
}

func (c *Client) SubscribeConversation(ctx context.Context, id string, fn func(ConversationActivity) error) error {
	emit := func(a ConversationActivity) error {
		if fn != nil {
			return fn(a)
		}
		return nil
	}
	active := false
	err := c.Subscribe(ctx, id, SubscribeOptions{}, func(msg Message) error {
		// Tasks have their own observers and must not own the foreground prompt.
		if msg.BackgroundTaskID != nil || msg.Content.Progress != nil {
			if msg.Content.TurnStatus != nil || msg.Content.Type == ContentReplayComplete {
				return nil
			}
			// Keep task history so copied task output and fork boundaries can
			// be restored. Live task rendering remains with task observers.
			return emit(ConversationActivity{Kind: ConversationActivityMessage, Message: msg})
		}
		switch {
		case msg.Content.Type == ContentReplayComplete:
			if p := msg.Content.ReplayComplete; p != nil && p.ReplayKind == "full" {
				return emit(ConversationActivity{Kind: ConversationActivityReady, Active: active})
			}
		case msg.Content.TurnStatus != nil:
			switch msg.Content.TurnStatus.Status {
			case "started":
				active = true
				return emit(ConversationActivity{Kind: ConversationActivityStarted})
			case "ended":
				active = false
				return emit(ConversationActivity{Kind: ConversationActivityEnded})
			}
		default:
			return emit(ConversationActivity{Kind: ConversationActivityMessage, Message: msg})
		}
		return nil
	})
	if err == nil {
		return io.EOF
	}
	return err
}
