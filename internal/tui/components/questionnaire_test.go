package components

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

func newQuestionnaire(questions ...Question) *Questionnaire {
	q := NewQuestionnaire(questions)
	q.SetStyles(styles.Default(true))
	q.SetSize(80, 24)
	return q
}

func twoQuestions() *Questionnaire {
	return newQuestionnaire(
		Question{Prompt: "Which region?", Options: []Option{{"US", "US region"}, {"EU", "EU region"}}},
		Question{Prompt: "Which service?", Options: []Option{{"API", "Public API"}}},
	)
}

func press(q *Questionnaire, code rune, mod tea.KeyMod) {
	q.Update(tea.KeyPressMsg{Code: code, Mod: mod})
}

func done(q *Questionnaire) bool {
	_, _, ok := q.Answers()
	return ok
}

func TestQuestionnaireShortcutsKeepDraftsAndRequireConfirmation(t *testing.T) {
	q := twoQuestions()
	press(q, '2', 0)
	if q.choices[0] != 1 || q.answers[0] != "" {
		t.Fatal("number shortcut must highlight without confirming an answer")
	}
	press(q, '3', 0)
	if !q.editing || !q.text.Focused() {
		t.Fatal("Other did not focus immediately")
	}
	for _, key := range "12[]jk" {
		q.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
	}
	if q.text.Value() != "12[]jk" || q.page != 0 {
		t.Fatalf("shortcuts stole custom text: %q", q.text.Value())
	}
	press(q, tea.KeyTab, 0)
	press(q, tea.KeyTab, tea.ModShift)
	if !q.editing || q.text.Value() != "12[]jk" || q.answers[0] != "" {
		t.Fatal("page navigation lost or confirmed a draft")
	}
	press(q, tea.KeyUp, 0) // leave Other
	press(q, 'k', 0)       // US
	press(q, 'j', 0)       // EU
	if q.choices[0] != 1 || q.editing {
		t.Fatal("choice navigation failed after leaving Other")
	}
	press(q, tea.KeyDown, 0)
	if q.text.Value() != "12[]jk" {
		t.Fatal("custom draft was lost when choosing another option")
	}
	press(q, tea.KeyTab, 0)
	press(q, ']', 0) // review, without confirming either answer
	press(q, tea.KeyEnter, 0)
	if q.page != 0 || done(q) || q.answers[0] != "" || q.answers[1] != "" {
		t.Fatal("navigation implicitly submitted a choice or draft")
	}
}

func TestQuestionnaireMouseTabsChoicesAndReview(t *testing.T) {
	q := twoQuestions()
	clickText := func(text string) {
		t.Helper()
		for y, row := range strings.Split(ansi.Strip(q.View()), "\n") {
			if offset := strings.Index(row, text); offset >= 0 {
				q.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth(row[:offset]), Y: y})
				return
			}
		}
		t.Fatalf("click target %q not visible", text)
	}
	clickText("2. EU")
	if q.choices[0] != 1 || q.answers[0] != "" {
		t.Fatal("click should highlight a choice without submitting")
	}
	press(q, tea.KeyEnter, 0)
	clickText("2. Other")
	if !q.editing {
		t.Fatal("clicking Other did not focus text input")
	}
	q.Update(tea.PasteMsg{Content: "billing-worker"})
	press(q, tea.KeyEnter, 0)
	clickText("billing-worker") // review row
	if q.page != 1 || !q.editing || q.text.Value() != "billing-worker" {
		t.Fatal("review click did not reopen the saved custom answer")
	}
	clickText("✓ Review")
	if q.page != 2 || done(q) {
		t.Fatal("tab click failed or submitted the form")
	}
	press(q, tea.KeyEnter, 0)
	if answers, dismissed, ok := q.Answers(); !ok || dismissed || !slices.Equal(answers, []string{"EU", "billing-worker"}) {
		t.Fatalf("answers = %q dismissed=%v done=%v", answers, dismissed, ok)
	}
}

func TestQuestionnaireFiveQuestionsAndLongOptions(t *testing.T) {
	questions := make([]Question, 5)
	for i := range questions {
		questions[i] = Question{Prompt: fmt.Sprintf("Question %d?", i+1), Options: make([]Option, 5)}
		for j := range questions[i].Options {
			questions[i].Options[j] = Option{Label: fmt.Sprintf("Option %d", j+1), Description: strings.Repeat("A long explanation. ", 10)}
		}
	}
	q := newQuestionnaire(questions...)
	q.SetStyles(styles.Default(true))
	q.SetSize(36, 14)
	for i := range 5 {
		press(q, tea.KeyUp, 0)    // Other, after all five options
		press(q, tea.KeyEnter, 0) // blank Other must stay unanswered
		if !q.editing || q.page != i {
			t.Fatal("blank custom answer was submitted")
		}
		q.Update(tea.PasteMsg{Content: fmt.Sprintf("Custom %d", i+1)})
		view := ansi.Strip(q.View())
		if !strings.Contains(view, fmt.Sprintf("Custom %d", i+1)) {
			t.Fatalf("custom input hidden:\n%s", view)
		}
		press(q, tea.KeyTab, 0) // save draft, not answer
		press(q, tea.KeyTab, tea.ModShift)
		if !q.editing || q.text.Value() != fmt.Sprintf("Custom %d", i+1) {
			t.Fatal("custom draft lost on navigation")
		}
		press(q, tea.KeyEnter, 0)
	}
	for range 5 {
		press(q, tea.KeyPgDown, 0)
	}
	if view := ansi.Strip(q.View()); !strings.Contains(view, "Custom 5") {
		t.Fatalf("review cannot scroll to the last answer:\n%s", view)
	}
	press(q, tea.KeyEnter, 0)
	if answers, _, ok := q.Answers(); !ok || len(answers) != 5 || answers[4] != "Custom 5" {
		t.Fatalf("answers = %q done=%v", answers, ok)
	}
}

func TestQuestionnaireLongCustomTextAndTabsFitBothThemes(t *testing.T) {
	q := twoQuestions()
	press(q, tea.KeyUp, 0)
	q.Update(tea.PasteMsg{Content: strings.Repeat("東京🙂", 50)})
	for _, dark := range []bool{true, false} {
		for _, size := range [][2]int{{36, 14}, {40, 16}, {80, 24}, {120, 40}} {
			q.SetStyles(styles.Default(dark))
			q.SetSize(size[0], size[1])
			view := q.View()
			if strings.Count(view, "\n")+1 != q.Height() {
				t.Fatalf("custom input changes the form height at %v:\n%s", size, ansi.Strip(view))
			}
			for _, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatalf("custom input exceeds width at %v: %q", size, ansi.Strip(row))
				}
			}
		}
	}
}
