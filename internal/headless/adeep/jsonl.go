// Package adeep implements the versioned bits.delivery.adeep JSONL delivery.
// It emits semantic run, round, conversation, tool, and usage records while
// keeping assistant Markdown exclusively in the terminal run.finished record.
package adeep

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/headless"
)

const (
	Schema  = "bits.delivery.adeep"
	Version = 1
)

const (
	typeRunStarted          = "run.started"
	typeConversationUpdated = "conversation.updated"
	typeRoundStarted        = "round.started"
	typeToolCall            = "tool.call"
	typeToolResult          = "tool.result"
	typeUsage               = "usage"
	typeRoundFinished       = "round.finished"
	typeRunFinished         = "run.finished"
)

type envelope struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Type    string `json:"type"`
	Round   int    `json:"round,omitempty"`
}

func env(typ string, round int) envelope {
	return envelope{Schema: Schema, Version: Version, Type: typ, Round: round}
}

type roundRecord struct{ envelope }

type runStartedRecord struct {
	envelope
	RequestedModel string `json:"requested_model,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
}

type conversationUpdatedRecord struct {
	envelope
	ConversationID string `json:"conversation_id"`
}

type toolCallRecord struct {
	envelope
	Tool toolCall `json:"tool"`
}

type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Side      string `json:"side"`
	Arguments string `json:"arguments"`
}

type toolResultRecord struct {
	envelope
	Tool toolResult `json:"tool"`
}

type toolResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result string `json:"result"`
}

type usageRecord struct {
	envelope
	Usage usage `json:"usage"`
}

type usage struct {
	TokensUsed   int  `json:"tokens_used"`
	MaxTokens    int  `json:"max_tokens"`
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}

type runError struct {
	Message string `json:"message"`
}

type runFinishedRecord struct {
	envelope
	Outcome        string    `json:"outcome"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Response       string    `json:"response,omitempty"`
	Rounds         int       `json:"rounds"`
	EndedAt        string    `json:"ended_at,omitempty"`
	Error          *runError `json:"error,omitempty"`
}

type toolState struct {
	call          toolCall
	round         int
	callEmitted   bool
	resultEmitted bool
}

// Delivery writes one ADEEP JSON object per line.
type Delivery struct {
	w io.Writer

	round          int
	rounds         int
	conversationID string
	started        bool
	finished       bool
	writeErr       error

	tools            map[string]*toolState
	toolOrder        []string
	responseBlockIDs map[agent.BlockID]struct{}
	lastRev          map[agent.BlockID]int
}

var _ headless.Delivery = (*Delivery)(nil)

func New(w io.Writer) *Delivery {
	return &Delivery{
		w:                w,
		tools:            make(map[string]*toolState),
		responseBlockIDs: make(map[agent.BlockID]struct{}),
		lastRev:          make(map[agent.BlockID]int),
	}
}

// Start emits run.started and opens model round 1.
func (d *Delivery) Start(s headless.Start) error {
	if d.started {
		return errors.New("ADEEP delivery already started")
	}
	startedAt := ""
	if !s.StartedAt.IsZero() {
		startedAt = s.StartedAt.Format(time.RFC3339Nano)
	}
	if err := d.write(runStartedRecord{
		envelope:       env(typeRunStarted, 0),
		RequestedModel: s.RequestedModel,
		StartedAt:      startedAt,
	}); err != nil {
		return err
	}
	d.started = true
	// Rounds open lazily in setRound when the first event of a backend send
	// arrives, so a turn rejected before any send reports no round.
	return nil
}

// Consume maps one ordered engine event to zero or more delivery records.
func (d *Delivery) Consume(ev agent.Event) error {
	if !d.started {
		return errors.New("ADEEP delivery not started")
	}
	if d.finished {
		return errors.New("ADEEP delivery already finished")
	}
	if d.writeErr != nil {
		return d.writeErr
	}
	// The engine's local echo of the user's text precedes any backend send;
	// it must not open a delivery round on its own.
	if ev.Kind != agent.EventTranscript || ev.Origin != agent.TranscriptOriginLocal {
		if err := d.setRound(ev.Round); err != nil {
			return err
		}
	}
	switch ev.Kind {
	case agent.EventTranscript:
		// A local echo establishes the run boundary. Its full snapshot can
		// include history restored before this turn, which must not produce
		// delivery records or become part of this run's response. Remember its
		// revisions so the first remote snapshot emits only new changes.
		if ev.Origin == agent.TranscriptOriginLocal {
			for _, block := range ev.Transcript.Blocks {
				d.lastRev[block.ID] = block.Rev
			}
			return nil
		}
		for _, block := range ev.Transcript.Blocks {
			if rev, seen := d.lastRev[block.ID]; seen && rev == block.Rev {
				continue
			}
			d.lastRev[block.ID] = block.Rev
			if block.Kind == assistant.KindText && block.Role == assistant.RoleAssistant {
				d.responseBlockIDs[block.ID] = struct{}{}
			}
			if block.Tool != nil && block.ToolCallID() != "" {
				if err := d.consumeTool(block, ev.Round); err != nil {
					return err
				}
			}
		}
	case agent.EventUsage:
		return d.write(usageRecord{envelope: env(typeUsage, ev.Round), Usage: usageOf(ev.Usage)})
	case agent.EventConversation:
		if ev.ConvID == "" {
			return nil
		}
		d.conversationID = ev.ConvID
		return d.write(conversationUpdatedRecord{envelope: env(typeConversationUpdated, ev.Round), ConversationID: ev.ConvID})
	case agent.EventTurnDone, agent.EventError:
		// Finish owns the sole terminal record.
	}
	return nil
}

