package agent

import (
	"context"
	"fmt"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

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

// WithProjectInstructions configures instruction discovery. Delivery state belongs
// to this engine and is reset only when its conversation changes.
func WithProjectInstructions(source *env.Instructions) Option {
	return func(engine *Engine) {
		if source != nil {
			engine.projectInstructions = &instructionDelivery{source: source}
		}
	}
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

// WithClientSkills attaches a registry owned exclusively by this engine.
func WithClientSkills(skills *env.Skills) Option {
	return func(e *Engine) { e.clientSkills = skills }
}

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
func (e *Engine) pendingUserContext(ctx context.Context, render func(context.Context) string) string {
	if render == nil {
		return ""
	}
	rendered := render(ctx)
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
