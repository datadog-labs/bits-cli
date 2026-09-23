package fake

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestRandomStreamGolden pins random()'s content so refactors cannot change
// what a seed produces. Message ids are excluded.
func TestRandomStreamGolden(t *testing.T) {
	var b strings.Builder
	for _, seed := range []string{"hello", "why is latency high?"} {
		fmt.Fprintf(&b, "# %s\n", seed)
		_, err := (&Fake{}).Send(context.Background(), "random("+strconv.Quote(seed)+")", assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
			fmt.Fprintln(&b, goldenLine(ar.Data.Attributes.StructuredMessage))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join("testdata", "random.golden")
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != string(want) {
		t.Fatalf("random stream changed; rerun with -update only if intended\n got:\n%s", got)
	}
}

func goldenLine(msg assistant.Message) string {
	c := msg.Content
	if c.Tool != nil && c.Tool.Metadata != nil {
		return fmt.Sprintf("%s\t%q %q %q", c.Type, c.Tool.Metadata.Name, c.Tool.Metadata.Input, c.Tool.Metadata.Output)
	}
	if msg.Results != nil && msg.Results.Usage != nil {
		return fmt.Sprintf("usage\t%d/%d", msg.Results.Usage.TokensUsed, msg.Results.Usage.MaxTokens)
	}
	return fmt.Sprintf("%s\t%q", c.Type, c.TextBody())
}

// collect runs one turn and returns the streamed (type, text) pairs.
func collect(t *testing.T, message string) [][2]string {
	t.Helper()
	f := &Fake{} // no delay
	var got [][2]string
	_, err := f.Send(context.Background(), message, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
		c := ar.Data.Attributes.StructuredMessage.Content
		got = append(got, [2]string{c.Type, c.TextBody()})
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return got
}

func TestDeterministicPerSeed(t *testing.T) {
	a := collect(t, `random("why is latency high?")`)
	b := collect(t, `random("why is latency high?")`)
	if len(a) != len(b) {
		t.Fatalf("nondeterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic fragment %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	if len(collect(t, `random("one")`)) == 0 {
		t.Fatal("expected output")
	}
	// Extremely unlikely to be identical streams for different inputs.
	a, b := collect(t, "random(1)"), collect(t, "random(2)")
	same := len(a) == len(b)
	for i := 0; same && i < len(a); i++ {
		if a[i] != b[i] {
			same = false
		}
	}
	if same {
		t.Fatal("different inputs produced identical streams")
	}
}

func TestEntitySearchIsDeterministicAndMixed(t *testing.T) {
	f := &Fake{}
	first, err := f.SearchEntities(context.Background(), assistant.SearchEntitiesInput{RawQuery: "checkout", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.SearchEntities(context.Background(), assistant.SearchEntitiesInput{RawQuery: "checkout", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entities) < 2 || len(first.Entities) != len(second.Entities) {
		t.Fatalf("fake search results = %#v", first.Entities)
	}
	types := make(map[assistant.EntityType]bool)
	for index := range first.Entities {
		if first.Entities[index].CandidateID != second.Entities[index].CandidateID {
			t.Fatalf("fake search changed at rank %d", index)
		}
		types[first.Entities[index].EntityType] = true
	}
	if len(types) < 2 {
		t.Fatalf("fake search returned one entity type: %#v", first.Entities)
	}
}

func TestEntitySearchFiltersGroupsBeforeLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		groups []string
		query  string
		limit  int
		want   []string
	}{
		{name: "app only", groups: []string{"app"}, limit: 10, want: []string{"app"}},
		{name: "multiple groups", groups: []string{"app", "service"}, limit: 10, want: []string{"service", "app"}},
		{name: "limit after filtering", groups: []string{"app", "service"}, limit: 1, want: []string{"service"}},
		{name: "query and group", groups: []string{"app", "service"}, query: "helper", limit: 10, want: []string{"app"}},
		{name: "default groups", limit: 2, want: []string{"dashboard", "monitor"}},
		{name: "empty groups", groups: []string{}, limit: 10},
		{name: "unknown group", groups: []string{"unknown"}, limit: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := (&Fake{}).SearchEntities(context.Background(), assistant.SearchEntitiesInput{
				SuggestionGroups: test.groups, RawQuery: test.query, Limit: test.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			for _, entity := range response.Entities {
				types = append(types, string(entity.EntityType))
			}
			if !slices.Equal(types, test.want) {
				t.Fatalf("entity types = %v, want %v", types, test.want)
			}
			groups := test.groups
			if groups == nil {
				groups = assistant.DefaultEntitySuggestionGroups
			}
			if !slices.Equal(response.ActiveSuggestionGroups, groups) {
				t.Fatalf("active groups = %v, want %v", response.ActiveSuggestionGroups, groups)
			}
		})
	}
}

func TestStreamsTextAndUsage(t *testing.T) {
	f := &Fake{}
	var text, usage int
	_, err := f.Send(context.Background(), "random()", assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
		msg := ar.Data.Attributes.StructuredMessage
		if msg.Content.Type == assistant.ContentMarkdownFragment && msg.Content.TextBody() != "" {
			text++
		}
		if msg.Results != nil && msg.Results.Usage != nil {
			usage++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if text == 0 {
		t.Error("expected at least one text fragment")
	}
	if usage != 1 {
		t.Errorf("expected exactly one usage line, got %d", usage)
	}
}

// markdownMessages reassembles streamed markdown fragments by message id, so
// each entry is one complete assistant text block.
func markdownMessages(t *testing.T, message string) map[string]string {
	t.Helper()
	f := &Fake{}
	acc := map[string]string{}
	_, err := f.Send(context.Background(), message, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
		m := ar.Data.Attributes.StructuredMessage
		if m.Content.Type == assistant.ContentMarkdownFragment {
			acc[m.MessageID] += m.Content.TextBody()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return acc
}

func TestFinalAnswerIsMarkdown(t *testing.T) {
	// The final answer always opens with a heading; across several seeds we
	// should also see fenced code and list markers at least once.
	sawHeading, sawFence, sawList, sawTable := false, false, false, false
	for i := range 30 {
		for _, txt := range markdownMessages(t, "random("+strconv.Itoa(i)+")") {
			if strings.HasPrefix(txt, "## ") {
				sawHeading = true
			}
			if strings.Contains(txt, "```") {
				sawFence = true
			}
			if strings.Contains(txt, "\n- ") {
				sawList = true
			}
			if strings.Contains(txt, "| --- |") {
				sawTable = true
			}
		}
	}
	if !sawHeading {
		t.Error("expected a Markdown heading in some answer")
	}
	if !sawFence {
		t.Error("expected a fenced code block in some answer")
	}
	if !sawList {
		t.Error("expected a bullet list in some answer")
	}
	if !sawTable {
		t.Error("expected a table in some answer")
	}
}

func TestStreamReassemblesToDoc(t *testing.T) {
	// Token-by-token streaming must reassemble byte-identically to a valid doc:
	// the final answer starts with a heading and ends with a newline.
	msgs := markdownMessages(t, `random("reassemble")`)
	var final string
	for _, txt := range msgs {
		if strings.HasPrefix(txt, "## ") {
			final = txt
		}
	}
	if final == "" {
		t.Fatal("no heading-led answer found")
	}
	if !strings.HasSuffix(final, "\n") {
		t.Errorf("answer should end with a newline: %q", final)
	}
}

func TestReturnsConversationID(t *testing.T) {
	f := &Fake{}
	id, err := f.Send(context.Background(), `say("hi")`, assistant.SendOptions{ConversationID: "abc"}, func(assistant.AssistantResponse) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "abc" {
		t.Errorf("conversation id = %q, want abc (echoed)", id)
	}
}

func TestMessageIDsUniquePerTurn(t *testing.T) {
	// Regression: the message id must not derive from the prompt, or identical
	// prompts across turns collide and clobber earlier transcript items.
	f := &Fake{}
	id := func() string {
		var got string
		_, err := f.Send(context.Background(), `say("same prompt")`, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
			if got == "" {
				got = ar.Data.Attributes.StructuredMessage.MessageID
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if a, b := id(), id(); a == b {
		t.Fatalf("message id must differ across turns, got %q twice", a)
	}
}

func TestMultipleUniqueToolCalls(t *testing.T) {
	f := &Fake{}
	maxCalls := 0
	for _, msg := range []string{`random("a")`, `random("b")`, `random("c")`, `random("d")`, `random("e")`, `random("f")`, `random("g")`, `random("h")`} {
		seen := map[string]bool{}
		calls := 0
		_, err := f.Send(context.Background(), msg, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
			c := ar.Data.Attributes.StructuredMessage.Content
			if c.Type == assistant.ContentToolCall && c.Tool != nil {
				calls++
				if seen[c.Tool.ToolCallID] {
					t.Fatalf("duplicate tool call id %q in one turn (%q)", c.Tool.ToolCallID, msg)
				}
				seen[c.Tool.ToolCallID] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		maxCalls = max(maxCalls, calls)
	}
	if maxCalls < 2 {
		t.Fatalf("expected some turn with multiple tool calls, max was %d", maxCalls)
	}
}

func TestContextCancellation(t *testing.T) {
	f := &Fake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before streaming

	var n int
	_, err := f.Send(ctx, "random()", assistant.SendOptions{}, func(assistant.AssistantResponse) error {
		n++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if n != 0 {
		t.Errorf("streamed %d fragments after cancel, want 0", n)
	}
}

func TestPropagatesCallbackError(t *testing.T) {
	f := &Fake{}
	boom := errors.New("boom")
	_, err := f.Send(context.Background(), "random()", assistant.SendOptions{}, func(assistant.AssistantResponse) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected callback error to propagate, got %v", err)
	}
}
