package agent

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// maxToolInputPreviewBytes bounds the raw streamed argument retained in a
// transcript. The final tool-call metadata remains authoritative and is not
// subject to this preview-only limit.
const maxToolInputPreviewBytes = 256 << 10

// Transcript is the ordered set of aggregated conversation blocks with an id
// index for O(1) patching. It is the engine's headless, surface-agnostic view
// of a conversation: it folds streamed wire messages into blocks. Not safe for
// concurrent use; the engine serializes access.
type Transcript struct {
	blocks        []Block
	index         map[BlockID]int // id -> position in blocks
	accumulatorID BlockID
	accumulator   *strings.Builder // pointer-owned: Builder must not be copied after use
	prompts       int              // user text prompts the conversation holds
	turnNumber    int              // user messages so far, see Block.TurnNumber
	// unsent are prompts shown here that the conversation does not hold
	// yet, in order; they take the next places.
	unsent []unsentPrompt
	// sentTurns maps an unsent prompt's token to its turn number once the
	// conversation holds it, and answeredTurns a client call's id to the turn
	// number of its answer.
	sentTurns     map[int]int
	answeredTurns map[string]int
	lastToken     int
	// openStream tracks the currently streaming (incomplete) text/reasoning
	openStream BlockID
	hasOpen    bool
	// run is the run changing the transcript, stamped on what it changes; 0
	// outside any run.
	run RunID
	// inputPreviews are keyed by wire tool-call id because input deltas for
	// multiple calls can be interleaved. Builders are transcript-owned and are
	// never published through a Block snapshot.
	inputPreviews map[string]*toolInputPreview
}

type toolInputPreview struct {
	value     strings.Builder
	truncated bool
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{
		index:         map[BlockID]int{},
		inputPreviews: map[string]*toolInputPreview{},
	}
}

// Blocks returns the conversation's blocks, then the prompts it does not
// hold yet. Callers must treat it as read-only.
func (t *Transcript) Blocks() []Block {
	if len(t.unsent) == 0 {
		return t.blocks
	}
	out := slices.Clone(t.blocks)
	for _, u := range t.unsent {
		out = append(out, u.block)
	}
	return out
}

func (t *Transcript) MarkAwaitingApproval(id string, prompt ApprovalPrompt) (Block, bool) {
	p := prompt
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Status = ToolAwaitingApproval
		tool.Approval = &p
	})
}

func (t *Transcript) MarkToolRunning(id string) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Status = ToolRunning
		tool.Approval = nil
	})
}

// MarkToolClientSide records client identity inferred from the registered tool
// set when a provider omitted it from a streamed start event.
func (t *Transcript) MarkToolClientSide(id string) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.IsClientSide = true
	})
}

// SetToolRenderState replaces the opaque local state carried by a tool block.
// markTool copies the ToolBlock before swapping it, preserving snapshots held
// by event consumers.
func (t *Transcript) SetToolRenderState(id string, state any) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.RenderState = state
	})
}

func (t *Transcript) MarkToolExecuted(id string, result ToolResult) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Approval = nil
		switch {
		case result.Denied:
			tool.Status = ToolDenied
		case result.Cancelled:
			tool.Status = ToolCancelled
		case result.IsError:
			tool.Status = ToolError
		default:
			tool.Status = ToolSuccess
		}
		if result.Title != "" {
			tool.Title = result.Title
		}
		if result.Output != "" {
			tool.Output = result.Output
		}
		if result.Display != "" {
			tool.Detail = result.Display
		}
		if result.RenderState != nil {
			tool.RenderState = result.RenderState.State
		}
	})
}

// answered reports whether the conversation shows a response to call id.
func (t *Transcript) answered(id string) bool {
	i, ok := t.index[BlockID{Scope: ScopeTool, Key: id}]
	return ok && t.blocks[i].Tool != nil && t.blocks[i].Tool.Responded
}

// markAnswered records that the conversation received the answer to the
// client call id, which this client sent.
func (t *Transcript) markAnswered(id string) {
	if !t.answered(id) {
		t.markTool(id, func(tool *ToolBlock) { tool.Responded = true })
	}
}

func (t *Transcript) markTool(id string, mutate func(*ToolBlock)) (Block, bool) {
	i, ok := t.index[BlockID{Scope: ScopeTool, Key: id}]
	if !ok || t.blocks[i].Tool == nil {
		return Block{}, false
	}
	tool := *t.blocks[i].Tool
	mutate(&tool)
	t.blocks[i].Tool = &tool
	t.revise(i)
	return t.blocks[i], true
}

