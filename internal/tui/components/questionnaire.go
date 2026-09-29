package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

type Question struct {
	Prompt  string
	Options []Option
}

type Option struct {
	Label, Description string
}

// Questionnaire asks one or more multiple-choice questions, each with a
// free-text Other choice, followed by a review page. Tool-provided text is
// escaped before it is styled or measured.
type Questionnaire struct {
	questions []Question
	answers   []string
	choices   []int
	custom    []string
	page      int // len(questions) is the review page
	editing   bool
	text      textinput.Model
	scroll    int
	follow    bool
	width     int
	height    int
	styles    styles.Theme
	dismissed bool
	complete  bool
}

func NewQuestionnaire(questions []Question) *Questionnaire {
	text := textinput.New()
	text.Prompt = ""
	text.SetVirtualCursor(true)
	text.Placeholder = "Type your answer"
	text.CharLimit = 0
	return &Questionnaire{
		questions: questions, text: text,
		answers: make([]string, len(questions)),
		choices: make([]int, len(questions)),
		custom:  make([]string, len(questions)), follow: true,
	}
}

func (q *Questionnaire) SetSize(width, height int, theme styles.Theme) {
	if q.width != width || q.height != height {
		q.follow = true
	}
	q.width, q.height, q.styles = width, height, theme
}

func (q *Questionnaire) MinSize() (int, int) { return 36, 14 }

// Answers reports the confirmed answers once the user submits or dismisses.
func (q *Questionnaire) Answers() (answers []string, dismissed, done bool) {
	return append([]string(nil), q.answers...), q.dismissed, q.complete
}

// finish submits unless a question is unanswered, which it shows instead.
func (q *Questionnaire) finish(dismiss bool) {
	if !dismiss {
		for i, answer := range q.answers {
			if strings.TrimSpace(answer) == "" {
				_ = q.showPage(i)
				return
			}
		}
	}
	q.dismissed, q.complete = dismiss, true
}

// saveDraft keeps custom text separate from explicitly confirmed answers.
func (q *Questionnaire) saveDraft() {
	if q.editing {
		q.custom[q.page] = q.text.Value()
	}
}

func (q *Questionnaire) selectChoice(choice int) tea.Cmd {
	q.saveDraft()
	q.choices[q.page] = choice
	q.follow = true
	q.editing = choice == len(q.questions[q.page].Options)
	if q.editing {
		q.text.SetValue(q.custom[q.page])
		return q.text.Focus()
	}
	q.text.Blur()
	return nil
}

func (q *Questionnaire) showPage(page int) tea.Cmd {
	q.saveDraft()
	q.editing = false
	q.text.Blur()
	q.page = (page + len(q.answers) + 1) % (len(q.answers) + 1)
	q.scroll, q.follow = 0, true
	if q.page < len(q.answers) {
		return q.selectChoice(q.choices[q.page])
	}
	return nil
}

func (q *Questionnaire) Update(msg tea.Msg) tea.Cmd {
	if q.complete {
		return nil
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		return q.click(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			q.scroll = max(0, q.scroll-1)
		case tea.MouseWheelDown:
			q.scroll++
		}
		q.follow = false
		return nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			q.finish(true)
			return nil
		case "tab", "ctrl+right":
			return q.showPage(q.page + 1)
		case "shift+tab", "ctrl+left":
			return q.showPage(q.page - 1)
		case "pgup", "pgdown":
			delta := q.bodyHeight()
			if key.String() == "pgup" {
				delta = -delta
			}
			q.scroll = max(0, q.scroll+delta)
			q.follow = false
			return nil
		case "enter":
			if q.page == len(q.answers) {
				q.finish(false)
				return nil
			}
			question := q.questions[q.page]
			if q.editing {
				if strings.TrimSpace(q.text.Value()) == "" {
					return nil
				}
				q.answers[q.page] = q.text.Value()
			} else {
				q.answers[q.page] = question.Options[q.choices[q.page]].Label
			}
			return q.showPage(q.page + 1)
		case "up", "down":
			if q.page < len(q.answers) {
				delta := 1
				if key.String() == "up" {
					delta = -1
				}
				count := len(q.questions[q.page].Options) + 1
				return q.selectChoice((q.choices[q.page] + delta + count) % count)
			}
		}
		// Text entry owns printable keys, including digits and brackets.
		if !q.editing {
			switch key.String() {
			case "[":
				return q.showPage(q.page - 1)
			case "]":
				return q.showPage(q.page + 1)
			}
			if q.page < len(q.answers) {
				count := len(q.questions[q.page].Options) + 1
				switch key.String() {
				case "j":
					return q.selectChoice((q.choices[q.page] + 1) % count)
				case "k":
					return q.selectChoice((q.choices[q.page] + count - 1) % count)
				}
				if len(key.String()) == 1 && key.Code >= '1' && key.Code < '1'+rune(count) {
					return q.selectChoice(int(key.Code - '1'))
				}
			}
		}
	}
	if q.editing {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			q.follow = true
		}
		var cmd tea.Cmd
		q.text, cmd = q.text.Update(msg)
		return cmd
	}
	return nil
}

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
func (q *Questionnaire) Height() int {
	return min(16, max(8, q.height*2/3))
}

