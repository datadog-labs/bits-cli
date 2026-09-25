package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/escape"
)

const questionChromeHeight = 4 // tabs, rule, gap, and help row

type questionRow struct {
	text   string
	choice int // -1 for non-choice rows
	page   int // review rows link back to their question; otherwise -1
}

type questionTab struct {
	text       string
	start, end int // cell offsets within the panel's horizontal padding
}

// A fixed height across pages prevents the transcript jumping as answers change.
// Even the smallest supported terminal keeps conversation rows above the form.
func (m *Model) questionHeight() int {
	return min(16, max(8, m.height*2/3))
}

func (m *Model) questionBodyHeight() int {
	return m.questionHeight() - questionChromeHeight
}

func (m *Model) questionTop() int {
	return m.list.Height() + chatNoticeHeight
}

func (m *Model) questionTabs(width int) []questionTab {
	q := m.questions
	count := len(q.answers)
	// Reserve the Review tab and gaps first. Narrow terminals retain numbered
	// tabs, while the full question remains in the body below them.
	slot := min(26, max(3, (width-10-count)/count))
	tabs := make([]questionTab, 0, count+1)
	x := 0
	for i := 0; i <= count; i++ {
		label := "✓ Review"
		if i < count {
			label = fmt.Sprint(i + 1)
			if slot >= 10 {
				label = escape.SingleLine(q.input.Questions[i].Question)
			}
			if slot >= 5 {
				marker := "□"
				if q.answers[i] != "" {
					marker = "☒"
				}
				label = marker + " " + label
			}
			label = ansi.Truncate(label, slot-2, "…")
		}
		style := m.styles.Tabs.Item
		if i == q.page {
			style = m.styles.Tabs.Active
		}
		rendered := style.Render(label)
		end := x + ansi.StringWidth(rendered)
		tabs = append(tabs, questionTab{text: rendered, start: x, end: end})
		x = end + 1
	}
	return tabs
}

// questionRows keeps hit targets in the same wrapped row coordinates as the
// view. Tool-provided text is escaped before styling or measuring it.
func (m *Model) questionRows(width int) ([]questionRow, int) {
	q := m.questions
	// textinput's width excludes its cursor cell, and SetWidth alone does not
	// recompute its horizontal viewport. Preserve the caret while resizing it.
	if inputWidth := max(1, width-3); q.text.Width() != inputWidth {
		position := q.text.Position()
		q.text.SetWidth(inputWidth)
		q.text.CursorEnd()
		q.text.SetCursor(position)
	}
	q.text.SetStyles(m.styles.TextInput)
	rows := []questionRow{}
	add := func(text string, style lipgloss.Style, choice, page int, marker string) {
		text = ansi.Hardwrap(ansi.Wordwrap(escape.Multiline(text), width-2, "-"), width-2, true)
		for _, line := range strings.Split(text, "\n") {
			rows = append(rows, questionRow{text: marker + style.Render(line), choice: choice, page: page})
			marker = m.styles.Selector.Marker
		}
	}
	blank := func() { rows = append(rows, questionRow{choice: -1, page: -1}) }
	if q.page == len(q.answers) {
		add("Review answers", m.styles.Text.Primary.Bold(true), -1, -1, "  ")
		blank()
		for i, question := range q.input.Questions {
			add(fmt.Sprintf("%d. %s", i+1, question.Question), m.styles.Text.Primary, -1, i, "  ")
			answer, style := q.answers[i], m.styles.Text.Secondary
			if answer == "" {
				answer, style = "[Unanswered]", m.styles.Feedback.Error
			}
			add(answer, style, -1, i, "  ")
			blank()
		}
		return rows, 0
	}

	question := q.input.Questions[q.page]
	add(question.Question, m.styles.Text.Primary.Bold(true), -1, -1, "  ")
	blank()
	focusRow := 0
	for i := 0; i <= len(question.Options); i++ {
		label, description := "Other", "Type a custom answer"
		if i < len(question.Options) {
			label, description = question.Options[i].Label, question.Options[i].Description
		}
		marker, style := m.styles.Selector.Marker, m.styles.Text.Primary
		if i == q.choices[q.page] {
			marker, style = m.styles.Selector.Selected.Render(m.styles.Selector.SelectedMarker), m.styles.Selector.Selected
			focusRow = len(rows)
		}
		add(fmt.Sprintf("%d. %s", i+1, label), style, i, -1, marker)
		marker = m.styles.Selector.Marker
		if i == len(question.Options) && q.editing {
			focusRow = len(rows)
			rows = append(rows, questionRow{text: marker + q.text.View(), choice: i, page: -1})
		} else {
			add(description, m.styles.Text.Secondary, i, -1, marker)
		}
		if i < len(question.Options) {
			blank()
		}
	}
	return rows, focusRow
}

func (m *Model) visibleQuestionRows(width int) []questionRow {
	q := m.questions
	rows, focusRow := m.questionRows(width)
	height := m.questionBodyHeight()
	if q.follow {
		if focusRow < q.scroll {
			q.scroll = focusRow
		}
		if focusRow >= q.scroll+height {
			q.scroll = focusRow - height + 1
		}
		q.follow = false
	}
	q.scroll = min(q.scroll, max(0, len(rows)-height))
	return rows[q.scroll:min(len(rows), q.scroll+height)]
}

// questionView replaces the composer, leaving the transcript and footer in place.
func (m *Model) questionView() string {
	width := max(3, m.width-4)
	tabs := m.questionTabs(width)
	tabText := make([]string, len(tabs))
	for i, tab := range tabs {
		tabText[i] = tab.text
	}
	rows := m.visibleQuestionRows(width)
	body := make([]string, len(rows))
	for i, row := range rows {
		body[i] = row.text
	}
	lines := []string{
		strings.Join(tabText, " "),
		m.styles.Text.Tertiary.Render(strings.Repeat("─", width)),
		lipgloss.NewStyle().Width(width).Height(m.questionBodyHeight()).Render(strings.Join(body, "\n")),
		"",
	}
	help := "enter select · tab/arrow keys to navigate · esc dismiss"
	if ansi.StringWidth(help) > width {
		help = "enter · tab/arrows · esc dismiss"
	}
	lines = append(lines, m.styles.Text.Secondary.Render(ansi.Truncate(help, width, "…")))
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
}

func (m *Model) clickQuestion(msg tea.MouseClickMsg) tea.Cmd {
	q := m.questions
	if q == nil || !q.request.Pending() || msg.Button != tea.MouseLeft {
		return nil
	}
	x, y := msg.X-2, msg.Y-m.questionTop()
	width := max(3, m.width-4)
	if x < 0 || x >= width || y < 0 {
		return nil
	}
	if y == 0 {
		for i, tab := range m.questionTabs(width) {
			if x >= tab.start && x < tab.end {
				return q.showPage(i)
			}
		}
	}
	rows := m.visibleQuestionRows(width)
	if row := y - 2; row >= 0 && row < len(rows) {
		hit := rows[row]
		if hit.page >= 0 {
			return q.showPage(hit.page)
		}
		if hit.choice >= 0 {
			return q.selectChoice(hit.choice)
		}
	}
	return nil
}