// AppendUser shows a user prompt the conversation does not hold yet, keyed
// by the place it takes, and returns its block. Unsent prompts are kept
// apart from the conversation's content and shown after it, as in the web
// client: content streaming meanwhile lands before them, and a replay never
// shows one twice. User blocks are complete on creation (never streamed).
// Context is retained without copying; callers must not mutate its slice or
// any nested data after this call.
func (t *Transcript) AppendUser(text string, context []assistant.ContextEntity) (Block, int) {
	t.lastToken++
	b := Block{
		ID:         promptID(t.prompts + len(t.unsent) + 1),
		Role:       assistant.RoleUser,
		Kind:       assistant.KindText,
		Markdown:   &assistant.MarkdownPayload{Content: text},
		Complete:   true,
		Context:    context,
		Run:        t.run,
		TurnNumber: t.turnNumber,
	}
	t.unsent = append(t.unsent, unsentPrompt{block: b, token: t.lastToken})
	return b, t.lastToken
}

// DropUnsent removes the prompt AppendUser named token, if the
// conversation does not hold it, and reports whether it did.
func (t *Transcript) DropUnsent(token int) bool {
	k := slices.IndexFunc(t.unsent, func(u unsentPrompt) bool { return u.token == token })
	if k < 0 {
		return false
	}
	t.unsent = slices.Delete(t.unsent, k, k+1)
	t.renumberUnsent()
	return true
}

// Commit places the unsent prompt AppendUser named token next in the
// conversation, as when the response it gets is the conversation's content.
func (t *Transcript) Commit(token int) {
	k := slices.IndexFunc(t.unsent, func(u unsentPrompt) bool { return u.token == token })
	if k < 0 {
		return
	}
	b := t.unsent[k].block
	id, _ := t.nextPrompt(b.Markdown.Content)
	b.ID = id
	b.Rev++
	t.push(b)
}

// renumberUnsent gives the unsent prompts the places after the prompts the
// conversation holds.
func (t *Transcript) renumberUnsent() {
	for k := range t.unsent {
		b := &t.unsent[k].block
		if id := promptID(t.prompts + k + 1); b.ID != id {
			b.ID = id
			b.Rev++
		}
	}
}

// unsentPrompt is a prompt shown before the conversation holds it. token
// names it for PromptTurn, since its place, and so its id, can change.
type unsentPrompt struct {
	block Block
	token int
}

func (u unsentPrompt) text() string { return u.block.Markdown.Content }

func promptID(n int) BlockID { return BlockID{Scope: ScopePrompt, Key: strconv.Itoa(n)} }

// PromptTurn reports the turn number of the prompt AppendUser named token,
// once the conversation holds it.
func (t *Transcript) PromptTurn(token int) (int, bool) {
	n, ok := t.sentTurns[token]
	return n, ok
}

// AnswerTurn reports the turn number of the answer to a client call, once
// the conversation holds it.
func (t *Transcript) AnswerTurn(callID string) (int, bool) {
	n, ok := t.answeredTurns[callID]
	return n, ok
}

// nextPrompt places a prompt the conversation holds, and returns the unsent
// prompt it confirms, if any: the first with the same text, even past one
// that never arrived. The unsent ones keep the places after.
func (t *Transcript) nextPrompt(text string) (BlockID, *Block) {
	t.prompts++
	id := promptID(t.prompts)
	var matched *Block
	if k := slices.IndexFunc(t.unsent, func(u unsentPrompt) bool { return u.text() == text }); k >= 0 {
		u := t.unsent[k]
		t.unsent = slices.Delete(t.unsent, k, k+1)
		if t.sentTurns == nil {
			t.sentTurns = map[int]int{}
		}
		t.sentTurns[u.token] = t.turnNumber
		matched = &u.block
	}
	t.renumberUnsent()
	return id, matched
}

// AppendMessage folds a wire message into the transcript and returns the block
// it created or updated, plus whether one was produced.
func (t *Transcript) AppendMessage(msg assistant.Message) (Block, bool) {
	if assistant.RoleOf(msg.Role) == assistant.RoleUser {
		t.turnNumber++
	}
	if msg.IsError {
		// The server reuses the answer's message ID for the failure; it is
		// not part of the answer.
		msg.MessageID += "#error"
	}
	b, ok := t.fold(msg)
	if ok {
		if msg.IsError {
			i := t.index[b.ID]
			t.blocks[i].Error = true
			b = t.blocks[i]
		}
		t.closePrior(b)
	}
	return b, ok
}

