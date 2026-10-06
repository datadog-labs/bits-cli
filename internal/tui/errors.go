package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// noticeForError turns a low-level error into a user-facing notice.
func noticeForError(op string, err error) chat.Notice {
	if err == nil {
		return chat.Notice{}
	}
	if authNotice, ok := noticeForAuthError(err); ok {
		return authNotice
	}
	apiErr, ok := errors.AsType[*assistant.APIError](err)
	if !ok {
		return noticeForNonAPIError(op, err)
	}
	return noticeForAPIError(apiErr)
}

// noticeForAuthError takes precedence when a server response is joined with a
// credential-state failure, preserving the original joined error for details.
func noticeForAuthError(err error) (chat.Notice, bool) {
	switch {
	case errors.Is(err, auth.ErrSessionCorrupt):
		return notice(chat.NoticeError, err, "The stored Datadog login is unreadable. Run `bits logout`, then `bits login`."), true
	case errors.Is(err, auth.ErrReauthRequired):
		return notice(chat.NoticeError, err, "Your Datadog login expired. Run `bits login` again."), true
	case errors.Is(err, auth.ErrSessionNotDurable):
		return notice(chat.NoticeError, err, "The refreshed login could not be secured. Try again; if this continues, run `bits login`."), true
	case errors.Is(err, auth.ErrSessionReplaced):
		return notice(chat.NoticeWarn, err, "The active Datadog login changed. Restart Bits to use it."), true
	case errors.Is(err, auth.ErrSessionUnlock):
		return notice(chat.NoticeError, err, "The Datadog login state could not be confirmed because its process lock could not be released. Restart Bits before continuing."), true
	}
	return chat.Notice{}, false
}

// noticeForNonAPIError covers errors that never carry an HTTP status.
func noticeForNonAPIError(op string, err error) chat.Notice {
	_, isNet := errors.AsType[net.Error](err)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return notice(chat.NoticeWarn, err, "The assistant took too long to respond. Try again.")
	case isNet:
		return notice(chat.NoticeError, err, "Can't reach the assistant service. Check your network and Datadog site.")
	case errors.Is(err, agent.ErrMaxTurns):
		return notice(chat.NoticeError, err, "The assistant kept working without finishing, so it was stopped at the turn limit.")
	case errors.Is(err, agent.ErrHistoryUnsupported):
		return notice(chat.NoticeWarn, err, "This backend can't restore past conversations.")
	}

	text := err.Error()
	if op != "" {
		text = op + ": " + text
	}
	return notice(chat.NoticeError, err, "%s", text)
}

func noticeForAPIError(apiErr *assistant.APIError) chat.Notice {
	switch {
	case errors.Is(apiErr, assistant.ErrUnauthorized):
		return notice(chat.NoticeError, apiErr, "Not authenticated. Run `bits login` again, or refresh your Datadog developer credentials.")
	case errors.Is(apiErr, assistant.ErrForbidden):
		return forbiddenNotice(apiErr)
	case errors.Is(apiErr, assistant.ErrNotFound):
		return notFoundNotice(apiErr)
	case errors.Is(apiErr, assistant.ErrRateLimited):
		return notice(chat.NoticeWarn, apiErr, "Rate limited by the assistant. Give it a moment and try again.")
	case errors.Is(apiErr, assistant.ErrBadRequest):
		return badRequestNotice(apiErr)
	case apiErr.StatusCode == http.StatusGatewayTimeout:
		return notice(chat.NoticeWarn, apiErr, "The assistant took too long to respond. Try again.")
	case errors.Is(apiErr, assistant.ErrServer):
		return notice(chat.NoticeError, apiErr, "The assistant service is having trouble. Try again shortly.")
	}
	return notice(chat.NoticeError, apiErr, "%s", apiErr.Error())
}

func notFoundNotice(apiErr *assistant.APIError) chat.Notice {
	switch in := apiErr.Input.(type) {
	case assistant.ConversationHistoryInput:
		return notice(chat.NoticeError, apiErr, "Couldn't open conversation %s. It was not found.", shortID(in.ConversationID))
	case assistant.DeleteConversationInput:
		return notice(chat.NoticeWarn, apiErr, "Conversation %s is already gone.", shortID(in.ConversationID))
	case assistant.RenameConversationInput:
		return notice(chat.NoticeError, apiErr, "Couldn't rename conversation %s. It was not found.", shortID(in.ConversationID))
	case assistant.ShareConversationInput:
		return notice(chat.NoticeError, apiErr, "Couldn't share conversation %s. It was not found.", shortID(in.ConversationID))
	}
	return notice(chat.NoticeError, apiErr, "That conversation could not be found.")
}

func forbiddenNotice(apiErr *assistant.APIError) chat.Notice {
	switch in := apiErr.Input.(type) {
	case assistant.ConversationHistoryInput:
		return notice(chat.NoticeError, apiErr, "You don't have access to conversation %s.", shortID(in.ConversationID))
	case assistant.DeleteConversationInput:
		return notice(chat.NoticeError, apiErr, "You don't have permission to delete conversation %s.", shortID(in.ConversationID))
	case assistant.RenameConversationInput:
		return notice(chat.NoticeError, apiErr, "You don't have permission to rename conversation %s.", shortID(in.ConversationID))
	case assistant.ShareConversationInput:
		return notice(chat.NoticeError, apiErr, "You don't have permission to share conversation %s.", shortID(in.ConversationID))
	}
	return notice(chat.NoticeError, apiErr, "Your account doesn't have access to this.")
}

func badRequestNotice(apiErr *assistant.APIError) chat.Notice {
	switch in := apiErr.Input.(type) {
	case assistant.ConversationHistoryInput:
		return notice(chat.NoticeError, apiErr, "Could not load the requested conversation %s. It may be invalid.", shortID(in.ConversationID))
	case assistant.RenameConversationInput:
		return notice(chat.NoticeError, apiErr, "Couldn't rename the conversation.")
	case assistant.ShareConversationInput:
		return notice(chat.NoticeError, apiErr, "Couldn't share the conversation.")
	}
	return notice(chat.NoticeError, apiErr, "The assistant rejected the request as invalid.")
}

func shortID(s string) string {
	const max = 12
	if len(s) > max {
		return fmt.Sprintf("%q", s[:max]+"…")
	}
	return fmt.Sprintf("%q", s)
}

func notice(level chat.NoticeLevel, err error, format string, args ...any) chat.Notice {
	return chat.Notice{Level: level, Text: fmt.Sprintf(format, args...), Err: err}
}
