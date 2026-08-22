package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func TestNoticeForError(t *testing.T) {
	cases := []struct {
		name      string
		op        string
		err       error
		wantLevel chat.NoticeLevel
		wantText  string // exact match, unless wantSubstr is set
		substr    bool
	}{
		{
			name:      "reauth required is actionable",
			err:       fmt.Errorf("token: %w", auth.ErrReauthRequired),
			wantLevel: chat.NoticeError,
			wantText:  "Your Datadog login expired. Run `bits login` again.",
		},
		{
			name:      "nondurable refresh is actionable",
			err:       fmt.Errorf("token: %w", auth.ErrSessionNotDurable),
			wantLevel: chat.NoticeError,
			wantText:  "The refreshed login could not be secured. Try again; if this continues, run `bits login`.",
		},
		{
			name:      "replaced session asks for restart",
			err:       fmt.Errorf("token: %w", auth.ErrSessionReplaced),
			wantLevel: chat.NoticeWarn,
			wantText:  "The active Datadog login changed. Restart Bits to use it.",
		},
		{
			name:      "api error maps through its status sentinel",
			err:       &assistant.APIError{StatusCode: 401},
			wantLevel: chat.NoticeError,
			wantText:  "Not authenticated. Run `bits login` again, or refresh your Datadog developer credentials.",
		},
		{
			name:      "not found without an input stays generic",
			err:       &assistant.APIError{StatusCode: 404},
			wantLevel: chat.NoticeError,
			wantText:  "That conversation could not be found.",
		},
		{
			name:      "not found opening a conversation",
			err:       &assistant.APIError{StatusCode: 404, Input: assistant.ConversationHistoryInput{ConversationID: "conv-1"}},
			wantLevel: chat.NoticeError,
			wantText:  "Couldn't open conversation \"conv-1\". It was not found.",
		},
		{
			name:      "not found deleting is a benign warning",
			err:       &assistant.APIError{StatusCode: 404, Input: assistant.DeleteConversationInput{ConversationID: "conv-1"}},
			wantLevel: chat.NoticeWarn,
			wantText:  "Conversation \"conv-1\" is already gone.",
		},
		{
			name:      "not found renaming names the operation",
			err:       &assistant.APIError{StatusCode: 404, Input: assistant.RenameConversationInput{ConversationID: "conv-1", Title: "x"}},
			wantLevel: chat.NoticeError,
			wantText:  "Couldn't rename conversation \"conv-1\". It was not found.",
		},
		{
			name:      "not found sharing names the operation",
			err:       &assistant.APIError{StatusCode: 404, Input: assistant.ShareConversationInput{ConversationID: "conv-1", Shared: true}},
			wantLevel: chat.NoticeError,
			wantText:  "Couldn't share conversation \"conv-1\". It was not found.",
		},
		{
			name:      "forbidden without an input stays generic",
			err:       &assistant.APIError{StatusCode: 403},
			wantLevel: chat.NoticeError,
			wantText:  "Your account doesn't have access to this.",
		},
		{
			name:      "forbidden opening a conversation names it",
			err:       &assistant.APIError{StatusCode: 403, Input: assistant.ConversationHistoryInput{ConversationID: "conv-1"}},
			wantLevel: chat.NoticeError,
			wantText:  "You don't have access to conversation \"conv-1\".",
		},
		{
			name:      "forbidden sharing names the operation",
			err:       &assistant.APIError{StatusCode: 403, Input: assistant.ShareConversationInput{ConversationID: "conv-1", Shared: true}},
			wantLevel: chat.NoticeError,
			wantText:  "You don't have permission to share conversation \"conv-1\".",
		},
		{
			name:      "bad request renaming hints at the title constraint",
			err:       &assistant.APIError{StatusCode: 400, Input: assistant.RenameConversationInput{ConversationID: "conv-1", Title: ""}},
			wantLevel: chat.NoticeError,
			wantText:  "Couldn't rename the conversation.",
		},
		{
			name:      "bad request sharing hints at the expiry constraint",
			err:       &assistant.APIError{StatusCode: 400, Input: assistant.ShareConversationInput{ConversationID: "conv-1", Shared: true}},
			wantLevel: chat.NoticeError,
			wantText:  "Couldn't share the conversation.",
		},
		{
			name:      "bad request without a known input stays generic",
			err:       &assistant.APIError{StatusCode: 400},
			wantLevel: chat.NoticeError,
			wantText:  "The assistant rejected the request as invalid.",
		},
		{
			name:      "rate limited is a warning",
			err:       &assistant.APIError{StatusCode: 429},
			wantLevel: chat.NoticeWarn,
			wantText:  "Rate limited by the assistant. Give it a moment and try again.",
		},
		{
			name:      "gateway timeout is a timeout, not a generic server error",
			err:       &assistant.APIError{StatusCode: 504},
			wantLevel: chat.NoticeWarn,
			wantText:  "The assistant took too long to respond. Try again.",
		},
		{
			name:      "generic server error",
			err:       &assistant.APIError{StatusCode: 500},
			wantLevel: chat.NoticeError,
			wantText:  "The assistant service is having trouble. Try again shortly.",
		},
		{
			name:      "context deadline is a timeout",
			err:       context.DeadlineExceeded,
			wantLevel: chat.NoticeWarn,
			wantText:  "The assistant took too long to respond. Try again.",
		},
		{
			name:      "history unsupported is a warning",
			err:       agent.ErrHistoryUnsupported,
			wantLevel: chat.NoticeWarn,
			wantText:  "This backend can't restore past conversations.",
		},
		{
			name:      "max turns",
			err:       agent.ErrMaxTurns,
			wantLevel: chat.NoticeError,
			wantText:  "The assistant kept working without finishing, so it was stopped at the turn limit.",
		},
		{
			name:      "unknown error falls back to its own text with the op prefix",
			op:        "restore failed",
			err:       errors.New("boom"),
			wantLevel: chat.NoticeError,
			wantText:  "restore failed: boom",
		},
		{
			name:      "unknown error without op keeps the raw text",
			err:       errors.New("boom"),
			wantLevel: chat.NoticeError,
			wantText:  "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := noticeForError(tc.op, tc.err)
			if got.Level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got.Level, tc.wantLevel)
			}
			// The original error is always embedded so raw detail stays printable.
			if !errors.Is(got.Err, tc.err) {
				t.Errorf("Err = %v, want it to wrap %v", got.Err, tc.err)
			}
			if tc.substr {
				if !strings.Contains(got.Text, tc.wantText) {
					t.Errorf("text = %q, want it to contain %q", got.Text, tc.wantText)
				}
			} else if got.Text != tc.wantText {
				t.Errorf("text = %q, want %q", got.Text, tc.wantText)
			}
		})
	}
}

// A nil error yields the empty notice (nothing to show).
func TestNoticeForErrorNil(t *testing.T) {
	if n := noticeForError("op", nil); !n.Empty() {
		t.Errorf("noticeForError(nil) = %+v, want empty", n)
	}
}