// TurnNumber is the number of user messages folded so far.
func (t *Transcript) TurnNumber() int { return t.turnNumber }

// fold routes a wire message to the mutator for its kind.
func (t *Transcript) fold(msg assistant.Message) (Block, bool) {
	switch kind := msg.Content.Kind(); kind {
	case assistant.KindText:
		return t.appendMarkdown(msg)
	case assistant.KindReasoning:
		return t.appendReasoning(msg)
	case assistant.KindToolCall, assistant.KindToolResult:
		return t.upsertTool(msg)
	case assistant.KindWidget:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Widget = msg.Content.Widget })
	case assistant.KindDashboard:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Dashboard = msg.Content.Dashboard })
	case assistant.KindProgress:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Progress = msg.Content.Progress })
	case assistant.KindMemory:
		if msg.Content.Memory == nil || len(msg.Content.Memory.Memories) == 0 {
			return Block{}, false
		}
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Memory = msg.Content.Memory })
	case assistant.KindTurnMarker, assistant.KindStop, assistant.KindInternal, assistant.KindUnknown:
		// Control metadata, metadata-only history records, and content types
		// this client does not know are not conversation content it can show. A
		// bare label for a future type would only be noise until it is supported.
		return Block{}, false
	}
	return Block{}, false
}

// appendMarkdown appends a streamed answer-text fragment. Fragments sharing an
// id are concatenated into one block; the first creates an incomplete
// (streaming) block capturing the message's context.
func (t *Transcript) appendMarkdown(msg assistant.Message) (Block, bool) {
	p := msg.Content.Markdown
	if p == nil || p.Content == "" {
		return Block{}, false
	}
	id := BlockIDOf(msg)
	if assistant.RoleOf(msg.Role) == assistant.RoleUser {
		var local *Block
		if id, local = t.nextPrompt(p.Content); local != nil {
			// The block shown while unsent keeps what only this process
			// knows, such as the context it was sent with.
			b := *local
			b.ID = id
			b.Rev++
			t.push(b)
			return t.blocks[t.index[id]], true
		}
	}
	if i, ok := t.index[id]; ok {
		acc := t.accumulatorFor(id, t.blocks[i].Markdown.Content)
		_, _ = acc.WriteString(p.Content)
		t.blocks[i].Markdown = &assistant.MarkdownPayload{Content: acc.String()}
		t.blocks[i].Complete = false
		t.revise(i)
		return t.blocks[i], true
	}
	acc := t.accumulatorFor(id, "")
	_, _ = acc.WriteString(p.Content)
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindText,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Markdown:  &assistant.MarkdownPayload{Content: acc.String()},
	}
	if b.Role == assistant.RoleUser {
		var sent []assistant.ContextEntity
		if json.Unmarshal(msg.ContextEntities, &sent) == nil {
			b.Context = sent
		}
	}
	t.push(b)
	return b, true
}

// appendReasoning appends a streamed thinking fragment. Like answer text the
// body is concatenated across fragments sharing an id, but reasoning keeps its
// own payload so the wire's Redacted flag and provider signature survive the
// fold rather than being flattened into plain text.
func (t *Transcript) appendReasoning(msg assistant.Message) (Block, bool) {
	p := msg.Content.Thinking
	if p == nil || (p.Content == "" && !p.Redacted) {
		return Block{}, false
	}
	id := BlockIDOf(msg)
	if i, ok := t.index[id]; ok {
		prev := t.blocks[i].Thinking
		acc := t.accumulatorFor(id, prev.Content)
		_, _ = acc.WriteString(p.Content)
		next := &assistant.ThinkingPayload{
			Content:          acc.String(),
			EncryptedContent: prev.EncryptedContent,
			Redacted:         prev.Redacted || p.Redacted,
		}
		if p.EncryptedContent != "" {
			next.EncryptedContent = p.EncryptedContent
		}
		t.blocks[i].Thinking = next
		t.blocks[i].Complete = false
		t.revise(i)
		return t.blocks[i], true
	}
	acc := t.accumulatorFor(id, "")
	_, _ = acc.WriteString(p.Content)
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindReasoning,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Thinking: &assistant.ThinkingPayload{
			Content:          acc.String(),
			EncryptedContent: p.EncryptedContent,
			Redacted:         p.Redacted,
		},
	}
	t.push(b)
	return b, true
}

