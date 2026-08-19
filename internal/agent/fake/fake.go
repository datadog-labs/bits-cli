// Package fake provides an agent.Backend that streams deterministic
// pseudo-random output seeded by the user's message, with no network or auth.
// It satisfies the same Send contract as *assistant.Client, so it plugs into
// the engine at the agent.Backend seam (not the assistant client). Select it
// with BITS_FAKE_BACKEND=1; it drives the real engine/classify/transcript/render
// pipeline, so only the network is faked.
package fake

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

// Fake implements agent.Backend.
var _ agent.Backend = (*Fake)(nil)

// Fake streams a seeded turn: thinking, interstitial text, and frequent tool
// calls interleaved in a random order (tools are not confined to the start),
// then a final answer and usage.
type Fake struct {
	// Delay is the pause between streamed fragments, simulating a live stream.
	// Zero (the default in tests) streams instantly.
	Delay time.Duration

	// seq is a monotonic counter handing out a unique message id per segment. It
	// must not derive from the message: identical prompts would then collide and
	// clobber earlier transcript items (keyed by message id), and interleaved
	// segments sharing an id would merge instead of rendering in order.
	seq atomic.Int64
}

// New returns a Fake with a small inter-fragment delay so interactive use looks
// like a real stream.
func New() *Fake { return &Fake{Delay: 40 * time.Millisecond} }

// Send implements agent.Backend. message seeds the output so the same prompt
// reproduces the same stream. It honors ctx so Esc/Ctrl+C interrupt a turn.
func (f *Fake) Send(ctx context.Context, message any, opts assistant.SendOptions,
	fn func(assistant.AssistantResponse) error,
) (string, error) {
	convID := opts.ConversationID
	if convID == "" {
		convID = "fake-conversation"
	}
	// Seed randomness from the message so a prompt reproduces its stream.
	r := rand.New(rand.NewSource(int64(hashString(fmt.Sprint(message)))))

	// step pauses (respecting cancellation) then delivers one response line.
	step := func(ar assistant.AssistantResponse) error {
		if f.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(f.Delay):
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		return fn(ar)
	}

	// nextMsgID hands out a fresh, globally-unique message id. Each segment gets
	// its own so interleaved segments render in order and turns never collide.
	nextMsgID := func() string { return "fake-msg-" + strconv.FormatInt(f.seq.Add(1), 10) }

	totalWords := 0

	// A turn interleaves thinking, interstitial text, and tool calls in a random
	// order — tools are not confined to the start — then ends with the answer.
	for range 2 + r.Intn(4) { // 2-5 pre-answer segments
		switch r.Intn(4) {
		case 0: // thinking
			if err := emitWords(step, r, convID, nextMsgID(), assistant.ContentThinking, 4+r.Intn(8)); err != nil {
				return convID, err
			}
		case 1: // a short interstitial remark
			c := 5 + r.Intn(15)
			totalWords += c
			if err := emitWords(step, r, convID, nextMsgID(), assistant.ContentMarkdownFragment, c); err != nil {
				return convID, err
			}
		default: // tool call + result (~half the time)
			id := nextMsgID()
			toolID := id + "-tool"
			if err := step(toolCallResp(convID, id, toolID, r)); err != nil {
				return convID, err
			}
			if err := step(toolResultResp(convID, id, toolID, r)); err != nil {
				return convID, err
			}
		}
	}

	// The final answer: its own message, length varying widely — a sentence up to
	// several paragraphs — so long, scrolling replies get exercised too.
	n := 10 + r.Intn(200)
	totalWords += n
	if err := emitWords(step, r, convID, nextMsgID(), assistant.ContentMarkdownFragment, n); err != nil {
		return convID, err
	}

	// Token usage rides on its own line, on the message rather than the content.
	// Scale it with the total words so longer turns report more tokens.
	if err := step(usageResp(convID, nextMsgID(), totalWords)); err != nil {
		return convID, err
	}

	return convID, nil
}

// emitWords streams count words of the given content type as one segment:
// fragments share msgID so they concatenate into a single block, with sentence
// and paragraph punctuation.
func emitWords(step func(assistant.AssistantResponse) error, r *rand.Rand, convID, msgID, typ string, count int) error {
	sentence, sentenceTarget := 0, 8+r.Intn(12)
	paragraph, paragraphTarget := 0, 2+r.Intn(4)
	newSentence := true
	for i := range count {
		w := pick(r, lexicon)
		if newSentence {
			w = capitalize(w)
			newSentence = false
		}
		sentence++
		switch {
		case i == count-1:
			w += "."
		case sentence >= sentenceTarget:
			sentence, sentenceTarget = 0, 8+r.Intn(12)
			newSentence = true
			paragraph++
			if paragraph >= paragraphTarget {
				paragraph, paragraphTarget = 0, 2+r.Intn(4)
				w += ".\n\n"
			} else {
				w += ". "
			}
		default:
			w += " "
		}
		if err := step(textResp(convID, msgID, typ, w)); err != nil {
			return err
		}
	}
	return nil
}

// resp wraps one message in the response envelope the stream delivers.
func resp(convID string, msg assistant.Message) assistant.AssistantResponse {
	var ar assistant.AssistantResponse
	ar.Data.Type = "assistant-response"
	ar.Data.Attributes.ConversationID = convID
	ar.Data.Attributes.StructuredMessage = msg
	return ar
}

func textResp(convID, msgID, typ, text string) assistant.AssistantResponse {
	c := assistant.TextContent(text)
	if typ == assistant.ContentThinking {
		c = assistant.ThinkingContent(text)
	}
	return resp(convID, assistant.AssistantMessage(msgID, c))
}

func toolCallResp(convID, msgID, toolID string, r *rand.Rand) assistant.AssistantResponse {
	input := `{"query":"` + pick(r, lexicon) + `"}`
	return resp(convID, assistant.AssistantMessage(msgID,
		assistant.ToolCallContent(toolID, pick(r, toolNames), input)))
}

func toolResultResp(convID, msgID, toolID string, r *rand.Rand) assistant.AssistantResponse {
	output := fmt.Sprintf("ok: %d results", 1+r.Intn(9))
	return resp(convID, assistant.AssistantMessage(msgID,
		assistant.ToolResultContent(toolID, "", assistant.ToolStatusSuccess, output)))
}

func usageResp(convID, msgID string, words int) assistant.AssistantResponse {
	msg := assistant.AssistantMessage(msgID, assistant.Content{})
	msg.Results = &assistant.Results{
		Usage: &assistant.Usage{TokensUsed: 50 + words*3, MaxTokens: 8000},
	}
	return resp(convID, msg)
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func pick(r *rand.Rand, s []string) string { return s[r.Intn(len(s))] }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var toolNames = []string{"search_logs", "get_metrics", "query_traces", "list_monitors"}

var lexicon = []string{
	"the", "system", "latency", "spike", "service", "request", "error", "rate",
	"deploy", "rollback", "metric", "trace", "span", "cluster", "node", "memory",
	"cpu", "throughput", "queue", "retry", "timeout", "dashboard", "monitor",
	"alert", "incident", "root", "cause", "investigate", "correlate", "baseline",
	"anomaly", "threshold", "window", "percentile", "saturation", "healthy",
}
