package acp

import (
	"strconv"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// mirror is what the client has been told about the conversation. Text and
// reasoning only grow (Transcript accumulates them), so their update is the
// suffix past what was sent.
type mirror struct {
	version uint64 // the newest state applied; older ones are stale
	title   string
	sent    map[key]sentBlock
}

type sentBlock struct {
	id   agent.BlockID // the block last sent under the key
	rev  int
	text int // bytes of text or reasoning sent
}

func newMirror() *mirror {
	return &mirror{sent: make(map[key]sentBlock)}
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

// name is the id of the message the block becomes.
func (k key) name(block agent.Block) string {
	switch {
	case k.task != "":
		return "task:" + k.task + "#" + strconv.Itoa(k.index)
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
		switch {
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
		m.sent[k] = next
	}
	if state.Title != m.title {
		m.title = state.Title
		out = append(out, acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: acp.PatchSet(state.Title)}})
	}
	return out
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