// upsertTool creates a tool block on the call and merges the result into it.
func (t *Transcript) upsertTool(msg assistant.Message) (Block, bool) {
	tp := msg.Content.Tool
	if tp == nil {
		return Block{}, false
	}

	// Streamed input events are updates to a call started earlier. They must not
	// manufacture a block for an unknown or missing id: doing so would merge
	// malformed deltas into a shared empty-id block.
	switch msg.Content.Type {
	case assistant.ContentToolCallStarted:
		if tp.ToolCallID == "" {
			return Block{}, false
		}
	case assistant.ContentToolCallInputDelta:
		if tp.ToolCallID == "" {
			return Block{}, false
		}
		if _, ok := t.index[BlockID{Scope: ScopeTool, Key: tp.ToolCallID}]; !ok {
			return Block{}, false
		}
		if _, ok := t.inputPreviews[tp.ToolCallID]; !ok {
			return Block{}, false
		}
	}

	id := BlockIDOf(msg)
	tc := ToolBlockOf(msg.Content.Tool)
	if msg.Content.Type == assistant.ContentClientToolResponse {
		tc.Responded = true
		if t.answeredTurns == nil {
			t.answeredTurns = map[string]int{}
		}
		t.answeredTurns[tp.ToolCallID] = t.turnNumber
	}
	clientSide, sided := toolSide(msg)
	if sided {
		tc.IsClientSide = clientSide
	}
	if i, ok := t.index[id]; ok {
		// Merge into a copy of the current aggregate, then swap the pointer, so
		// a snapshot already sharing the old pointer is not mutated.
		merged := *t.blocks[i].Tool
		if tc.Name != "" {
			merged.Name = tc.Name
		}
		if tc.Namespace != nil {
			merged.Namespace = tc.Namespace
		}
		if hasFinalToolInput(msg) {
			merged.Input = tc.Input
			merged.HasFinalInput = true
			merged.InputPartial = ""
			merged.InputPreviewTruncated = false
		}
		if tc.Output != "" {
			merged.Output = tc.Output
		}
		if tc.Title != "" {
			merged.Title = tc.Title
		}
		if tc.Detail != "" {
			merged.Detail = tc.Detail
		}
		switch merged.Status {
		case ToolDenied, ToolCancelled:
			// Wire statuses cannot represent these local outcomes. Preserve them
			// when an echoed response supplies only a generic error status.
		case ToolUnknown, ToolRunning, ToolAwaitingApproval, ToolSuccess, ToolError:
			if tc.Status != ToolUnknown {
				merged.Status = tc.Status
			}
		}
		if sided {
			merged.IsClientSide = clientSide
		}
		if msg.Content.Type == assistant.ContentToolCallStarted {
			// A repeated started event resets no already-received input; it only
			// refreshes the identifying metadata.
			if preview := t.inputPreviews[tp.ToolCallID]; preview != nil {
				merged.InputPartial = preview.value.String()
				merged.InputPreviewTruncated = preview.truncated
			}
		}
		if msg.Content.Type == assistant.ContentToolCallInputDelta {
			t.inputToolPreview(&merged, tp.ToolCallID, tp.PartialJSON)
		}
		if msg.Content.Type == assistant.ContentClientToolResponse {
			merged.Responded = true
		}
		if msg.Content.Type == assistant.ContentClientToolCall && merged.Responded {
			// A call asked again is a new call of the current turn.
			merged.Responded = false
			t.blocks[i].TurnNumber = t.turnNumber
		}
		t.blocks[i].Tool = &merged
		t.revise(i)
		if hasFinalToolInput(msg) || msg.Content.Type == assistant.ContentToolResponse || msg.Content.Type == assistant.ContentClientToolResponse {
			delete(t.inputPreviews, tp.ToolCallID)
		}
		return t.blocks[i], true
	}

	// Only a started event initializes the per-call preview accumulator. A
	// normal final call can still create a block for backwards compatibility
	// with streams that do not advertise streamed input.
	if msg.Content.Type == assistant.ContentToolCallStarted {
		t.inputPreviews[tp.ToolCallID] = &toolInputPreview{}
	}
	if tc.Status == ToolUnknown {
		tc.Status = ToolRunning
	}
	if hasFinalToolInput(msg) {
		tc.HasFinalInput = true
	}
	if msg.Content.Type == assistant.ContentToolCallStarted {
		t.inputToolPreview(&tc, tp.ToolCallID, "")
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleAssistant,
		Kind:      assistant.KindToolCall,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Tool:      &tc,
		Complete:  true,
	}
	t.push(b)
	if hasFinalToolInput(msg) {
		delete(t.inputPreviews, tp.ToolCallID)
	}
	return b, true
}

