package fake

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// send runs one Send and summarizes the wire messages it emitted.
func send(t *testing.T, f *Fake, message any, opts assistant.SendOptions) ([]string, error) {
	t.Helper()
	var s summaries
	_, err := f.Send(context.Background(), message, opts, func(ar assistant.AssistantResponse) error {
		s.add(ar.Data.Attributes.StructuredMessage)
		return nil
	})
	return s.got, err
}

// summaries is one Send's output, one entry per wire message. Fragments of
// one streamed text or input delta are merged into a single entry.
type summaries struct {
	got     []string
	lastKey string
}

func (s *summaries) add(msg assistant.Message) {
	key, head, body := summarize(msg)
	if key != "" && key == s.lastKey {
		s.got[len(s.got)-1] += body
		return
	}
	s.lastKey = key
	s.got = append(s.got, head+body)
}

// backgroundSend runs one Send concurrently, so a test can act while the
// script is stopped at a breakpoint.
type backgroundSend struct {
	mu      sync.Mutex
	out     summaries
	changed chan struct{}
	done    chan error
}

func startSend(ctx context.Context, f *Fake, message any, opts assistant.SendOptions) *backgroundSend {
	b := &backgroundSend{changed: make(chan struct{}, 1), done: make(chan error, 1)}
	go func() {
		_, err := f.Send(ctx, message, opts, func(ar assistant.AssistantResponse) error {
			b.mu.Lock()
			b.out.add(ar.Data.Attributes.StructuredMessage)
			b.mu.Unlock()
			select {
			case b.changed <- struct{}{}:
			default:
			}
			return nil
		})
		b.done <- err
	}()
	return b
}

func (b *backgroundSend) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.out.got)
}

// waitFor returns once the output so far is exactly want.
func (b *backgroundSend) waitFor(t *testing.T, want []string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for !slices.Equal(b.snapshot(), want) {
		select {
		case <-b.changed:
		case err := <-b.done:
			t.Fatalf("Send ended (err %v) with %q, want it stopped at %q", err, b.snapshot(), want)
		case <-timeout:
			t.Fatalf("output %q, want %q", b.snapshot(), want)
		}
	}
}

