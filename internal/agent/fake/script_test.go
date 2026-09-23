package fake

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// send runs one Send and summarizes the wire messages it emitted. Fragments
// of one streamed text or input delta are merged into a single entry.
func send(t *testing.T, f *Fake, message any, opts assistant.SendOptions) ([]string, error) {
	t.Helper()
	var got []string
	lastKey := ""
	_, err := f.Send(context.Background(), message, opts, func(ar assistant.AssistantResponse) error {
		msg := ar.Data.Attributes.StructuredMessage
		key, head, body := summarize(msg)
		if key != "" && key == lastKey {
			got[len(got)-1] += body
			return nil
		}
		lastKey = key
		got = append(got, head+body)
		return nil
	})
	return got, err
}

// summarize renders a message as head+body. key is non-empty for fragments
// whose body merges into the previous entry of the same key.
func summarize(msg assistant.Message) (key, head, body string) {
	c := msg.Content
	switch {
	case msg.Results != nil && msg.Results.Usage != nil:
		return "", "usage", ""
	case c.Type == assistant.ContentMarkdownFragment || c.Type == assistant.ContentThinking:
		return c.Type + msg.MessageID, c.Type + ":", c.TextBody()
	case c.Type == assistant.ContentToolCallInputDelta:
		return c.Type + c.Tool.ToolCallID, "delta:", c.Tool.PartialJSON
	case c.Type == assistant.ContentToolCallStarted:
		return "", "started:" + c.Tool.ToolName, ""
	case c.Tool != nil && c.Tool.Metadata != nil && c.Tool.Metadata.Output == "" && c.Tool.Status == "":
		name := c.Tool.Metadata.Name
		if ns := c.Tool.Metadata.Namespace; ns != nil {
			name = *ns + "." + name
		}
		return "", c.Type + ":" + name + " " + c.Tool.Metadata.Input, ""
	case c.Tool != nil:
		s := c.Type + ":" + c.Tool.Status + " " + c.Tool.Metadata.Output
		if c.Tool.Title != "" {
			s += " title=" + c.Tool.Title
		}
		if c.Tool.Detail != nil {
			s += " detail=" + c.Tool.Detail.Content
		}
		return "", s, ""
	default:
		return "", c.Type, ""
	}
}

func TestBuiltinsEmit(t *testing.T) {
	stream := assistant.SendOptions{StreamToolCallInput: true}
	for _, test := range []struct {
		name    string
		message string
		opts    assistant.SendOptions
		want    []string
	}{
		{
			name: "say and think", message: `think("hmm"); say("Hello **world**")`,
			want: []string{"thinking:hmm", "markdown_fragment:Hello **world**", "usage"},
		},
		{
			name: "tool with extras", message: `tool("search", {"q": "p95"}, out="3", err=True, ns="dd", title="T", detail="D")`,
			want: []string{`tool_call:dd.search {"q":"p95"}`, "tool_response:error 3 title=T detail=D", "usage"},
		},
		{
			name: "tool list emits calls then results", message: `tool([("a", {}, "1"), ("b", None)], out="x")`,
			want: []string{"tool_call:a {}", "tool_call:b {}", "tool_response:success 1", "tool_response:success x", "usage"},
		},
		{
			name: "string input is verbatim", message: `call("read_file", "{not json")`,
			want: []string{"client_tool_call:read_file {not json", "usage"},
		},
		{
			name: "approval request gets its call id", message: `call("approval_request", {"tool_name": "t", "tool_args": {}})`,
			want: []string{`client_tool_call:approval_request {"tool_args":{},"tool_call_id":"fake-msg-3","tool_name":"t"}`, "usage"},
		},
		{
			name: "raw content", message: `raw({"type": "widget_def", "title": "p95", "widget_def": {}})`,
			want: []string{"widget_def", "usage"},
		},
		{
			name: "raw usage replaces the default", message: `raw({"type": "markdown_fragment", "content": ""}, results={"usage": {"tokens_used": 1, "max_tokens": 2}})`,
			want: []string{"usage"},
		},
		{
			name: "streamed input", message: `tool("search", {"query": "a longer query than one chunk"}, out="ok")`, opts: stream,
			want: []string{"started:search", `delta:{"query":"a longer query than one chunk"}`, `tool_call:search {"query":"a longer query than one chunk"}`, "tool_response:success ok", "usage"},
		},
		{
			name: "multi-line script", message: "say(\"a\")\nif True:\n    say(\"b\")",
			want: []string{"markdown_fragment:a", "markdown_fragment:b", "usage"},
		},
		{
			name: "empty call batch continues", message: `rs = call([]); say("after %d" % len(rs))`,
			want: []string{"markdown_fragment:after 0", "usage"},
		},
		{
			name: "stream disabled per call", message: `call("list_files", {}, stream=False)`, opts: stream,
			want: []string{"client_tool_call:list_files {}", "usage"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := send(t, &Fake{}, test.message, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("got  %q\nwant %q", got, test.want)
			}
		})
	}
}

