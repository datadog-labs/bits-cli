package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

var trayEpoch = time.UnixMilli(1_794_821_702_250)

func trayUpdate(content string) agent.Block {
	return agent.Block{Kind: assistant.KindProgress, Progress: &assistant.ProgressPayload{Content: content}}
}

func runningTask(name string, blocks ...agent.Block) agent.Task {
	return agent.Task{Name: name, AgentID: "investigation", StartedAt: trayEpoch, Status: agent.TaskRunning, Blocks: blocks}
}

func TestTrayView(t *testing.T) {
	launched := trayUpdate("I'm launching an **[investigation](https://example.com)** into this.")
	inspecting := trayUpdate("### Findings\n- `GET /ads` returns 500\n\n_Inspecting code_")
	concluded := trayUpdate("### Root Cause\n`GET /ads` fails because…")
	many := make([]agent.Task, 5)
	for i := range many {
		many[i] = runningTask("Investigate errors", launched)
	}
	for _, tc := range []struct {
		name  string
		tasks []agent.Task
		width int
		rows  int
		want  []string
	}{
		{
			name: "nothing running",
			tasks: []agent.Task{
				{Name: "Done", Status: agent.TaskDone},
				{Name: "Refused", Status: agent.TaskFailed},
				{Name: "Copied", Status: agent.TaskHistorical},
			},
			width: 80, rows: 4,
		},
		{
			name:  "before its first update",
			tasks: []agent.Task{runningTask("Investigate ad-server errors")},
			width: 80, rows: 4,
			want: []string{"• Investigate ad-server errors · Starting… · 3m30s"},
		},
		{
			name:  "step from the latest update, time since the start",
			tasks: []agent.Task{runningTask("Investigate ad-server errors", launched, inspecting)},
			width: 80, rows: 4,
			want: []string{"• Investigate ad-server errors · Inspecting code · 3m30s"},
		},
		{
			name:  "an update without a step shows none",
			tasks: []agent.Task{runningTask("Investigate ad-server errors", launched, inspecting, concluded)},
			width: 60, rows: 4,
			want: []string{"• Investigate ad-server errors · 3m30s"},
		},
		{
			name: "reconnecting replaces the time",
			tasks: []agent.Task{func() agent.Task {
				task := runningTask("Investigate ad-server errors", launched)
				task.Err = errors.New("stream reset")
				return task
			}()},
			width: 60, rows: 4,
			want: []string{"• Investigate ad-server errors · reconnecting"},
		},
		{
			name:  "unnamed tasks are named by their agent",
			tasks: []agent.Task{{AgentID: "investigation", Status: agent.TaskRunning}, {Status: agent.TaskRunning}},
			width: 40, rows: 4,
			want: []string{"• Bits Investigation · Starting…", "• Background task · Starting…"},
		},
		{
			name:  "narrow: the step goes, then the time, the title stays",
			tasks: []agent.Task{runningTask("Investigate ad-server error rate on GET /ads", launched, inspecting)},
			width: 30, rows: 4,
			want: []string{"• Investigate ad-serv… · 3m30s"},
		},
		{
			name:  "past the cap, an overflow line",
			tasks: many,
			width: 40, rows: 4,
			want: []string{
				"• Investigate errors · 3m30s",
				"• Investigate errors · 3m30s",
				"• Investigate errors · 3m30s",
				"+ 2 more running",
			},
		},
		{
			name:  "short of rows, one summary line",
			tasks: many,
			width: 40, rows: 3,
			want: []string{"• 5 tasks running"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := tray{now: trayEpoch.Add(3*time.Minute + 30*time.Second)}
			view := tr.view(taskLines(tc.tasks), tc.width, tc.rows, styles.Default(true).WithoutMotion())
			var got []string
			if view != "" {
				got = strings.Split(ansi.Strip(view), "\n")
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("tray =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			for _, line := range got {
				if w := ansi.StringWidth(line); w > tc.width {
					t.Errorf("line %q is %d cells, wider than %d", line, w, tc.width)
				}
			}
		})
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		12 * time.Second:                "12s",
		3*time.Minute + 5*time.Second:   "3m05s",
		time.Hour + 2*time.Minute + 9e9: "1h02m",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}
