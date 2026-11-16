package tui

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	// trayLineCap bounds the tray to a glance: more lines collapse into one
	// overflow line.
	trayLineCap = 3
	// trayTranscriptRows is the transcript the tray never takes: below it,
	// the tray shrinks to one summary line.
	trayTranscriptRows = 5
	// trayTitleMinWidth keeps a title readable: below it the step goes, not
	// the title.
	trayTitleMinWidth = 16
	trayStepMaxWidth  = 40
	traySeparator     = " · "
)

// trayLine is one line of the tray: work that keeps running while the user
// chats. Background tasks are lines; other work that runs apart from the turn
// joins as lines of its own.
type trayLine struct {
	title string
	step  string    // what it is doing now, if it says
	since time.Time // when it started, zero if unknown
	// stalled means it lost the connection that follows it and is retrying.
	stalled bool
}

// tray lists what runs apart from the turn, below the composer. It shows
// only while something runs, and holds only the clock it is drawn at.
type tray struct {
	frame int
	now   time.Time
}

// trayAnimationWanted keeps the clock running while the tray shows: its
// spinner turns and its elapsed times advance. Without motion the clock
// still ticks, only for the times.
func (m *Model) trayAnimationWanted() bool {
	return !m.frame.tooSmall && m.frame.trayView != ""
}

// taskLines is a line for each running background task.
func taskLines(tasks []agent.Task) []trayLine {
	var lines []trayLine
	for _, task := range tasks {
		if task.Status != agent.TaskRunning {
			continue
		}
		lines = append(lines, trayLine{
			title:   taskTitle(task),
			step:    taskStep(task.Blocks),
			since:   task.StartedAt,
			stalled: task.Err != nil,
		})
	}
	return lines
}

// taskTitle names a task as its start call's row does.
func taskTitle(task agent.Task) string {
	if task.AgentID == "" {
		return cmp.Or(escape.Inline(task.Name), "Background task")
	}
	return cmp.Or(escape.Inline(task.Name), chat.BackgroundAgentName(task.AgentID))
}

// taskStep is the step the task's latest update ends on: an investigation
// closes each progress update with its current step in italics, such as
// "_Inspecting code_". An update without one, like a conclusion, has no step.
func taskStep(blocks []agent.Block) string {
	for i := len(blocks) - 1; i >= 0; i-- {
		progress := blocks[i].Progress
		if progress == nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(progress.Content), "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		inner, ok := strings.CutPrefix(last, "_")
		if inner, ok2 := strings.CutSuffix(inner, "_"); ok && ok2 && inner != "" && !strings.ContainsRune(inner, '_') {
			return escape.Inline(inner)
		}
		return ""
	}
	return "Starting…"
}

// view renders lines within width and at most rows rows: every line up to
// the cap and an overflow line, or one summary line when that does not fit.
func (t tray) view(lines []trayLine, width, rows int, sty styles.Theme) string {
	if len(lines) == 0 || rows < 1 {
		return ""
	}
	shown := min(len(lines), trayLineCap)
	full := shown
	if len(lines) > shown {
		full++
	}
	glyph := t.glyph(sty)
	if full > rows {
		noun := "tasks"
		if len(lines) == 1 {
			noun = "task"
		}
		return ansi.Truncate(glyph+sty.Chat.ToolName.Render(fmt.Sprintf("%d %s running", len(lines), noun)), width, "…")
	}
	out := make([]string, 0, full)
	for _, line := range lines[:shown] {
		out = append(out, t.line(line, glyph, width, sty))
	}
	if len(lines) > shown {
		more := fmt.Sprintf("+ %d more running", len(lines)-shown)
		out = append(out, ansi.Truncate(sty.Text.Tertiary.Render(more), width, "…"))
	}
	return strings.Join(out, "\n")
}

// line lays out "glyph title · step · time". When space runs out the step
// goes first, then the time, so the title stays.
func (t tray) line(line trayLine, glyph string, width int, sty styles.Theme) string {
	avail := width - ansi.StringWidth(glyph)
	fits := func(parts ...string) bool {
		rest := avail
		for _, part := range parts {
			rest -= ansi.StringWidth(part)
		}
		return rest >= trayTitleMinWidth
	}
	timing, timingStyle := t.timing(line), sty.Text.Tertiary
	if line.stalled {
		timingStyle = sty.Chat.NoticeWarn
	}
	if timing != "" {
		timing = traySeparator + timing
	}
	if !fits(timing) {
		timing = ""
	}
	step := ""
	if line.step != "" {
		step = traySeparator + ansi.Truncate(line.step, trayStepMaxWidth, "…")
	}
	if !fits(step, timing) {
		step = ""
	}
	title := ansi.Truncate(line.title, max(1, avail-ansi.StringWidth(step)-ansi.StringWidth(timing)), "…")
	view := glyph + sty.Chat.ToolName.Render(title) + sty.Chat.ToolDetail.Render(step)
	if timing != "" {
		view += sty.Chat.ToolDetail.Render(traySeparator) + timingStyle.Render(strings.TrimPrefix(timing, traySeparator))
	}
	return view
}

// timing is how long the line has run, or that it is reconnecting.
func (t tray) timing(line trayLine) string {
	if line.stalled {
		return "reconnecting"
	}
	if line.since.IsZero() || t.now.Before(line.since) {
		return ""
	}
	return elapsed(t.now.Sub(line.since))
}

// glyph is the transcript's running spinner, or its static dot without motion.
func (t tray) glyph(sty styles.Theme) string {
	glyph := sty.Chat.StatusSpinner.Frame(t.frame)
	if glyph == "" {
		glyph = "•"
	}
	return sty.Chat.StatusRunning.Render(glyph) + " "
}

// elapsed is a duration as a stopwatch shows it: 12s, 3m05s, 1h02m.
func elapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	default:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
}
