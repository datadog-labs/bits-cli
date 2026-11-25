package acp

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

// mirror is what the client has been told about the conversation. Text and
// reasoning only grow (Transcript accumulates them), so their update is the
// suffix past what was sent. A tool card is sent whole when it changes.
type mirror struct {
	root    string // the session's cwd, which tool paths are relative to
	version uint64 // the newest state applied; older ones are stale
	title   string
	sent    map[key]sentBlock
}

type sentBlock struct {
	id   agent.BlockID // the block last sent under the key
	rev  int
	text int    // bytes of text or reasoning sent
	card uint64 // hash of the tool card last sent, to skip changes it does not show
}

func newMirror(root string) *mirror {
	return &mirror{root: root, sent: make(map[key]sentBlock)}
}

// key names what the client was sent of a block: a conversation block by its
// id, a task's by its place in the task. Reattaching replays a task under new
// message ids, and the client, which cannot retract, must not be sent it twice.
type key struct {
	block agent.BlockID
	task  string
	index int
}

func taskKeys(tasks []agent.Task) map[agent.BlockID]key {
	out := make(map[agent.BlockID]key)
	for _, task := range tasks {
		for i, block := range task.Blocks {
			out[block.ID] = key{task: task.ID, index: i}
		}
	}
	return out
}

// name is the id of the message or card the block becomes. Permission
// requests name a call by its tool call id.
func (k key) name(block agent.Block) string {
	switch {
	case k.task != "":
		return "task:" + k.task + "#" + strconv.Itoa(k.index)
	case block.ToolCallID() != "":
		return block.ToolCallID()
	default:
		return block.ID.String()
	}
}

// updates returns what changed since the last applied state, in transcript
// order. Live user prompts are not echoed: the client sent them. History
// restored on load is replayed whole, user prompts included.
func (m *mirror) updates(state agent.State) []acp.SessionUpdate {
	if state.Version <= m.version {
		return nil
	}
	m.version = state.Version
	var out []acp.SessionUpdate
	inTasks := taskKeys(state.Tasks)
	for _, block := range agent.BlocksWithTasks(state.Blocks, state.Tasks) {
		k, inTask := inTasks[block.ID]
		if !inTask {
			k = key{block: block.ID}
		}
		prev, seen := m.sent[k]
		if seen && prev.id == block.ID && prev.rev == block.Rev {
			continue
		}
		next := prev
		next.id, next.rev = block.ID, block.Rev
		name := k.name(block)
		id := acp.MessageId(name)
		// card is the block's card, as a start or an update.
		var card func(seen bool) acp.SessionUpdate
		switch {
		case block.Tool != nil:
			card = func(seen bool) acp.SessionUpdate { return toolUpdate(m.root, acp.ToolCallId(name), block, seen) }
		case block.Widget != nil, block.App != nil, block.Dashboard != nil, block.Memory != nil:
			card = func(seen bool) acp.SessionUpdate { return shownUpdate(acp.ToolCallId(name), block, seen) }
		case block.Kind == assistant.KindText && block.Markdown != nil:
			out = append(out, text(&next, id, block, block.Markdown.Content, seen)...)
		case block.Kind == assistant.KindProgress && block.Progress != nil:
			// A background task's update reads as a message of its own, as
			// the TUI shows it in the transcript.
			out = append(out, text(&next, id, block, block.Progress.Content, seen)...)
		case block.Kind == assistant.KindReasoning && block.Thinking != nil:
			if delta := grown(&next, block.Thinking.Content); delta != "" {
				update := acp.UpdateAgentThoughtText(delta)
				update.AgentThoughtChunk.MessageId = &id
				out = append(out, update)
			}
		}
		if card != nil {
			// Hashed as an update, so a card's first revision compares with
			// its start.
			hash, err := cardHash(card(true))
			if err != nil || hash != prev.card {
				next.card = hash
				out = append(out, card(seen))
			}
		}
		m.sent[k] = next
	}
	if state.Title != m.title {
		m.title = state.Title
		out = append(out, acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: acp.PatchSet(state.Title)}})
	}
	return out
}

