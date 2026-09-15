package diffrender

import (
	"io"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/DataDog/bits-cli/internal/tui/escape"
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

// HighlightLine safely styles one source line using the lexer matched by path.
func HighlightLine(path, text string, dark bool, rowStyle lipgloss.Style) string {
	return highlight(path, text, dark, rowStyle, false)
}

// HighlightLines safely styles text as one lexer document, then returns its
// source rows. Tokenising before splitting preserves multiline lexer state.
func HighlightLines(path, text string, dark bool, rowStyle lipgloss.Style) []string {
	rendered := strings.TrimSuffix(highlight(path, text, dark, rowStyle, true), "\n")
	rows := strings.Split(rendered, "\n")
	want := strings.Count(text, "\n") + 1
	if len(rows) > want {
		rows = rows[:want]
	}
	for len(rows) < want {
		rows = append(rows, "")
	}
	return rows
}

func highlight(path, text string, dark bool, rowStyle lipgloss.Style, multiline bool) string {
	lexer := matchLexer(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return fallbackHighlight(text, rowStyle, multiline)
	}
	var b strings.Builder
	if err := formatter(rowStyle, multiline).Format(&b, chromaStyle(dark), iterator); err != nil {
		return fallbackHighlight(text, rowStyle, multiline)
	}
	return b.String()
}

func fallbackHighlight(text string, rowStyle lipgloss.Style, multiline bool) string {
	if !multiline {
		return rowStyle.Render(processValue(text))
	}
	rows := strings.Split(escape.Multiline(text), "\n")
	for i := range rows {
		rows[i] = rowStyle.Render(rows[i])
	}
	return strings.Join(rows, "\n")
}

func chromaStyle(dark bool) *chroma.Style {
	if dark {
		return chromastyles.Get("dracula")
	}
	return chromastyles.Get("github")
}

// formatter styles syntax tokens, optionally preserving source line breaks.
func formatter(rowStyle lipgloss.Style, multiline bool) chroma.Formatter {
	return chroma.FormatterFunc(func(w io.Writer, style *chroma.Style, iterator chroma.Iterator) error {
		for token := iterator(); token != chroma.EOF; token = iterator() {
			entry := style.Get(token.Type)
			values := []string{processValue(token.Value)}
			if multiline {
				values = strings.Split(escape.Multiline(token.Value), "\n")
			}
			for i, value := range values {
				if i > 0 {
					if _, err := io.WriteString(w, "\n"); err != nil {
						return err
					}
				}
				if value == "" {
					continue
				}
				if entry.Colour.IsSet() {
					if _, err := io.WriteString(w, rowStyle.Foreground(lipgloss.Color(entry.Colour.String())).Render(value)); err != nil {
						return err
					}
					continue
				}
				if _, err := io.WriteString(w, rowStyle.Render(value)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// processValue makes token text safe for one diff row.
func processValue(value string) string {
	value = strings.TrimSuffix(value, "\n")
	value = escape.RenderTabs(value)
	return escape.Inline(value)
}