func hasFinalToolInput(msg assistant.Message) bool {
	if msg.Content.Tool == nil || msg.Content.Tool.Metadata == nil {
		return false
	}
	switch msg.Content.Type {
	case assistant.ContentToolCall, assistant.ContentClientToolCall:
		return true
	case assistant.ContentToolResponse, assistant.ContentClientToolResponse:
		// A result repeats its call's input, if any.
		return msg.Content.Tool.Metadata.Input != ""
	default:
		return false
	}
}

// toolSide reports whether msg says its call runs on the client, if it says.
// The latest call or result decides, since an approval_request gate shares its
// server call's id: the gate's messages are client-side, then the server's
// result makes the block the server call again. Only tool_call_started carries
// the wire is_client_side field; the other types imply it, which also covers
// replayed history, which never passes through the engine fold.
func toolSide(msg assistant.Message) (clientSide, ok bool) {
	switch msg.Content.Type {
	case assistant.ContentToolCall, assistant.ContentToolResponse:
		return false, true
	case assistant.ContentClientToolCall, assistant.ContentClientToolResponse:
		return true, true
	case assistant.ContentToolCallStarted:
		return msg.Content.Tool.IsClientSide, msg.Content.Tool.HasClientSide
	default:
		return false, false
	}
}

func (t *Transcript) inputToolPreview(tool *ToolBlock, id, delta string) {
	preview := t.inputPreviews[id]
	if preview == nil {
		preview = &toolInputPreview{}
		t.inputPreviews[id] = preview
	}
	if !preview.truncated && delta != "" {
		remaining := maxToolInputPreviewBytes - preview.value.Len()
		if remaining <= 0 {
			preview.truncated = true
		} else if len(delta) > remaining {
			_, _ = preview.value.WriteString(delta[:remaining])
			preview.truncated = true
		} else {
			_, _ = preview.value.WriteString(delta)
		}
	}
	tool.InputPartial = preview.value.String()
	tool.InputPreviewTruncated = preview.truncated
}

// appendPassthrough upserts a non-streamed block whose payload the block layer
// carries verbatim from the wire (widget, dashboard, progress).
func (t *Transcript) appendPassthrough(msg assistant.Message, kind assistant.ContentKind, set func(*Block)) (Block, bool) {
	id := BlockIDOf(msg)
	if i, ok := t.index[id]; ok {
		set(&t.blocks[i])
		t.revise(i)
		return t.blocks[i], true
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      kind,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Complete:  true,
	}
	set(&b)
	t.push(b)
	return b, true
}

// closePrior implements block-scoped completion. It optimistically assumes a
// streamed text/reasoning block is done once a fragment lands on a different
// block id.
//
// Assistant API does not forward thinking start/complete events.
func (t *Transcript) closePrior(b Block) {
	if t.hasOpen && t.openStream != b.ID {
		priorID := t.openStream
		if i, ok := t.index[priorID]; ok && !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.revise(i)
		}
		t.releaseAccumulator(priorID)
		t.hasOpen = false
	}
	if !b.Complete {
		t.openStream = b.ID
		t.hasOpen = true
	}
}

// FinalizeAll marks every still-open block complete; called at turn end as the
// backstop for the last open block (typically the answer text, which has no
// status glyph). Mid-turn blocks are already closed incrementally by
// closePrior, so this normally only touches that trailing block. It reports
// whether the transcript changed.
func (t *Transcript) FinalizeAll() bool {
	finalized := false
	if t.hasOpen {
		id := t.openStream
		if i, ok := t.index[id]; ok && !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.revise(i)
			finalized = true
		}
		t.releaseAccumulator(id)
	}
	t.hasOpen = false
	return finalized
}

func (t *Transcript) accumulatorFor(id BlockID, existing string) *strings.Builder {
	if t.accumulator != nil && t.accumulatorID == id {
		return t.accumulator
	}
	acc := &strings.Builder{}
	if existing != "" {
		_, _ = acc.WriteString(existing)
	}
	t.accumulatorID = id
	t.accumulator = acc
	return acc
}

func (t *Transcript) releaseAccumulator(id BlockID) {
	if t.accumulator != nil && t.accumulatorID == id {
		t.accumulatorID = BlockID{}
		t.accumulator = nil
	}
}

// adopt records that the current run takes over the tool call id, which it
// did not create, before changing it.
func (t *Transcript) adopt(id string) {
	if i, ok := t.index[BlockID{Scope: ScopeTool, Key: id}]; ok {
		t.revise(i)
	}
}

