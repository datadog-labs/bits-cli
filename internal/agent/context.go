package agent

import (
	"context"
	"fmt"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// turnRequest is one backend request of a turn: the user's message, or the
// answers to a round of client tool calls.
type turnRequest struct {
	user  *userMessage
	tools []assistant.ClientToolResponse
}

// userMessage holds what is sent with the user's text and never with tool
// answers.
type userMessage struct {
	text         string
	context      *assistant.AssistantContext
	userContext  string
	skillContext string
}

func (r turnRequest) payload() any {
	if r.user != nil {
		return r.user.text
	}
	return r.tools
}

// sendOptions describes one request. Client tools ride on every request
// because the backend does not retain them. Conversation-level deliveries,
// such as project instructions, are added by the caller that acknowledges them.
func (e *Engine) sendOptions(tools []assistant.ClientTool, req turnRequest) assistant.SendOptions {
	opts := e.opts
	opts.ClientTools = tools
	if req.user != nil {
		opts.Context = req.user.context
		opts.CustomUserContext = req.user.userContext
	}
	return opts
}

type instructionsPhase uint8

const (
	instructionsUnprepared instructionsPhase = iota
	instructionsPending
	instructionsDelivered
)

// instructionDelivery tracks instruction delivery for one conversation.
// Its engine serializes access.
type instructionDelivery struct {
	source         *env.Instructions
	phase          instructionsPhase
	pendingContext string
}

// apply prepares an update once per conversation. The exact update stays pending
// until delivery, even if a failed request assigns a conversation ID.
func (p *instructionDelivery) apply(ctx context.Context, opts assistant.SendOptions) assistant.SendOptions {
	if p == nil || p.phase == instructionsDelivered {
		return opts
	}
	if p.phase == instructionsUnprepared {
		current := p.source.Snapshot(ctx)
		if ctx.Err() != nil {
			return opts
		}
		update := current
		if opts.ConversationID != "" {
			update = env.ReplaceInstructions(current)
		}
		p.pendingContext = update
		p.phase = instructionsPending
	}
	if p.pendingContext != "" {
		if opts.CustomUserContext != "" {
			opts.CustomUserContext += "\n\n"
		}
		opts.CustomUserContext += p.pendingContext
	}
	return opts
}

// acknowledge records delivery after a response starts streaming or Send succeeds.
// Unstreamed failures retain the exact prepared update for the next request.
func (p *instructionDelivery) acknowledge() {
	if p != nil && p.phase == instructionsPending {
		p.phase = instructionsDelivered
		p.pendingContext = ""
	}
}

// reset discards the conversation snapshot so the next request reloads the files.
func (p *instructionDelivery) reset() {
	if p != nil {
		p.phase = instructionsUnprepared
		p.pendingContext = ""
	}
}

// SkillSummary is presentation metadata exposed to completion clients.
type SkillSummary = env.SkillSummary

// ClientSkills lists the shared conversation registry.
func (e *Engine) ClientSkills(ctx context.Context) ([]SkillSummary, error) {
	if e.clientSkills == nil {
		return nil, nil
	}
	return e.clientSkills.List(ctx)
}

// applySkills adds the catalog, then an explicitly invoked skill's instructions.
func (e *Engine) applySkills(ctx context.Context, opts assistant.SendOptions, skillContext string) assistant.SendOptions {
	if e.clientSkills == nil {
		return opts
	}
	if catalog, err := e.clientSkills.Catalog(ctx); err == nil {
		if catalog == "" && opts.ConversationID != "" {
			catalog = env.SkillsRemovalNotice
		}
		opts.CustomUserContext = joinUserContext(opts.CustomUserContext, catalog)
	}
	opts.CustomUserContext = joinUserContext(opts.CustomUserContext, skillContext)
	return opts
}

func joinUserContext(existing, block string) string {
	if existing == "" || block == "" {
		return existing + block
	}
	return existing + "\n\n" + block
}

// SkillInvocation is a user request resolved against the current conversation registry.
type SkillInvocation struct{ Name, Arguments string }

func (e *Engine) prepareSkill(ctx context.Context, invocation SkillInvocation) (string, error) {
	if e.clientSkills == nil {
		return "", fmt.Errorf("unknown client skill: %s", invocation.Name)
	}
	return e.clientSkills.Invoke(ctx, invocation.Name, invocation.Arguments)
}

// pendingUserContext returns the user context to send with a new user turn, or
// "" when the conversation already carries it.
func (e *Engine) pendingUserContext(ctx context.Context) string {
	if e.userContext == nil {
		return ""
	}
	rendered := e.userContext(ctx)
	if e.sentUserContext != nil && *e.sentUserContext == rendered {
		return ""
	}
	return rendered
}

// resetContext discards conversation snapshots only after a conversation change commits.
func (e *Engine) resetContext() {
	e.sentUserContext = nil
	e.projectInstructions.reset()
	if e.clientSkills != nil {
		e.clientSkills.Reset()
	}
}
