package cmd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

// runToolSet is the shape under test: a local clock tool with a declared gate.
func runToolSet(t *testing.T) *agent.ToolSet {
	t.Helper()
	tools, err := agent.NewToolSet(
		agent.Tool{
			Definition: assistant.ClientTool{Name: "get_local_time"},
			Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
				return agent.ApprovalRequirement{Key: agent.ApprovalKey{Tool: "get_local_time", Resource: "process-clock"}}, true
			},
			Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
				return agent.ToolResult{Output: "noon"}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func runOptions() RunOptions {
	return RunOptions{
		ChatOptions: ChatOptions{AuthMode: "auto", Model: "test-model", PermissionsMode: agent.ModeSkipPermissions},
		Prompt:      "what time is it?",
		Delivery:    "adeep",
	}
}

// The fake-backend path proves the noninteractive surface end to end: no TUI,
// no login, strict versioned JSONL, a client tool round trip delivered as two
// rounds, and a completed outcome with exit nil.
func TestRunFakeBackendStreamsVersionedJSONL(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	var out, stderr bytes.Buffer
	err := Execute(context.Background(), []string{
		"run", "--prompt", `call("list_files", {"path": "."}); say("done")`, "--delivery", "adeep", "--model", "test-model", "--permissions", "skip-permissions",
	}, &out, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("run failed: %v, stderr: %s", err, &stderr)
	}

	counts := map[string]int{}
	var started, finished map[string]any
	for line := range strings.Lines(out.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not an independent JSON object: %q: %v", line, err)
		}
		if record["schema"] != "bits.delivery.adeep" || record["version"] != float64(1) {
			t.Fatalf("line lacks the delivery discriminator: %q", line)
		}
		typ, _ := record["type"].(string)
		counts[typ]++
		switch typ {
		case "run.started":
			started = record
		case "run.finished":
			finished = record
		}
	}
	if started["requested_model"] != "test-model" || finished["outcome"] != "completed" || finished["response"] != "done" {
		t.Fatalf("run.started = %v, run.finished = %v", started, finished)
	}
	if counts["round.started"] != 2 || counts["tool.call"] != 1 || counts["tool.result"] != 1 || counts["assistant.text"] != 0 {
		t.Fatalf("records = %v:\n%s", counts, out.String())
	}
}

// A missing OAuth session fails fast with the `bits login` hint; run never
// opens interactive login or a TUI.
func TestRunMissingOAuthFailsFastWithLoginHint(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "")
	var out bytes.Buffer

	err := runRunWithStore(context.Background(), runOptions(), stubCredentialStore{err: auth.ErrNoSession}, &out)
	if err == nil {
		t.Fatal("run succeeded without an OAuth session")
	}
	if !strings.Contains(err.Error(), "run `bits login`") {
		t.Fatalf("error = %q, want a `bits login` hint", err)
	}
	if !strings.Contains(err.Error(), "OAuth login is required") {
		t.Fatalf("error = %q, want the login-required classification", err)
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == ExitUsage {
		t.Fatalf("login-required misclassified as usage: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("login path wrote to the delivery stream: %q", out.String())
	}
}

// runEngineTurn maps each terminal outcome of the one turn to the process
// contract; the denial evidence stays in the stream when a failure wins.
func TestRunOutcomeExitMapping(t *testing.T) {
	const gates = `g, c = call([("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}}), ("get_local_time", {})]); say(g.status + " " + c.status)`
	writerErr := errors.New("stdout gone")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		name    string
		prompt  string
		mode    agent.PermissionsMode
		ctx     context.Context
		writer  io.Writer
		wantErr func(error) bool
		want    []string // in the delivery
	}{
		{
			name:    "skip-permissions approves local and server gates",
			prompt:  gates,
			mode:    agent.ModeSkipPermissions,
			wantErr: func(err error) bool { return err == nil },
			want:    []string{`"outcome":"completed"`, `"rounds":2`, `"response":"success success"`},
		},
		{
			name:    "a denial exits 3 with the adjusted answer",
			prompt:  gates,
			mode:    agent.ModeDeny,
			wantErr: exitCode(ExitApprovalDenied),
			want:    []string{`"outcome":"approval_denied"`, `"response":"error error"`},
		},
		{
			name:    "a malformed server gate exits 3 under skip-permissions",
			prompt:  `g = call("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}, "tool_call_id": "mismatch"}); say(g.status)`,
			mode:    agent.ModeSkipPermissions,
			wantErr: exitCode(ExitApprovalDenied),
			want:    []string{`"outcome":"approval_denied"`, `"response":"error"`},
		},
		{
			name:    "a runtime failure wins over a denial",
			prompt:  `call("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}}); fail("net")`,
			mode:    agent.ModeDeny,
			wantErr: func(err error) bool { return err != nil && !exitCode(ExitApprovalDenied)(err) },
			want:    []string{`"status":"denied"`, `"outcome":"failed"`},
		},
		{
			name:    "cancellation is a runtime failure",
			prompt:  `say("never")`,
			ctx:     canceled,
			wantErr: func(err error) bool { return errors.Is(err, context.Canceled) },
			want:    []string{`"outcome":"canceled"`},
		},
		{
			name:    "a broken delivery writer is a runtime failure",
			prompt:  `say("done")`,
			writer:  &failRunWriter{err: writerErr},
			wantErr: func(err error) bool { return errors.Is(err, writerErr) },
		},
		{
			name:    "a delivery writer breaking mid-run cancels the turn",
			prompt:  `say("done")`,
			writer:  &failRunWriter{writesLeft: 1, err: writerErr},
			wantErr: func(err error) bool { return errors.Is(err, writerErr) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := runOptions()
			opts.Prompt = tt.prompt
			ctx, out, writer := tt.ctx, &bytes.Buffer{}, tt.writer
			if ctx == nil {
				ctx = context.Background()
			}
			if writer == nil {
				writer = out
			}
			mode := cmp.Or(tt.mode, agent.ModeSkipPermissions)
			err := runEngineTurn(ctx, agent.New(&fake.Fake{}, agent.Config{PermissionsMode: mode}), agent.TurnInput{Tools: runToolSet(t)}, opts, writer)
			if !tt.wantErr(err) {
				t.Fatalf("error = %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("delivery lacks %s:\n%s", want, out.String())
				}
			}
		})
	}
}

func exitCode(code int) func(error) bool {
	return func(err error) bool {
		var exitErr *ExitError
		return errors.As(err, &exitErr) && exitErr.Code == code
	}
}

// failRunWriter fails once its successful writes run out.
type failRunWriter struct {
	writesLeft int
	err        error
}

func (w *failRunWriter) Write(p []byte) (int, error) {
	if w.writesLeft == 0 {
		return 0, w.err
	}
	w.writesLeft--
	return len(p), nil
}

func TestRunCommandAuthenticationFailureIsNotUsageError(t *testing.T) {
	clearStagingEnv(t)
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	out, stderr, err := executeForTest(t, []string{
		"run", "--prompt", "hello", "--delivery", "adeep",
		"--auth", "api-key", "--site", "api.datadoghq.com",
	})
	if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY") {
		t.Fatalf("error = %v, want missing API credentials", err)
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		t.Fatalf("runtime authentication failure classified as exit %d: %v", exit.Code, err)
	}
	if out != "" || stderr != "" {
		t.Fatalf("authentication failure output = %q, stderr = %q", out, stderr)
	}
}

func TestImageFlagsReferenceAbsoluteFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("shot.png", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	images, err := imageContext([]string{"shot.png"})
	if err != nil {
		t.Fatal(err)
	}
	want := []assistant.ContextEntity{{Type: assistant.EntityImageURL, ID: imageinput.FileURL(filepath.Join(dir, "shot.png")), Label: "shot.png"}}
	if !reflect.DeepEqual(images.Entities, want) {
		t.Fatalf("image context = %+v, want %+v", images.Entities, want)
	}
}