// supersede makes t, which replaces prev, newer than it: its blocks start at
// revisions above prev's, so a consumer that caches a block by id and
// revision does not take a replayed block for one it already has.
func (t *Transcript) supersede(prev *Transcript) {
	base := 0
	for _, b := range prev.blocks {
		base = max(base, b.Rev+1)
	}
	for i := range t.blocks {
		t.blocks[i].Rev += base
	}
}

// carryLocal keeps what only this process knows about prev's blocks on the
// matching blocks of t, a replay replacing prev: presentation state, a local
// tool detail, a parked approval, and outcomes the wire cannot express. While
// the replay's turn is running, its tools' states are the conversation's.
func (t *Transcript) carryLocal(prev *Transcript, running bool) {
	for i, b := range t.blocks {
		j, ok := prev.index[b.ID]
		if !ok {
			continue
		}
		// The run that changed a block is local knowledge too.
		t.blocks[i].Run = prev.blocks[j].Run
		if b.Tool == nil || prev.blocks[j].Tool == nil {
			continue
		}
		old, tool := prev.blocks[j].Tool, *b.Tool
		tool.RenderState = old.RenderState
		tool.Approval = old.Approval
		tool.Detail = cmp.Or(tool.Detail, old.Detail)
		switch {
		case running && old.Status == ToolCancelled:
			// A local cancel does not end a call the turn still runs.
		case old.Status == ToolDenied, old.Status == ToolCancelled,
			!tool.Status.IsTerminal() && old.Status.IsTerminal(),
			old.Status == ToolAwaitingApproval && !tool.Responded:
			tool.Status = old.Status
			tool.Output = cmp.Or(old.Output, tool.Output)
		}
		t.blocks[i].Tool = &tool
	}
}

// noteAnswer counts a client call's answer without showing it, when it
// answers a call not shown.
func (t *Transcript) noteAnswer(msg assistant.Message) {
	if msg.Content.Tool == nil {
		return
	}
	if _, shown := t.index[BlockID{Scope: ScopeTool, Key: msg.Content.Tool.ToolCallID}]; shown {
		t.AppendMessage(msg)
		return
	}
	t.turnNumber++
	if t.answeredTurns == nil {
		t.answeredTurns = map[string]int{}
	}
	t.answeredTurns[msg.Content.Tool.ToolCallID] = t.turnNumber
}

// keepUnsent carries prev's prompt bookkeeping to t, a replay replacing
// prev: the turns of prompts prev saw sent, and the prompts t does not hold
// yet, which it still shows after its content.
func (t *Transcript) keepUnsent(prev *Transcript) {
	for token, n := range prev.sentTurns {
		if t.sentTurns == nil {
			t.sentTurns = map[int]int{}
		}
		t.sentTurns[token] = n
	}
	t.lastToken = prev.lastToken
	// The prompts the replay holds beyond those prev did are candidates: the
	// first with an unsent prompt's text is that prompt, sent, wherever the
	// replay places it.
	claimed := map[int]bool{}
	for _, u := range prev.unsent {
		sent := false
		for _, b := range t.blocks {
			n, err := strconv.Atoi(b.ID.Key)
			if b.ID.Scope != ScopePrompt || err != nil || n <= prev.prompts || claimed[n] || b.Markdown == nil || b.Markdown.Content != u.text() {
				continue
			}
			claimed[n], sent = true, true
			if t.sentTurns == nil {
				t.sentTurns = map[int]int{}
			}
			t.sentTurns[u.token] = b.TurnNumber
			break
		}
		if !sent {
			t.unsent = append(t.unsent, u)
		}
	}
	t.renumberUnsent()
}

// revise records a change to the block at i. A change outside any run, such
// as a result the conversation shows after the run's response ended, keeps
// the block the run's.
func (t *Transcript) revise(i int) {
	t.blocks[i].Rev++
	if t.run != 0 {
		t.blocks[i].Run = t.run
	}
}

func (t *Transcript) push(b Block) {
	// Mutators upsert by id before reaching push, so a duplicate is a programmer
	// error, not bad input; fail loudly.
	if _, ok := t.index[b.ID]; ok {
		panic("agent: push of duplicate block id " + b.ID.String())
	}
	b.Run = t.run
	b.TurnNumber = t.turnNumber
	t.index[b.ID] = len(t.blocks)
	t.blocks = append(t.blocks, b)
}
