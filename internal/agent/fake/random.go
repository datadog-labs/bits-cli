package fake

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
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

// markdownAnswer builds a deterministic Markdown document. Sized answers use
// the richer generator so larger profiling runs exercise more Markdown forms.
func markdownAnswer(r *rand.Rand, minBytes int) string {
	doc := markdownDocument(r, minBytes > 0)
	for len(doc) < minBytes {
		doc += "\n" + markdownDocument(r, true)
	}
	return doc
}

func markdownDocument(r *rand.Rand, rich bool) string {
	parts := []string{"## " + capitalize(phrase(r, 2+r.Intn(3)))}
	usedCode := false
	for range 2 + r.Intn(4) {
		choice := r.Intn(6)
		if rich {
			choice = r.Intn(14)
		}
		switch choice {
		case 0:
			parts = append(parts, bulletList(r, rich))
		case 1:
			parts = append(parts, orderedList(r, rich))
		case 2:
			parts = append(parts, table(r))
		case 3:
			if !usedCode {
				parts = append(parts, codeBlock(r, rich))
				usedCode = true
				continue
			}
			parts = append(parts, paragraph(r, rich))
		case 4, 5:
			parts = append(parts, paragraph(r, rich))
		case 6:
			parts = append(parts, blockquote(r))
		case 7:
			parts = append(parts, atxHeading(r))
		case 8:
			parts = append(parts, tildeCodeBlock(r))
		case 9:
			parts = append(parts, indentedCode(r))
		case 10:
			parts = append(parts, thematicBreak(r))
		case 11:
			parts = append(parts, definitionList(r))
		case 12:
			parts = append(parts, htmlBlock(r))
		case 13:
			parts = append(parts, setextHeading(r))
		default:
			parts = append(parts, paragraph(r, rich))
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// paragraph returns 2-4 prose sentences.
func paragraph(r *rand.Rand, rich bool) string {
	ss := make([]string, 2+r.Intn(3))
	for i := range ss {
		ss[i] = sentence(r, rich)
	}
	if rich && r.Intn(5) == 0 {
		return strings.Join(ss, "  \n")
	}
	return strings.Join(ss, " ")
}

// sentence returns one capitalized sentence with occasional inline Markdown.
func sentence(r *rand.Rand, rich bool) string {
	ws := make([]string, 6+r.Intn(8))
	for i := range ws {
		w := pick(r, lexicon)
		if i > 0 {
			choices := 12
			if rich {
				choices = 22
			}
			switch r.Intn(choices) {
			case 0:
				w = "**" + w + "**"
			case 1:
				w = "`" + w + "`"
			case 2:
				w = "[" + w + "](https://docs.datadoghq.com)"
			case 3:
				if rich {
					w = "~~" + w + "~~"
				}
			case 4:
				if rich {
					w = "https://" + w + ".example.com"
				}
			case 5:
				if rich {
					w = "<" + w + "@example.com>"
				}
			case 6:
				if rich {
					w = "*" + w + "*"
				}
			case 7:
				if rich {
					w = "_" + w + "_"
				}
			case 8:
				if rich {
					w = `\*` + w + `\*`
				}
			case 9:
				if rich {
					w = `\_` + w + `\_`
				}
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

func bulletList(r *rand.Rand, rich bool) string {
	items := make([]string, 2+r.Intn(3))
	for i := range items {
		marker := "- "
		if rich && r.Intn(4) == 0 {
			marker = "- [" + []string{" ", "x"}[r.Intn(2)] + "] "
		}
		items[i] = marker + phrase(r, 2+r.Intn(4))
		if rich && r.Intn(3) == 0 {
			items[i] += "\n  - nested " + phrase(r, 2)
			if r.Intn(3) == 0 {
				items[i] += "\n    - deeply nested " + phrase(r, 2)
			}
		}
		if rich && r.Intn(4) == 0 {
			items[i] += "\n\n  " + sentence(r, true)
		}
	}
	separator := "\n"
	if rich && r.Intn(3) == 0 {
		separator = "\n\n"
	}
	return strings.Join(items, separator)
}

func orderedList(r *rand.Rand, rich bool) string {
	items := make([]string, 2+r.Intn(3))
	start := 1
	if rich && r.Intn(3) == 0 {
		start = 7
	}
	for i := range items {
		items[i] = strconv.Itoa(start+i) + ". " + phrase(r, 2+r.Intn(4))
		if rich && i == 0 && r.Intn(3) == 0 {
			items[i] += "\n   continued " + phrase(r, 2)
		}
		if rich && r.Intn(4) == 0 {
			items[i] += "\n\n   " + sentence(r, true)
		}
	}
	return strings.Join(items, "\n")
}

func blockquote(r *rand.Rand) string {
	return "> " + sentence(r, true) + "\n> - " + phrase(r, 3) +
		"\n>\n> " + sentence(r, true)
}

func setextHeading(r *rand.Rand) string {
	heading := phrase(r, 2+r.Intn(3))
	return heading + "\n" + strings.Repeat("=", len(heading))
}

func atxHeading(r *rand.Rand) string {
	return strings.Repeat("#", 3+r.Intn(2)) + " " + capitalize(phrase(r, 2+r.Intn(3)))
}

func tildeCodeBlock(r *rand.Rand) string {
	snippet := richCodeSnippets[r.Intn(len(richCodeSnippets))]
	return "~~~" + snippet.lang + "\n" + snippet.body + "\n~~~"
}

func indentedCode(r *rand.Rand) string {
	return "    " + phrase(r, 4) + "\n\n    " + phrase(r, 4)
}

func thematicBreak(r *rand.Rand) string {
	return []string{"---", "* * *", "___"}[r.Intn(3)]
}

func definitionList(r *rand.Rand) string {
	return phrase(r, 2) + "\n: " + sentence(r, true)
}

func htmlBlock(r *rand.Rand) string {
	tag := []string{"div", "details", "section"}[r.Intn(3)]
	return "<" + tag + ">\n" + phrase(r, 5) + "\n</" + tag + ">"
}

// codeSnippets are small, syntactically plausible blocks so the code fence path
// (and chroma highlighting) is exercised across a few languages.
var codeSnippets = []struct{ lang, body string }{
	{"go", "func main() {\n\tfmt.Println(\"latency spike\")\n}"},
	{"bash", "kubectl get pods -n prod\ncurl -s localhost:8126/health"},
	{"json", "{\n  \"service\": \"web\",\n  \"p95_ms\": 42\n}"},
	{"sql", "SELECT service, count(*)\nFROM traces\nGROUP BY service;"},
}

var richCodeSnippets = append([]struct{ lang, body string }{}, codeSnippets...)

func init() {
	richCodeSnippets = append(richCodeSnippets,
		struct{ lang, body string }{"python", "def percentile(values, p):\n    values = sorted(values)\n    return values[int(len(values) * p)]"},
		struct{ lang, body string }{"typescript", "const latency = spans.map(span => span.durationMs)\nconsole.log(Math.max(...latency))"},
		struct{ lang, body string }{"yaml", "service: checkout\nreplicas: 3\nresources:\n  limits:\n    cpu: 500m"},
		struct{ lang, body string }{"diff", "- timeout: 5s\n+ timeout: 10s\n  retries: 2"},
	)
}

func codeBlock(r *rand.Rand, rich bool) string {
	snippets := codeSnippets
	if rich {
		snippets = richCodeSnippets
	}
	s := snippets[r.Intn(len(snippets))]
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
