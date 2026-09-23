package fake

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// streamRandom streams a pseudo-random answer from seed, so a seed always
// reproduces its stream: thinking, interstitial text, and tool calls
// interleaved in a random order, then a Markdown answer. It returns the
// answer words streamed, which feed the turn's default usage.
func streamRandom(out *emitter, seed int64, size int) (words int, err error) {
	r := rand.New(rand.NewSource(seed))

	for range 2 + r.Intn(4) { // 2-5 pre-answer segments
		switch r.Intn(4) {
		case 0: // thinking
			if err := emitWords(out, r, out.nextID(), assistant.ContentThinking, 4+r.Intn(8)); err != nil {
				return words, err
			}
		case 1: // a short interstitial remark
			c := 5 + r.Intn(15)
			words += c
			if err := emitWords(out, r, out.nextID(), assistant.ContentMarkdownFragment, c); err != nil {
				return words, err
			}
		default: // tool call + result (~half the time)
			id := out.nextID()
			toolID := id + "-tool"
			if err := out.emit(assistant.AssistantMessage(id, toolCall(toolID, r))); err != nil {
				return words, err
			}
			if err := out.emit(assistant.AssistantMessage(id, toolResult(toolID, r))); err != nil {
				return words, err
			}
		}
	}

	// The final answer is a Markdown document streamed token by token so
	// Markdown rendering (including transient partial code fences) is exercised.
	doc := markdownAnswer(r, size)
	words += len(strings.Fields(doc))
	return words, out.text(assistant.ContentMarkdownFragment, doc)
}

// emitWords streams count words of the given content type as one segment:
// fragments share msgID so they concatenate into a single block, with sentence
// and paragraph punctuation.
func emitWords(out *emitter, r *rand.Rand, msgID, typ string, count int) error {
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
		if err := out.emit(assistant.AssistantMessage(msgID, textContent(typ, w))); err != nil {
			return err
		}
	}
	return nil
}

// markdownAnswer builds a deterministic Markdown document: a heading followed
// by 2-5 body blocks (prose, bullet/ordered lists, and at most one fenced code
// block) so the renderer sees a realistic mix of constructs.
func markdownAnswer(r *rand.Rand, minBytes int) string {
	doc := markdownDocument(r)
	for len(doc) < minBytes {
		doc += "\n" + markdownDocument(r)
	}
	return doc
}

func markdownDocument(r *rand.Rand) string {
	parts := []string{"## " + capitalize(phrase(r, 2+r.Intn(3)))}
	usedCode := false
	for range 2 + r.Intn(4) {
		switch r.Intn(6) {
		case 0:
			parts = append(parts, bulletList(r))
		case 1:
			parts = append(parts, orderedList(r))
		case 2:
			parts = append(parts, table(r))
		case 3:
			if !usedCode {
				parts = append(parts, codeBlock(r))
				usedCode = true
				continue
			}
			parts = append(parts, paragraph(r))
		default:
			parts = append(parts, paragraph(r))
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// paragraph returns 2-4 prose sentences.
func paragraph(r *rand.Rand) string {
	ss := make([]string, 2+r.Intn(3))
	for i := range ss {
		ss[i] = sentence(r)
	}
	return strings.Join(ss, " ")
}

// sentence returns one capitalized sentence with occasional inline Markdown
// (bold, inline code, a link) sprinkled on non-leading words.
func sentence(r *rand.Rand) string {
	ws := make([]string, 6+r.Intn(8))
	for i := range ws {
		w := pick(r, lexicon)
		if i > 0 {
			switch r.Intn(12) {
			case 0:
				w = "**" + w + "**"
			case 1:
				w = "`" + w + "`"
			case 2:
				w = "[" + w + "](https://docs.datadoghq.com)"
			}
		}
		ws[i] = w
	}
	ws[0] = capitalize(ws[0])
	return strings.Join(ws, " ") + "."
}

// phrase returns n lowercase words joined by spaces, with no trailing period.
func phrase(r *rand.Rand, n int) string {
	ws := make([]string, n)
	for i := range ws {
		ws[i] = pick(r, lexicon)
	}
	return strings.Join(ws, " ")
}

func bulletList(r *rand.Rand) string {
	items := make([]string, 2+r.Intn(3))
	for i := range items {
		items[i] = "- " + phrase(r, 2+r.Intn(4))
	}
	return strings.Join(items, "\n")
}

func orderedList(r *rand.Rand) string {
	items := make([]string, 2+r.Intn(3))
	for i := range items {
		items[i] = strconv.Itoa(i+1) + ". " + phrase(r, 2+r.Intn(4))
	}
	return strings.Join(items, "\n")
}

// codeSnippets are small, syntactically plausible blocks so the code fence path
// (and chroma highlighting) is exercised across a few languages.
var codeSnippets = []struct{ lang, body string }{
	{"go", "func main() {\n\tfmt.Println(\"latency spike\")\n}"},
	{"bash", "kubectl get pods -n prod\ncurl -s localhost:8126/health"},
	{"json", "{\n  \"service\": \"web\",\n  \"p95_ms\": 42\n}"},
	{"sql", "SELECT service, count(*)\nFROM traces\nGROUP BY service;"},
}

func codeBlock(r *rand.Rand) string {
	s := codeSnippets[r.Intn(len(codeSnippets))]
	return "```" + s.lang + "\n" + s.body + "\n```"
}

// table returns a small GFM table with 2-4 rows of fake service metrics.
func table(r *rand.Rand) string {
	lines := []string{"| Service | P95 | Errors |", "| --- | --- | --- |"}
	for range 2 + r.Intn(3) {
		lines = append(lines, fmt.Sprintf("| %s | %dms | %d |", pick(r, lexicon), 10+r.Intn(300), r.Intn(10)))
	}
	return strings.Join(lines, "\n")
}

// toolCall builds a server tool call with a seeded name and query.
func toolCall(toolID string, r *rand.Rand) assistant.Content {
	input := `{"query":"` + pick(r, lexicon) + `"}`
	return assistant.ToolCallContent(toolID, pick(r, toolNames), input)
}

// toolResult builds the seeded result of a server tool call.
func toolResult(toolID string, r *rand.Rand) assistant.Content {
	output := fmt.Sprintf("ok: %d results", 1+r.Intn(9))
	return assistant.ToolResultContent(toolID, "", assistant.ToolStatusSuccess, output)
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
