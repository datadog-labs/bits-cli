package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// contextDelivery tracks context known to be in the current conversation.
// Delivery is recorded after a response arrives or a request succeeds.
type contextDelivery struct {
	// instructions is the prepared project instructions update. It is resent
	// unchanged until delivered, even if the files change in the meantime.
	instructions          *string
	instructionsDelivered bool
	// userContext is the environment context last delivered; nil when unknown.
	userContext *string
}

// userRequest is a prepared user message and the context it carries.
type userRequest struct {
	message      string
	opts         assistant.SendOptions
	instructions *string // instructions update, until delivered
	userContext  string  // environment context, unless already delivered
}

// pending retains the request's instructions update for retries.
func (d *contextDelivery) pending(req userRequest) {
	if req.instructions != nil {
		d.instructions = req.instructions
	}
}

// delivered records the context carried by a successful request or response.
func (d *contextDelivery) delivered(req userRequest) {
	if req.instructions != nil {
		d.instructionsDelivered = true
	}
	if req.userContext != "" {
		d.userContext = &req.userContext
	}
}

// sendOptions builds the options shared by user and tool-result requests.
// Client tools are included each time because the backend does not retain them.
func (e *Engine) sendOptions(tools *ToolSet) assistant.SendOptions {
	opts := e.opts
	opts.ClientTools = tools.Definitions()
	return opts
}

// prepare builds a user request from opts with its environment context,
// pending project instructions, skills catalog, and invoked skill. It runs off
// the actor, so it reads only its arguments and the engine's immutable
// configuration, and never changes delivery state.
func (e *Engine) prepare(ctx context.Context, in TurnInput, opts assistant.SendOptions, delivery contextDelivery, imageNotice string) (userRequest, error) {
	req := userRequest{message: in.Message, opts: opts}
	resumed := req.opts.ConversationID != ""
	if e.userContext != nil {
		rendered := e.userContext(ctx)
		if sent := delivery.userContext; sent == nil || *sent != rendered {
			req.userContext = rendered
		}
	}
	parts := []string{req.userContext, imageNotice}
	var err error
	if req.instructions, err = e.pendingInstructions(ctx, resumed, delivery); err != nil {
		return req, err
	}
	if req.instructions != nil {
		parts = append(parts, *req.instructions)
	}
	if e.clientSkills != nil {
		if catalog, err := e.clientSkills.Catalog(ctx); err == nil {
			if catalog == "" && resumed {
				catalog = env.SkillsRemovalNotice
			}
			parts = append(parts, catalog)
		}
	}
	if in.Skill != nil {
		if e.clientSkills == nil {
			return req, fmt.Errorf("unknown client skill: %s", in.Skill.Name)
		}
		body, err := e.clientSkills.Invoke(ctx, in.Skill.Name, in.Skill.Arguments)
		if err != nil {
			return req, err
		}
		parts = append(parts, body)
	}
	parts = slices.DeleteFunc(parts, func(part string) bool { return part == "" })
	req.opts.CustomUserContext = strings.Join(parts, "\n\n")
	return req, nil
}

// prepareResume builds the answer to restored tool calls from opts. It carries
// the pending project instructions, since the restored conversation may hold
// outdated ones or another project's. The rest of the context belongs to a
// user message. Like prepare, it runs off the actor.
func (e *Engine) prepareResume(ctx context.Context, opts assistant.SendOptions, delivery contextDelivery) (userRequest, error) {
	req := userRequest{opts: opts}
	var err error
	if req.instructions, err = e.pendingInstructions(ctx, opts.ConversationID != "", delivery); err != nil {
		return req, err
	}
	if req.instructions != nil {
		req.opts.CustomUserContext = *req.instructions
	}
	return req, nil
}

// pendingInstructions returns the project instructions update the
// conversation still needs, or nil once delivered.
func (e *Engine) pendingInstructions(ctx context.Context, resumed bool, delivery contextDelivery) (*string, error) {
	if e.instructions == nil || delivery.instructionsDelivered {
		return nil, nil
	}
	if delivery.instructions != nil {
		return delivery.instructions, nil
	}
	update := e.instructions.Snapshot(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resumed {
		update = env.ReplaceInstructions(update)
	}
	return &update, nil
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

// SkillInvocation is a user request resolved against the current conversation registry.
type SkillInvocation struct{ Name, Arguments string }

// resetContext discards conversation snapshots only after a conversation change commits.
func (e *Engine) resetContext() {
	e.delivery = contextDelivery{}
	if e.clientSkills != nil {
		e.clientSkills.Reset()
	}
}