func cardHash(update acp.SessionUpdate) (uint64, error) {
	card, err := json.Marshal(update)
	if err != nil {
		return 0, err
	}
	h := fnv.New64a()
	_, err = h.Write(card)
	return h.Sum64(), err
}

// grown returns what content has past what was sent of it, and records it as
// sent. A replay can install shorter text under the same block; the client
// cannot retract, so that sends nothing.
func grown(sent *sentBlock, content string) string {
	if len(content) <= sent.text {
		return ""
	}
	delta := content[sent.text:]
	sent.text = len(content)
	return delta
}

// text sends an answer's growth, or replays a restored prompt once. Live
// prompts are not echoed: the client sent them.
func text(sent *sentBlock, id acp.MessageId, block agent.Block, content string, seen bool) []acp.SessionUpdate {
	delta := grown(sent, content)
	switch {
	case block.Role != assistant.RoleUser:
		if delta != "" {
			update := acp.UpdateAgentMessageText(delta)
			update.AgentMessageChunk.MessageId = &id
			return []acp.SessionUpdate{update}
		}
	case block.Run == 0 && !seen:
		return userMessage(&id, delta, block.Context)
	}
	return nil
}

// shownUpdate presents what the TUI shows but an editor cannot render, a
// widget, an app's view, a dashboard or recalled memories, as a finished
// card that names it.
func shownUpdate(id acp.ToolCallId, block agent.Block, seen bool) acp.SessionUpdate {
	kind, title := acp.ToolKindOther, ""
	switch {
	case block.Widget != nil:
		title = cmpTitle("Widget", block.Widget.Title)
	case block.App != nil:
		title = cmpTitle("View", block.App.Summary().Title)
	case block.Dashboard != nil:
		title = cmpTitle("Dashboard", block.Dashboard.Title)
	case block.Memory != nil:
		kind, title = acp.ToolKindSearch, fmt.Sprintf("%d memories found", len(block.Memory.Memories))
		if len(block.Memory.Memories) == 1 {
			title = "1 memory found"
		}
	}
	if !seen {
		return acp.StartToolCall(id, title, acp.WithStartKind(kind), acp.WithStartStatus(acp.ToolCallStatusCompleted))
	}
	return acp.UpdateToolCall(id, acp.WithUpdateTitle(title), acp.WithUpdateKind(kind), acp.WithUpdateStatus(acp.ToolCallStatusCompleted))
}

func cmpTitle(noun, title string) string {
	if title == "" {
		return noun
	}
	return noun + ": " + title
}

// userMessage replays a prompt from history: its text, then its images.
func userMessage(id *acp.MessageId, text string, context []assistant.ContextEntity) []acp.SessionUpdate {
	var out []acp.SessionUpdate
	if strings.TrimSpace(text) != "" {
		update := acp.UpdateUserMessageText(text)
		update.UserMessageChunk.MessageId = id
		out = append(out, update)
	}
	for _, entity := range context {
		if entity.Type != assistant.EntityImageURL {
			continue
		}
		// An image is sent as a data URL: data:<media type>;base64,<data>.
		header, data, ok := strings.Cut(strings.TrimPrefix(entity.ID, "data:"), ",")
		media, base64, _ := strings.Cut(header, ";")
		if !ok || base64 != "base64" {
			continue
		}
		update := acp.UpdateUserMessage(acp.ImageBlock(data, media))
		update.UserMessageChunk.MessageId = id
		out = append(out, update)
	}
	return out
}

