package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tools"
)

// client is the part of the connection a session calls.
type client interface {
	SessionUpdate(context.Context, acp.SessionNotification) error
	RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
	CreateElicitation(context.Context, acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error)
}

// Env is one session's local world: its engine and the tools it runs at the
// session's cwd. Close releases the workspace and stops its commands.
type Env struct {
	Engine *agent.Engine
	Tools  *agent.ToolSet
	Close  func()
}

// session pumps one engine's events to the client on one goroutine, so
// updates reach the client in the order the engine made them.
type session struct {
	id     acp.SessionId
	client client
	env    Env
	ui     *tools.UI // nil when the client cannot answer forms
	mirror *mirror
	log    *slog.Logger

	events <-chan agent.Event
	// waits asks the pump for a run's terminal event, which it hands over
	// after the updates before it.
	waits chan waiter
	// syncs asks the pump to answer once it has sent a state version.
	syncs chan syncer
	done  chan struct{} // closed once the pump returns
	// turn holds the session's one prompt. The SDK cancels a running prompt
	// when the next one arrives, so the next waits for it to wind down.
	turn chan struct{}
}

// syncer is closed once the pump has sent state version, or a newer one.
type syncer struct {
	version uint64
	done    chan struct{}
}

// waiter receives its run's terminal event on end, which holds one.
type waiter struct {
	run agent.RunID
	end chan agent.Event
}

func newSession(id acp.SessionId, c client, env Env, ui *tools.UI, root string, log *slog.Logger) *session {
	// Engine.Close ends the subscription, so its stop function is unused.
	events, _ := env.Engine.Subscribe()
	s := &session{
		id: id, client: c, env: env, ui: ui, mirror: newMirror(root), log: log,
		events: events, waits: make(chan waiter), syncs: make(chan syncer), done: make(chan struct{}), turn: make(chan struct{}, 1),
	}
	go s.pump()
	return s
}

// pump sends every state change, asks about every parked call, and presents
// every tool UI request, until the subscription ends.
func (s *session) pump() {
	defer close(s.done)
	asks := make(map[string]context.CancelFunc) // parked call id -> its request
	defer func() {
		for _, cancel := range asks {
			cancel()
		}
	}()
	// A run can end before its request asks for the end, so the ends no
	// one asked for yet are kept. Every run is a request's, so both stay small.
	ended := make(map[agent.RunID]agent.Event)
	waiting := make(map[agent.RunID]chan agent.Event)
	var syncing []syncer
	synced := func() {
		kept := syncing[:0]
		for _, w := range syncing {
			if w.version <= s.mirror.version {
				close(w.done)
			} else {
				kept = append(kept, w)
			}
		}
		syncing = kept
	}
	var requests <-chan *tools.Request
	if s.ui != nil {
		requests = s.ui.Requests()
	}
	// held are the questions whose card the client has yet to be sent: a
	// form must follow its card, and the state that adds it may be queued.
	// NOTE: no test covers the hold: the fake backend always delivers the
	// card's state first, so only this pump's order guarantees it.
	var held []*tools.Request
	present := func() {
		kept := held[:0]
		for _, request := range held {
			switch {
			case request.Context().Err() != nil: // the call ended unanswered
			case s.mirror.shown(request.Call.ID):
				go s.question(request)
			default:
				kept = append(kept, request)
			}
		}
		held = kept
	}
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}
			switch ev.Kind {
			case agent.EventState:
				s.send(s.mirror.updates(ev.State))
				s.ask(ev.State, asks)
				present()
				synced()
			case agent.EventUsage:
				if ev.Usage != nil {
					s.send([]acp.SessionUpdate{{UsageUpdate: &acp.SessionUsageUpdate{Used: int64(ev.Usage.TokensUsed), Size: int64(ev.Usage.MaxTokens)}}})
				}
			case agent.EventConversation:
			case agent.EventDone, agent.EventError, agent.EventCanceled:
				if end, ok := waiting[ev.Run]; ok {
					delete(waiting, ev.Run)
					end <- ev
				} else {
					ended[ev.Run] = ev
				}
			}
		case w := <-s.waits:
			if ev, ok := ended[w.run]; ok {
				delete(ended, w.run)
				w.end <- ev
			} else {
				waiting[w.run] = w.end
			}
		case w := <-s.syncs:
			syncing = append(syncing, w)
			synced()
		case request := <-requests:
			held = append(held, request)
			present()
		}
	}
}

func (s *session) send(updates []acp.SessionUpdate) {
	for _, update := range updates {
		// A failed write means the client is gone; the connection's end
		// closes the session.
		if err := s.client.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: s.id, Update: update}); err != nil {
			s.log.Debug("session update", "session", s.id, "err", err)
		}
	}
}

