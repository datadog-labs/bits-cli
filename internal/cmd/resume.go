package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/resume"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

const (
	// ownedPageSize is the API's largest page of the account's conversations,
	// most recently updated first.
	ownedPageSize = 100
	// maxOwnedPages bounds the search when the most recently opened candidate
	// is missing, as when it was deleted, so a long history cannot stall startup.
	// NOTE: a conversation older than the first maxOwnedPages pages counts as
	// not owned.
	maxOwnedPages = 10
	// searchTimeout bounds the whole search: it runs before the chat draws,
	// so a slow server must not leave a blank terminal indefinitely.
	searchTimeout = 30 * time.Second

	// recordTimeout bounds one record so a stuck lock cannot hold up the chat's exit.
	recordTimeout = 2 * time.Second
)

// workspaceConversations connects a run to the workspace's resume history: it
// picks the conversation for --last and records each one the chat opens.
// Interactive chat alone records, so scripted runs never become what a user
// returns to.
type workspaceConversations struct {
	workspace *workspace.Workspace

	opening sync.Mutex
	opened  *resume.History // set once the workspace's root resolves

	mu        sync.Mutex
	closed    bool // wait has begun: an engine attached later is not followed
	recorders sync.WaitGroup
	err       error // the first record that failed
}

func newWorkspaceConversations(ws *workspace.Workspace) *workspaceConversations {
	return &workspaceConversations{workspace: ws}
}

// history opens the workspace's history on first use: finding its root runs
// Git, which a run that neither resumes nor records must not wait for. Only
// success is kept, so a Git that timed out once is asked again.
func (w *workspaceConversations) history() (resume.History, error) {
	w.opening.Lock()
	defer w.opening.Unlock()
	if w.opened != nil {
		return *w.opened, nil
	}
	root, err := w.workspace.Root(context.Background())
	if err != nil {
		return resume.History{}, err
	}
	history, err := resume.Open(root)
	if err != nil {
		return resume.History{}, err
	}
	w.opened = &history
	return history, nil
}

// recent returns the conversations last open in the workspace, most recent first.
func (w *workspaceConversations) recent() ([]string, error) {
	history, err := w.history()
	if err != nil {
		return nil, err
	}
	return history.Recent()
}

// start picks the conversation the flags ask for. With --last it reads the
// workspace's records now: they need no account, so a workspace with none
// fails before login.
func (w *workspaceConversations) start(opts ChatOptions) (conversationStart, error) {
	if !opts.Last {
		return conversationByID(opts.ConversationID), nil
	}
	history, err := w.history()
	if err != nil {
		return nil, fmt.Errorf("find the last conversation: %w", err)
	}
	candidates, err := history.Recent()
	if err != nil {
		return nil, fmt.Errorf("find the last conversation: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no conversation to resume in %s; run bits there to start one", history.Workspace())
	}
	return func(ctx context.Context, backend agent.Backend) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, searchTimeout)
		defer cancel()
		id, err := owned(ctx, backend, candidates)
		if err == nil && id == "" {
			err = fmt.Errorf("no conversation to resume in %s for the signed-in account; those opened there "+
				"were deleted, belong to another login, or are older than its %d most recently updated",
				history.Workspace(), ownedPageSize*maxOwnedPages)
		}
		return id, err
	}, nil
}

// owned returns the first of candidates the signed-in account owns, or "".
// The server's list is the authority: a conversation deleted since, or opened
// under another login, is skipped and never loaded. A failed read is an error,
// never an empty list.
func owned(ctx context.Context, backend agent.Backend, candidates []string) (string, error) {
	lister, ok := backend.(agent.ConversationListBackend)
	if !ok {
		return "", fmt.Errorf("find the last conversation: %w", agent.ErrConversationListUnsupported)
	}
	rank := make(map[string]int, len(candidates))
	for i, id := range candidates {
		rank[id] = i
	}
	best := len(candidates)
	in := assistant.UserConversationsInput{PageSize: ownedPageSize}
	for range maxOwnedPages {
		page := agent.ListConversations(ctx, lister, in)
		if page.Err != nil {
			return "", fmt.Errorf("find the last conversation: %w", page.Err)
		}
		for _, summary := range page.Conversations {
			if r, ok := rank[summary.ConversationID]; ok {
				best = min(best, r)
			}
		}
		// Only the most recent candidate ends the search early: a better one
		// than the best so far may still be on a later page.
		if best == 0 || page.NextPageToken == "" {
			break
		}
		in.PageToken = page.NextPageToken
	}
	if best == len(candidates) {
		return "", nil
	}
	return candidates[best], nil
}

// follow records each conversation engine confirms until it closes: the one it
// starts in once restored, each one it switches to, and the current one again
// once per turn, so the conversation in use stays the most recent. Records run
// here, off the engine's goroutine.
func (w *workspaceConversations) follow(engine *agent.Engine) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	events, unsubscribe := engine.Subscribe()
	w.recorders.Go(func() {
		defer unsubscribe()
		var lastRun agent.RunID
		var lastID string
		for event := range events {
			if event.Kind != agent.EventConversation || !agent.ValidConversationID(event.ConvID) {
				continue
			}
			// Every round of a turn confirms its id again; once per turn suffices.
			if event.Run == lastRun && event.ConvID == lastID {
				continue
			}
			lastRun, lastID = event.Run, event.ConvID
			history, err := w.history()
			if err != nil {
				// NOTE: a workspace whose root Git cannot resolve records
				// nothing. Only --last and /resume, which need the history,
				// report why; a chat that never asked for it stays quiet.
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
			err = history.Record(ctx, event.ConvID, time.Now())
			cancel()
			if err != nil {
				w.fail(err)
			}
		}
	})
}

func (w *workspaceConversations) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// wait stops following new engines and returns once every followed one has
// closed, with the first record that failed.
func (w *workspaceConversations) wait() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.recorders.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
