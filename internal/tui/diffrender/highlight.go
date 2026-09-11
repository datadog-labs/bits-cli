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

// highlight safely styles one source line.
func highlight(path, text string, dark bool, rowStyle lipgloss.Style) string {
	lexer := matchLexer(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return rowStyle.Render(processValue(text))
	}
	var b strings.Builder
	if err := formatter(rowStyle).Format(&b, chromaStyle(dark), iterator); err != nil {
		return rowStyle.Render(processValue(text))
	}
	return b.String()
}

func chromaStyle(dark bool) *chroma.Style {
	if dark {
		return chromastyles.Get("dracula")
	}
	return chromastyles.Get("github")
}

// formatter styles syntax tokens.
func formatter(rowStyle lipgloss.Style) chroma.Formatter {
	return chroma.FormatterFunc(func(w io.Writer, style *chroma.Style, iterator chroma.Iterator) error {
		for token := iterator(); token != chroma.EOF; token = iterator() {
			value := processValue(token.Value)
			if value == "" {
				continue
			}
			entry := style.Get(token.Type)
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
		return nil
	})
}

// processValue makes token text safe for one diff row.
func processValue(value string) string {
	value = strings.TrimSuffix(value, "\n")
	value = escape.RenderTabs(value)
	return escape.Inline(value)
}