// wait returns the Send's error and full output once it ends.
func (b *backgroundSend) wait(t *testing.T) ([]string, error) {
	t.Helper()
	select {
	case err := <-b.done:
		return b.snapshot(), err
	case <-time.After(5 * time.Second):
		t.Fatalf("Send did not end; output %q", b.snapshot())
		return nil, nil
	}
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
			name: "dict input keeps key order", message: `call("write_file", {"path": "p", "content": "c", "n": [1, {"z": None, "a": (True, 2.5)}]})`,
			want: []string{`client_tool_call:write_file {"path":"p","content":"c","n":[1,{"z":null,"a":[true,2.5]}]}`, "usage"},
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

func TestLoadModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.star"), []byte(`message = "loaded"

def run():
    say(message)`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := send(t, &Fake{ScriptRoot: dir}, "load(\"fixture.star\", \"run\")\nrun()", assistant.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"markdown_fragment:loaded", "usage"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoadedModuleSnapshotSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.star")
	old := `def run():
    call("search", {})
    say("from snapshot")
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &Fake{ScriptRoot: dir}
	opts := assistant.SendOptions{ConversationID: "load-replay"}
	first, err := send(t, f, "load(\"fixture.star\", \"run\")\nrun()", opts)
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := []string{"client_tool_call:search {}", "usage"}
	if !slices.Equal(first, wantFirst) {
		t.Fatalf("first round %q, want %q", first, wantFirst)
	}

	if err := os.WriteFile(path, []byte(`def run():
    call("search", {})
    say("changed on disk")
`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := send(t, f, []assistant.ClientToolResponse{{Status: assistant.ToolStatusSuccess}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	wantSecond := []string{"markdown_fragment:from snapshot", "usage"}
	if !slices.Equal(second, wantSecond) {
		t.Fatalf("replayed round %q, want %q", second, wantSecond)
	}
}

func TestLoadedModuleCannotEmitDuringInitialization(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.star"), []byte(`say("import side effect")`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := send(t, &Fake{ScriptRoot: dir}, `load("fixture.star", "unused")`, assistant.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !strings.Contains(got[0], "cannot run while loading a module") {
		t.Fatalf("answer = %q", got)
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

// TestBreakpoints pins where each breakpoint stops, that a continue file
// continues it once, that Esc still cancels, and that replay never stops.
func TestBreakpoints(t *testing.T) {
	stream := assistant.SendOptions{StreamToolCallInput: true}
	input := strings.Repeat("0123456789abcdef", 2) + "tail" // 36 characters, three chunks
	streamedWrite := func(streamed string) []string {
		return []string{"started:write_file", "delta:" + streamed, "client_tool_call:write_file " + input, "usage"}
	}
	for _, test := range []struct {
		name, script       string
		opts               assistant.SendOptions
		stopped, continued []string // output at the stop, and once continued
	}{
		{
			name: "between built-ins", script: `say("a"); breakpoint("x"); say("b")`,
			stopped:   []string{"markdown_fragment:a"},
			continued: []string{"markdown_fragment:a", "markdown_fragment:b", "usage"},
		},
		{
			name: "input halfway", script: `call("write_file", "` + input + `", break_input="x")`, opts: stream,
			stopped:   []string{"started:write_file", "delta:" + input[:18]},
			continued: streamedWrite(input),
		},
		{
			name: "input at start", script: `call("write_file", "` + input + `", break_input="x", at=START)`, opts: stream,
			stopped:   []string{"started:write_file"},
			continued: streamedWrite(input),
		},
		{
			name: "input at end", script: `call("write_file", "` + input + `", break_input="x", at=END)`, opts: stream,
			stopped:   []string{"started:write_file", "delta:" + input},
			continued: streamedWrite(input),
		},
		{
			name: "input after text", script: `call("write_file", "` + input + `", break_input="x", at="abcdef0")`, opts: stream,
			stopped:   []string{"started:write_file", "delta:" + input[:17]},
			continued: streamedWrite(input),
		},
		{
			name: "server tool input", script: `tool("search", {"q": "p95 latency"}, out="ok", break_input="x", at="p95")`, opts: stream,
			stopped:   []string{"started:search", `delta:{"q":"p95`},
			continued: []string{"started:search", `delta:{"q":"p95 latency"}`, `tool_call:search {"q":"p95 latency"}`, "tool_response:success ok", "usage"},
		},
		{
			name: "before results", script: `tool("search", {}, out="ok", break_before_results="x")`,
			stopped:   []string{"tool_call:search {}"},
			continued: []string{"tool_call:search {}", "tool_response:success ok", "usage"},
		},
	} {
		t.Run(test.name+"/continue", func(t *testing.T) {
			f := &Fake{ContinueDir: t.TempDir()}
			run := startSend(context.Background(), f, test.script, test.opts)
			run.waitFor(t, test.stopped)
			continueBreakpoint(t, f, "x")
			got, err := run.wait(t)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.continued) {
				t.Fatalf("got  %q\nwant %q", got, test.continued)
			}
			if _, err := os.Stat(filepath.Join(f.ContinueDir, "x")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("continue file was not consumed: %v", err)
			}
		})
		t.Run(test.name+"/cancel", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			run := startSend(ctx, &Fake{ContinueDir: t.TempDir()}, test.script, test.opts)
			run.waitFor(t, test.stopped)
			cancel()
			got, err := run.wait(t)
			if !errors.Is(err, context.Canceled) || !slices.Equal(got, test.stopped) {
				t.Fatalf("after Esc: err %v, output %q, want %q", err, got, test.stopped)
			}
		})
	}

	t.Run("continue before arrival", func(t *testing.T) {
		f := &Fake{ContinueDir: t.TempDir()}
		continueBreakpoint(t, f, "x")
		got, err := startSend(context.Background(), f, `breakpoint("x"); say("b")`, assistant.SendOptions{}).wait(t)
		if err != nil || !slices.Equal(got, []string{"markdown_fragment:b", "usage"}) {
			t.Fatalf("err %v, output %q", err, got)
		}
	})

	t.Run("replay does not stop again", func(t *testing.T) {
		f := &Fake{ContinueDir: t.TempDir()}
		opts := assistant.SendOptions{ConversationID: "c"}
		first := startSend(context.Background(), f, `breakpoint("x"); r = call("y", {}); say("b")`, opts)
		first.waitFor(t, nil)
		continueBreakpoint(t, f, "x")
		if _, err := first.wait(t); err != nil {
			t.Fatal(err)
		}
		responses := []assistant.ClientToolResponse{{Status: assistant.ToolStatusSuccess}}
		got, err := startSend(context.Background(), f, responses, opts).wait(t)
		if err != nil || !slices.Equal(got, []string{"markdown_fragment:b", "usage"}) {
			t.Fatalf("second round: err %v, output %q", err, got)
		}
	})
}

// continueBreakpoint does what a person does from another terminal.
func continueBreakpoint(t *testing.T, f *Fake, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.ContinueDir, name), nil, 0o600); err != nil {
		t.Fatal(err)
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
		{name: "unnamed breakpoint", script: `breakpoint()`, answer: "a name is required"},
		{name: "breakpoint name is not a path", script: `breakpoint("../x")`, answer: "must match"},
		{name: "break input without streaming", script: `call("x", {}, break_input="x")`, answer: "needs streamed input"},
		{name: "at without break input", script: `call("x", {}, at=END)`, answer: "at needs break_input"},
		{name: "at text not in input", script: `call([("a", {"k": "yes"}), ("b", {})], break_input="x", at="yes")`, answer: `at: "yes" is not in the input of b`},
		{name: "at of the wrong type", script: `call("x", {}, break_input="x", at=3)`, answer: "at must be START, END, or text"},
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
