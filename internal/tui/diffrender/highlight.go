package diffrender

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

// lexerCache memoizes filename matches.
var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}
)

func matchLexer(name string) chroma.Lexer {
	lexerCacheMu.RLock()
	cached, ok := lexerCache[name]
	lexerCacheMu.RUnlock()
	if ok {
		return cached
	}
	lexer := lexers.Match(name)
	if lexer != nil {
		lexer = chroma.Coalesce(lexer)
	}
	lexerCacheMu.Lock()
	lexerCache[name] = lexer
	lexerCacheMu.Unlock()
	return lexer
}

// Highlight styles text as one lexer document, so constructs spanning lines
// such as heredocs keep their state, and returns one segment slice per source
// line. Text is escaped for the terminal and tabs are expanded, so the result
// can go straight to textwrap.
func Highlight(path, text string, dark bool, base lipgloss.Style) [][]textwrap.Segment {
	rows := highlight(path, text, dark, base, true)
	want := strings.Count(text, "\n") + 1
	if len(rows) > want {
		rows = rows[:want]
	}
	for len(rows) < want {
		rows = append(rows, nil)
	}
	return rows
}

// highlightLine styles one source line on its own.
func highlightLine(path, text string, dark bool, base lipgloss.Style) []textwrap.Segment {
	return highlight(path, text, dark, base, false)[0]
}

// highlight returns the styled tokens of text, split into rows at newlines
// when multiline is set.
func highlight(path, text string, dark bool, base lipgloss.Style, multiline bool) [][]textwrap.Segment {
	lexer := matchLexer(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	theme := chromaStyle(dark)
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		// Unstyled by the theme, but still escaped and split.
		iterator, theme = chroma.Literator(chroma.Token{Type: chroma.Text, Value: text}), nil
	}
	rows := [][]textwrap.Segment{nil}
	for token := iterator(); token != chroma.EOF; token = iterator() {
		style := base
		if theme != nil {
			if colour := theme.Get(token.Type).Colour; colour.IsSet() {
				style = base.Foreground(lipgloss.Color(colour.String()))
			}
		}
		values := []string{processValue(token.Value)}
		if multiline {
			values = strings.Split(escape.Multiline(token.Value), "\n")
		}
		for i, value := range values {
			if i > 0 {
				rows = append(rows, nil)
			}
			if value != "" {
				rows[len(rows)-1] = append(rows[len(rows)-1], textwrap.Styled(value, style))
			}
		}
	}
	return rows
}

func chromaStyle(dark bool) *chroma.Style {
	if dark {
		return chromastyles.Get("dracula")
	}
	return chromastyles.Get("github")
}

// processValue makes token text safe for one diff row.
func processValue(value string) string {
	value = strings.TrimSuffix(value, "\n")
	value = escape.RenderTabs(value)
	return escape.Inline(value)
}