func TestReplayResumesAfterCall(t *testing.T) {
	f := &Fake{}
	opts := assistant.SendOptions{ConversationID: "conversation"}
	first, err := send(t, f, `say("a"); r = call("x", {}); say("b " + r.output)`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"markdown_fragment:a", "client_tool_call:x {}", "usage"}; !slices.Equal(first, want) {
		t.Fatalf("first round %q, want %q", first, want)
	}
	response := assistant.ClientToolResponse{Status: assistant.ToolStatusSuccess, Metadata: assistant.ClientToolMetadata{Output: "out"}}
	second, err := send(t, f, []assistant.ClientToolResponse{response}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"markdown_fragment:b out", "usage"}; !slices.Equal(second, want) {
		t.Fatalf("second round %q, want %q", second, want)
	}
}

// TestRandomDefaultSeed pins what random() without a seed promises: each
// call in a turn and each turn differ, a fresh conversation replays the same
// sequence, and a replayed round does not reuse an earlier call's seed.
func TestRandomDefaultSeed(t *testing.T) {
	answer := func(f *Fake, message any, id string) string {
		t.Helper()
		got, err := send(t, f, message, assistant.SendOptions{ConversationID: id})
		if err != nil {
			t.Fatal(err)
		}
		got = slices.DeleteFunc(got, func(s string) bool {
			return s == "usage" || strings.HasPrefix(s, assistant.ContentClientToolCall)
		})
		return strings.Join(got, "\n")
	}
	f := &Fake{}
	first := answer(f, `random(); call("x", {}); random()`, "a")
	afterCall := answer(f, []assistant.ClientToolResponse{{Status: assistant.ToolStatusSuccess}}, "a")
	nextTurn := answer(f, "random()", "a")
	fresh := answer(&Fake{}, "random()", "b")
	switch {
	case first == "":
		t.Fatal("random() streamed nothing")
	case afterCall == first:
		t.Fatal("the random() after a call reused the seed of the one before it")
	case nextTurn == first:
		t.Fatal("the next turn reused the first turn's seed")
	case fresh != first:
		t.Fatal("a fresh conversation did not replay the first turn")
	}
}

func TestScriptErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		script  string
		answer  string           // a key substring of the answer, beyond the error marker
		wantErr func(error) bool // nil: the error is answered, not returned
	}{
		{name: "syntax", script: `say(`},
		{name: "prose", script: "why is latency high?", answer: "`random()`"},
		{name: "if after semicolon", script: `say("a"); if True: say("b")`, answer: "new line"},
		{name: "runtime", script: `say(1 + "a")`},
		{name: "step limit", script: `for i in range(100000000): pass`},
		{
			name: "rate limited", script: `say("partial"); fail(429)`,
			wantErr: func(err error) bool { return errors.Is(err, assistant.ErrRateLimited) },
		},
		{
			name: "network", script: `say("partial"); fail("net")`,
			wantErr: func(err error) bool { _, ok := errors.AsType[net.Error](err); return ok },
		},
		{
			name: "timeout", script: `say("partial"); fail("timeout")`,
			wantErr: func(err error) bool { return errors.Is(err, context.DeadlineExceeded) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := send(t, &Fake{}, test.script, assistant.SendOptions{})
			if test.wantErr != nil {
				if !test.wantErr(err) {
					t.Fatalf("error = %v", err)
				}
				if len(got) == 0 || got[0] != "markdown_fragment:partial" {
					t.Fatalf("output before fail = %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("script error was returned instead of answered: %v", err)
			}
			if len(got) == 0 || !strings.Contains(got[0], "fake script error") || !strings.Contains(got[0], test.answer) {
				t.Fatalf("answer = %q", got)
			}
		})
	}
}
