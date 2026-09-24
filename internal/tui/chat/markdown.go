package chat

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"

	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Streaming keeps settled top-level blocks rendered and reparses only the tail.

type markdownPrefix struct {
	src string // whole top-level blocks, ending at a line start
	out string // src rendered and sanitized, without the document's last newline
}

var markdownBlockParser = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
).Parser()

// Keep the last two blocks open because later lines can still merge them.
const openMarkdownBlocks = 2

func (r *markdownRenderer) renderStreamed(src string) (string, error) {
	// Reference definitions apply to the whole document.
	if strings.Contains(src, "]:") {
		r.prefix = markdownPrefix{}
		return r.renderDocument(src)
	}
	if !strings.HasPrefix(src, r.prefix.src) {
		r.prefix = markdownPrefix{}
	}
	tail := src[len(r.prefix.src):]
	if n, spaced := settledMarkdown(tail); n > 0 {
		out, err := r.renderDocument(tail[:n])
		if err != nil {
			return "", err
		}
		out = strings.TrimSuffix(out, "\n")
		if r.prefix.src != "" {
			out = joinMarkdown(r.prefix.out, out, spaced)
		}
		r.prefix = markdownPrefix{src: src[:len(r.prefix.src)+n], out: out}
		tail = tail[n:]
	}
	if r.prefix.src == "" {
		return r.renderDocument(tail)
	}
	if tail == "" {
		return r.prefix.out + "\n", nil
	}
	out, err := r.renderDocument(tail)
	if err != nil {
		return "", err
	}
	return joinMarkdown(r.prefix.out, out, startsSpaced(tail)), nil
}

func (r *markdownRenderer) renderDocument(src string) (string, error) {
	out, err := r.term.Render(src)
	if err != nil {
		return "", err
	}
	return escape.StyledMultiline(out), nil
}

func joinMarkdown(head, next string, spaced bool) string {
	sep := ""
	if spaced {
		sep = "\n"
	}
	return head + sep + strings.TrimPrefix(next, "\n")
}

// settledMarkdown only parses complete lines so a partial line cannot close a block.
func settledMarkdown(src string) (int, bool) {
	complete := src[:strings.LastIndexByte(src, '\n')+1]
	if complete == "" {
		return 0, false
	}
	doc := markdownBlockParser.Parse(text.NewReader([]byte(complete)))
	var blocks []ast.Node
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		blocks = append(blocks, n)
	}
	for k := len(blocks) - openMarkdownBlocks; k > 0; k-- {
		if blocks[k].Kind() == ast.KindHTMLBlock || blocks[k-1].Kind() == ast.KindHTMLBlock {
			continue
		}
		if start := blockStart(blocks[k]); start > 0 {
			return strings.LastIndexByte(complete[:start], '\n') + 1, spacedBlock(blocks[0])
		}
	}
	return 0, false
}

func startsSpaced(src string) bool {
	first := markdownBlockParser.Parse(text.NewReader([]byte(src))).FirstChild()
	return first != nil && spacedBlock(first)
}

func spacedBlock(n ast.Node) bool {
	return n.Kind() == ast.KindParagraph || n.Kind() == ast.KindHeading
}

func blockStart(n ast.Node) int {
	start := -1
	keep := func(pos int) {
		if pos >= 0 && (start < 0 || pos < start) {
			start = pos
		}
	}
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if c.Type() != ast.TypeBlock {
			return ast.WalkSkipChildren, nil
		}
		keep(c.Pos())
		if c.Lines().Len() > 0 {
			keep(c.Lines().At(0).Start)
		}
		return ast.WalkContinue, nil
	})
	return start
}

// RenderMarkdown renders markdown source to ANSI using the Datadog-accented
// glamour style for the given terminal background, wrapped to width. It is the
// exported entry point for dev tooling (the style catalog) that needs the same
// markdown rendering the transcript uses.
func RenderMarkdown(src string, width int, isDark bool) string {
	return renderMarkdown(src, width, markdownStyleConfig(isDark))
}

func markdownStyleConfig(isDark bool) ansi.StyleConfig {
	return styles.Default(isDark).Chat.Markdown
}

// renderMarkdown renders markdown source to ANSI using the Datadog-accented
// glamour style for the given palette, wrapped to width, with glamour's
// surrounding blank lines trimmed so blocks join cleanly.
func renderMarkdown(src string, width int, style ansi.StyleConfig) string {
	var r markdownRenderer
	return r.Render(src, width, style)
}

type markdownRenderer struct {
	term   *glamour.TermRenderer
	width  int
	style  ansi.StyleConfig
	ready  bool
	prefix markdownPrefix
}

// Render reuses Glamour's parsed style and Goldmark pipeline until wrapping or
// the terminal theme changes. Failed setup is cached too, avoiding repeated
// setup attempts while the same fallback configuration remains active.
func (r *markdownRenderer) Render(src string, width int, style ansi.StyleConfig) string {
	src = escape.MarkdownSource(src)
	if width < 1 {
		width = 1
	}
	if !r.ready || width != r.width || style != r.style {
		r.term, _ = glamour.NewTermRenderer(
			glamour.WithStyles(style),
			glamour.WithWordWrap(width),
			glamour.WithInlineTableLinks(true),
		)
		r.width = width
		r.style = style
		r.ready = true
		r.prefix = markdownPrefix{}
	}
	if r.term == nil {
		return wrap(escape.Multiline(src), width)
	}
	out, err := r.renderStreamed(src)
	if err != nil {
		r.term = nil
		r.ready = false
		r.prefix = markdownPrefix{}
		return wrap(escape.Multiline(src), width)
	}
	return strings.Trim(out, "\n")
}