func (q *Questionnaire) bodyHeight() int {
	return q.Height() - questionChromeHeight
}

func (q *Questionnaire) tabs(width int) []questionTab {
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
				label = escape.SingleLine(q.questions[i].Prompt)
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
		style := q.styles.Tabs.Item
		if i == q.page {
			style = q.styles.Tabs.Active
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
func (q *Questionnaire) rows(width int) ([]questionRow, int) {
	// textinput's width excludes its cursor cell, and SetWidth alone does not
	// recompute its horizontal viewport. Preserve the caret while resizing it.
	if inputWidth := max(1, width-3); q.text.Width() != inputWidth {
		position := q.text.Position()
		q.text.SetWidth(inputWidth)
		q.text.CursorEnd()
		q.text.SetCursor(position)
	}
	q.text.SetStyles(q.styles.TextInput)
	rows := []questionRow{}
	add := func(text string, style lipgloss.Style, choice, page int, marker string) {
		text = ansi.Hardwrap(ansi.Wordwrap(escape.Multiline(text), width-2, "-"), width-2, true)
		for _, line := range strings.Split(text, "\n") {
			rows = append(rows, questionRow{text: marker + style.Render(line), choice: choice, page: page})
			marker = q.styles.Selector.Marker
		}
	}
	blank := func() { rows = append(rows, questionRow{choice: -1, page: -1}) }
	if q.page == len(q.answers) {
		add("Review answers", q.styles.Text.Primary.Bold(true), -1, -1, "  ")
		blank()
		for i, question := range q.questions {
			add(fmt.Sprintf("%d. %s", i+1, question.Prompt), q.styles.Text.Primary, -1, i, "  ")
			answer, style := q.answers[i], q.styles.Text.Secondary
			if answer == "" {
				answer, style = "[Unanswered]", q.styles.Feedback.Error
			}
			add(answer, style, -1, i, "  ")
			blank()
		}
		return rows, 0
	}

	question := q.questions[q.page]
	add(question.Prompt, q.styles.Text.Primary.Bold(true), -1, -1, "  ")
	blank()
	focusRow := 0
	for i := 0; i <= len(question.Options); i++ {
		label, description := "Other", "Type a custom answer"
		if i < len(question.Options) {
			label, description = question.Options[i].Label, question.Options[i].Description
		}
		marker, style, descStyle := q.styles.Selector.Marker, q.styles.Text.Secondary, q.styles.Text.Tertiary
		if i == q.choices[q.page] {
			marker, style, descStyle = q.styles.Selector.Selected.Render(q.styles.Selector.SelectedMarker), q.styles.Selector.Selected, q.styles.Text.Secondary
			focusRow = len(rows)
		}
		add(fmt.Sprintf("%d. %s", i+1, label), style, i, -1, marker)
		marker = q.styles.Selector.Marker
		if i == len(question.Options) && q.editing {
			focusRow = len(rows)
			rows = append(rows, questionRow{text: marker + q.text.View(), choice: i, page: -1})
		} else {
			add(description, descStyle, i, -1, marker)
		}
		if i < len(question.Options) {
			blank()
		}
	}
	return rows, focusRow
}

func (q *Questionnaire) visibleRows(width int) []questionRow {
	rows, focusRow := q.rows(width)
	height := q.bodyHeight()
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

// View replaces the composer, leaving the transcript and footer in place.
func (q *Questionnaire) View() string {
	width := max(3, q.width-4)
	tabs := q.tabs(width)
	tabText := make([]string, len(tabs))
	for i, tab := range tabs {
		tabText[i] = tab.text
	}
	rows := q.visibleRows(width)
	body := make([]string, len(rows))
	for i, row := range rows {
		body[i] = row.text
	}
	lines := []string{
		strings.Join(tabText, " "),
		q.styles.Text.Tertiary.Render(strings.Repeat("─", width)),
		lipgloss.NewStyle().Width(width).Height(q.bodyHeight()).Render(strings.Join(body, "\n")),
		"",
	}
	help := "enter select · tab/arrow keys to navigate · esc dismiss"
	if ansi.StringWidth(help) > width {
		help = "enter · tab/arrows · esc dismiss"
	}
	lines = append(lines, q.styles.Text.Tertiary.Render(ansi.Truncate(help, width, "…")))
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
}

func (q *Questionnaire) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	x, y := msg.X-2, msg.Y
	width := max(3, q.width-4)
	if x < 0 || x >= width || y < 0 {
		return nil
	}
	if y == 0 {
		for i, tab := range q.tabs(width) {
			if x >= tab.start && x < tab.end {
				return q.showPage(i)
			}
		}
	}
	rows := q.visibleRows(width)
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