// toolUpdate presents a tool block. An approval_request presents the server
// call it gates: the request repeats that call's id, so the call's own
// messages later fold into the same block and card.
func toolUpdate(root string, id acp.ToolCallId, block agent.Block, seen bool) acp.SessionUpdate {
	tool := block.Tool
	identity, input, output := tool.Identity(), tool.Input, tool.Output
	gated, isGate := tool.GatedCall()
	if isGate {
		identity, input = spec.Identity{Name: gated.Name}, gated.Input
		// The approval's own result says nothing about the call it gates.
		output = ""
	}
	name := identity.Name
	v := present(root, identity, input)
	if diff, locations := fileDiff(root, tool); diff != nil {
		v.content, v.locations = diff, locations
	}
	status := toolStatus(tool.Status)
	if isGate && tool.Status == agent.ToolSuccess {
		// An approved request has not run the call it gates, as the TUI
		// shows it: the call's own result settles the card.
		status = acp.ToolCallStatusPending
	}
	var raw json.RawMessage
	if tool.HasFinalInput && json.Valid([]byte(input)) {
		raw = json.RawMessage(input)
	}
	content := v.content
	// An edit's diff says what it did, but not why it failed. A server tool
	// may explain its result in Detail alone, as the TUI shows it.
	if tool.Status.IsTerminal() && !isGate && (v.kind != acp.ToolKindEdit || tool.Status != agent.ToolSuccess) {
		if tool.Detail != "" && tool.Detail != output {
			content = append(content, acp.ToolContent(acp.TextBlock(tool.Detail)))
		}
		if output != "" {
			content = append(content, acp.ToolContent(acp.TextBlock(output)))
		}
	}
	if !seen {
		opts := []acp.ToolCallStartOpt{acp.WithStartKind(v.kind), acp.WithStartStatus(status), acp.WithStartName(name)}
		if raw != nil {
			opts = append(opts, acp.WithStartRawInput(raw))
		}
		if content != nil {
			opts = append(opts, acp.WithStartContent(content))
		}
		if v.locations != nil {
			opts = append(opts, acp.WithStartLocations(v.locations))
		}
		return acp.StartToolCall(id, v.title, opts...)
	}
	opts := []acp.ToolCallUpdateOpt{
		acp.WithUpdateTitle(v.title), acp.WithUpdateKind(v.kind), acp.WithUpdateStatus(status),
		acp.WithUpdateContent(content), acp.WithUpdateLocations(v.locations),
	}
	if raw != nil {
		opts = append(opts, acp.WithUpdateRawInput(raw))
	}
	if tool.Status.IsTerminal() && output != "" {
		opts = append(opts, acp.WithUpdateRawOutput(output))
	}
	return acp.UpdateToolCall(id, opts...)
}