// ask starts one permission request per newly parked call, and withdraws
// the request of a call the engine settled another way: a grant, a mode
// switch, a stop, or the turn's end.
func (s *session) ask(state agent.State, asks map[string]context.CancelFunc) {
	parked := make(map[string]bool)
	for _, block := range state.PendingApprovals() {
		id := block.ToolCallID()
		parked[id] = true
		if asks[id] != nil {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		asks[id] = cancel
		go s.permission(ctx, block)
	}
	for id, cancel := range asks {
		if !parked[id] {
			cancel()
			delete(asks, id)
		}
	}
}

const (
	optionAllowOnce   acp.PermissionOptionId = "allow_once"
	optionAllowAlways acp.PermissionOptionId = "allow_always"
	optionReject      acp.PermissionOptionId = "reject_once"
)

// The labels are the TUI's: a session grant covers the call's approval key,
// such as every workspace write, not only this call.
var permissionOptions = []acp.PermissionOption{
	{OptionId: optionAllowOnce, Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow"},
	{OptionId: optionAllowAlways, Kind: acp.PermissionOptionKindAllowAlways, Name: "Allow for session"},
	{OptionId: optionReject, Kind: acp.PermissionOptionKindRejectOnce, Name: "Deny"},
}

// permission asks the client about one parked call and applies the answer.
// Anything but an allow denies: the user did not approve the call.
func (s *session) permission(ctx context.Context, block agent.Block) {
	id := block.ToolCallID()
	call := acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(id)}
	if block.Tool.Approval != nil {
		call.Title = acp.PatchSet(block.Tool.Approval.Title)
	}
	response, err := s.client.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: s.id, ToolCall: call, Options: permissionOptions})
	if ctx.Err() != nil {
		return // settled otherwise; the client was told to drop the prompt
	}
	choice := optionReject
	switch {
	case err != nil:
		s.log.Debug("permission request", "session", s.id, "call", id, "err", err)
	case response.Outcome.Selected != nil:
		choice = response.Outcome.Selected.OptionId
	}
	switch choice {
	case optionAllowOnce:
		err = s.env.Engine.Decide(id, agent.ApprovalAllowOnce)
	case optionAllowAlways:
		err = s.env.Engine.GrantSessionFor(id)
	default:
		err = s.env.Engine.Decide(id, agent.ApprovalDeny)
	}
	// A stale call was settled otherwise, such as by a grant.
	if err != nil && !errors.Is(err, agent.ErrStale) {
		s.log.Debug("permission", "session", s.id, "call", id, "err", err)
	}
}

// prompt runs one turn and returns once the client has every update it made.
func (s *session) prompt(ctx context.Context, in agent.TurnInput) (acp.StopReason, error) {
	select {
	case s.turn <- struct{}{}:
		defer func() { <-s.turn }()
	case <-ctx.Done():
		return acp.StopReasonCancelled, nil
	}
	run, err := s.env.Engine.StartTurn(ctx, in)
	if err != nil {
		return "", err
	}
	end := s.wait(run)
	switch end.Kind {
	case agent.EventCanceled:
		return acp.StopReasonCancelled, nil
	case agent.EventError:
		if errors.Is(end.Err, agent.ErrMaxTurns) {
			return acp.StopReasonMaxTurnRequests, nil
		}
		return "", end.Err
	case agent.EventDone, agent.EventState, agent.EventUsage, agent.EventConversation:
	}
	return acp.StopReasonEndTurn, nil
}

// synced returns once the client has been sent state version, or a newer
// one, or the session closed.
func (s *session) synced(version uint64) {
	w := syncer{version: version, done: make(chan struct{})}
	select {
	case s.syncs <- w:
	case <-s.done:
		return
	}
	select {
	case <-w.done:
	case <-s.done:
	}
}

// wait returns run's terminal event, or an error once the session closed.
func (s *session) wait(run agent.RunID) agent.Event {
	w := waiter{run: run, end: make(chan agent.Event, 1)}
	select {
	case s.waits <- w:
	case <-s.done:
		return s.closed(run)
	}
	select {
	case ev := <-w.end:
		return ev
	case <-s.done:
	}
	// The pump may have handed the end over just before it returned.
	select {
	case ev := <-w.end:
		return ev
	default:
		return s.closed(run)
	}
}

func (s *session) closed(run agent.RunID) agent.Event {
	return agent.Event{Kind: agent.EventError, Run: run, Err: fmt.Errorf("session %s closed", s.id)}
}

// close ends the session. Only the request that took it out of the agent's
// table calls it, so it runs once.
func (s *session) close() {
	s.env.Engine.Close() // ends the subscription after its events, so the pump returns
	<-s.done
	s.env.Close()
}