// consumeTool emits each call and terminal result once. Calls whose arguments
// have not arrived yet are held so the exact eventual string is preserved. A
// call that remains unresolved is flushed by Finish without inventing a result.
func (d *Delivery) consumeTool(block agent.Block, round int) error {
	id := block.ToolCallID()
	state := d.tools[id]
	if state == nil {
		state = &toolState{round: round}
		d.tools[id] = state
		d.toolOrder = append(d.toolOrder, id)
	}
	state.call = toolCall{
		ID:        id,
		Name:      block.Tool.Name,
		Side:      toolSide(block.Tool.IsClientSide),
		Arguments: block.Tool.Input,
	}
	status, terminal := toolStatus(block.Tool)
	if !state.callEmitted && (state.call.Arguments != "" || terminal) {
		if err := d.emitToolCall(state); err != nil {
			return err
		}
	}
	if state.resultEmitted || !terminal {
		return nil
	}
	if err := d.write(toolResultRecord{
		envelope: env(typeToolResult, round),
		Tool:     toolResult{ID: id, Status: status, Result: block.Tool.Output},
	}); err != nil {
		return err
	}
	state.resultEmitted = true
	return nil
}

func (d *Delivery) emitToolCall(state *toolState) error {
	if state.callEmitted {
		return nil
	}
	if err := d.write(toolCallRecord{envelope: env(typeToolCall, state.round), Tool: state.call}); err != nil {
		return err
	}
	state.callEmitted = true
	return nil
}

// Finish closes pending calls and the current round, then emits exactly one
// terminal record. Assistant text is never streamed; only the final response
// is included here.
func (d *Delivery) Finish(f headless.Finish) error {
	if !d.started {
		return errors.New("ADEEP delivery not started")
	}
	if d.finished {
		return errors.New("ADEEP delivery already finished")
	}
	if d.writeErr != nil {
		return d.writeErr
	}
	d.finished = true

	for _, id := range d.toolOrder {
		if err := d.emitToolCall(d.tools[id]); err != nil {
			return err
		}
	}
	if d.round > 0 {
		if err := d.write(roundRecord{envelope: env(typeRoundFinished, d.round)}); err != nil {
			return err
		}
	}

	outcome := headless.ClassifyTurn(f.Result)
	conversationID := f.Result.ConversationID
	if conversationID == "" {
		conversationID = d.conversationID
	}
	endedAt := ""
	if !f.EndedAt.IsZero() {
		endedAt = f.EndedAt.Format(time.RFC3339Nano)
	}
	record := runFinishedRecord{
		envelope:       env(typeRunFinished, 0),
		Outcome:        string(outcome),
		ConversationID: conversationID,
		Response:       assistantResponse(f.Result.Blocks, d.responseBlockIDs),
		Rounds:         d.rounds,
		EndedAt:        endedAt,
	}
	if outcome != headless.OutcomeCompleted && outcome != headless.OutcomeApprovalDenied {
		message := "the turn ended without a completion"
		if f.Err != nil {
			message = f.Err.Error()
		}
		record.Error = &runError{Message: message}
	}
	return d.write(record)
}

func (d *Delivery) setRound(round int) error {
	if round <= 0 || round == d.round {
		return nil
	}
	if round < d.round {
		return fmt.Errorf("delivery round moved backwards from %d to %d", d.round, round)
	}
	for d.round < round {
		if d.round > 0 {
			if err := d.write(roundRecord{envelope: env(typeRoundFinished, d.round)}); err != nil {
				return err
			}
		}
		d.round++
		d.rounds++
		if err := d.write(roundRecord{envelope: env(typeRoundStarted, d.round)}); err != nil {
			return err
		}
	}
	return nil
}

func toolSide(clientSide bool) string {
	if clientSide {
		return "client"
	}
	return "server"
}

func toolStatus(tool *agent.ToolBlock) (string, bool) {
	switch tool.Status {
	case agent.ToolSuccess:
		return "success", true
	case agent.ToolError:
		return "error", true
	case agent.ToolDenied:
		return "denied", true
	case agent.ToolCancelled:
		return "cancelled", true
	case agent.ToolUnknown, agent.ToolRunning, agent.ToolAwaitingApproval:
		return "", false
	}
	return "", false
}

func assistantResponse(blocks []agent.Block, included map[agent.BlockID]struct{}) string {
	parts := make([]string, 0, len(included))
	for _, block := range blocks {
		if _, ok := included[block.ID]; !ok {
			continue
		}
		if block.Kind == assistant.KindText && block.Role == assistant.RoleAssistant &&
			block.Markdown != nil && block.Markdown.Content != "" {
			parts = append(parts, block.Markdown.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func usageOf(u *assistant.Usage) usage {
	if u == nil {
		return usage{}
	}
	return usage{
		TokensUsed:   u.TokensUsed,
		MaxTokens:    u.MaxTokens,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
	}
}

func (d *Delivery) write(record any) error {
	if d.writeErr != nil {
		return d.writeErr
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal delivery record: %w", err)
	}
	line = append(line, '\n')
	n, err := d.w.Write(line)
	if err != nil {
		d.writeErr = fmt.Errorf("write delivery record: %w", err)
		return d.writeErr
	}
	if n != len(line) {
		d.writeErr = fmt.Errorf("write delivery record: %w", io.ErrShortWrite)
		return d.writeErr
	}
	return nil
}