// fileDiff is an edit or write's change in hunks, as the TUI shows it: the
// preview of its final input until it applies, then the change the tool
// applied, which may differ if the file changed meanwhile. Hunks keep the
// card small whatever the file's size, and each hunk's location says where
// it starts. NOTE: a diff carries no line offset, so an editor numbers a
// hunk from 1. It is nil for history, which keeps no structured change.
func fileDiff(root string, tool *agent.ToolBlock) ([]acp.ToolCallContent, []acp.ToolCallLocation) {
	state, ok := tool.RenderState.(*filediff.State)
	if !ok {
		return nil, nil
	}
	var path string
	var diff *filediff.Diff
	var created bool
	switch {
	case state.Phase == filediff.PhaseReady && state.Preview != nil && state.Snapshot != nil:
		path, diff, created = state.Snapshot.Path, state.Preview.Diff, state.Snapshot.State == filediff.SnapshotMissing
	case state.Phase == filediff.PhaseApplied && state.Change != nil && state.Change.State == filediff.ChangeApplied:
		path, diff, created = state.Change.Path, state.Change.Diff, state.Change.Op == filediff.OpCreate
	}
	if diff == nil || len(diff.Hunks) == 0 {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	out := make([]acp.ToolCallContent, 0, len(diff.Hunks))
	locations := make([]acp.ToolCallLocation, 0, len(diff.Hunks))
	for _, hunk := range diff.Hunks {
		start := int64(max(hunk.ToLine, 1))
		locations = append(locations, acp.ToolCallLocation{Path: path, Line: &start})
		var before, after strings.Builder
		for i, line := range hunk.Lines {
			// A file's last line may have no newline, which is a change too.
			ending := "\n"
			if i+1 < len(hunk.Lines) && hunk.Lines[i+1].Kind == filediff.LineNoNewline {
				ending = ""
			}
			switch line.Kind {
			case filediff.LineContext:
				before.WriteString(line.Content + ending)
				after.WriteString(line.Content + ending)
			case filediff.LineDelete:
				before.WriteString(line.Content + ending)
			case filediff.LineAdd:
				after.WriteString(line.Content + ending)
			case filediff.LineNoNewline, filediff.LineOmitted:
			}
		}
		if created {
			out = append(out, acp.ToolDiffContent(path, after.String()))
		} else {
			out = append(out, acp.ToolDiffContent(path, after.String(), before.String()))
		}
	}
	// Line content does not show a change of line endings.
	if format := diff.FormatChange(); format != "" && !created {
		out = append(out, acp.ToolContent(acp.TextBlock(format)))
	}
	return out, locations
}

func toolStatus(s agent.ToolStatus) acp.ToolCallStatus {
	switch s {
	case agent.ToolUnknown, agent.ToolAwaitingApproval:
		return acp.ToolCallStatusPending
	case agent.ToolRunning:
		return acp.ToolCallStatusInProgress
	case agent.ToolSuccess:
		return acp.ToolCallStatusCompleted
	case agent.ToolError, agent.ToolCancelled, agent.ToolDenied:
		return acp.ToolCallStatusFailed
	}
	return acp.ToolCallStatusFailed
}

type presentation struct {
	title     string
	kind      acp.ToolKind
	locations []acp.ToolCallLocation
	content   []acp.ToolCallContent
}

// present describes a call from its input alone, so the card is right before
// the call runs, including while it awaits approval.
func present(root string, identity spec.Identity, input string) presentation {
	p := presentation{title: identity.Name, kind: acp.ToolKindOther}
	abs := func(path string) string {
		if path == "" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(root, path)
	}
	at := func(path string) []acp.ToolCallLocation {
		if path == "" {
			return nil
		}
		return []acp.ToolCallLocation{{Path: abs(path)}}
	}
	switch identity {
	case spec.ClientReadFile:
		p.kind = acp.ToolKindRead
		var in spec.ReadFileInput
		if json.Unmarshal([]byte(input), &in) == nil && in.Path != "" {
			p.title, p.locations = "Read "+in.Path, at(in.Path)
			if in.Offset != nil {
				line := int64(*in.Offset)
				p.locations[0].Line = &line
			}
		}
	case spec.ClientListFiles:
		p.kind = acp.ToolKindSearch
		var in spec.ListFilesInput
		if json.Unmarshal([]byte(input), &in) == nil {
			path := in.Path
			if path == "" {
				path = "."
			}
			p.title, p.locations = "List "+path, at(path)
		}
	case spec.ClientGrepFiles:
		p.kind = acp.ToolKindSearch
		var in spec.GrepFilesInput
		if json.Unmarshal([]byte(input), &in) == nil && in.Pattern != "" {
			p.title, p.locations = "Search "+in.Pattern, at(in.Path)
		}
	case spec.ClientWriteFile:
		p.kind = acp.ToolKindEdit
		var in spec.WriteFileInput
		if json.Unmarshal([]byte(input), &in) == nil && in.Path != "" {
			p.title, p.locations = "Write "+in.Path, at(in.Path)
			if in.Content != nil {
				// NOTE: history has no prior text, so a replayed overwrite
				// reads as a creation; fileDiff covers live calls.
				p.content = []acp.ToolCallContent{acp.ToolDiffContent(abs(in.Path), *in.Content)}
			}
		}
	case spec.ClientEditFile:
		p.kind = acp.ToolKindEdit
		var in spec.EditFileInput
		if json.Unmarshal([]byte(input), &in) == nil && in.Path != "" {
			p.title, p.locations = "Edit "+in.Path, at(in.Path)
			for _, edit := range in.Edits {
				if edit.NewText != nil {
					p.content = append(p.content, acp.ToolDiffContent(abs(in.Path), *edit.NewText, edit.OldText))
				}
			}
		}
	case spec.ClientExecCommand:
		p.kind = acp.ToolKindExecute
		var in spec.ExecCommandInput
		if json.Unmarshal([]byte(input), &in) == nil && in.Cmd != "" {
			p.title = in.Cmd
		}
	case spec.ClientWaitCommand, spec.ClientStopCommand:
		p.kind = acp.ToolKindExecute
	}
	return p
}
