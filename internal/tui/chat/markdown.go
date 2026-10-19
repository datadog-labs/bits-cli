package chat

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	astext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"charm.land/glamour/v2/ansi"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
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
	term   *markdownTerm
	width  int
	style  ansi.StyleConfig
	ready  bool
	prefix markdownPrefix
}

func (r *markdownRenderer) Render(src string, width int, style ansi.StyleConfig) string {
	src = escape.MarkdownSource(src)
	if width < 1 {
		width = 1
	}
	if !r.ready || width != r.width || style != r.style {
		r.term = newMarkdownTerm(width, style)
		r.width = width
		r.style = style
		r.ready = true
		r.prefix = markdownPrefix{}
	}
	if r.term == nil {
		return textwrap.WrapProseText(width, escape.Multiline(src), nil)
	}
	out, err := r.renderStreamed(src)
	if err != nil {
		r.term = nil
		r.ready = false
		r.prefix = markdownPrefix{}
		return textwrap.WrapProseText(width, escape.Multiline(src), nil)
	}
	return strings.Trim(out, "\n")
}

// Glamour has no option to hide link URLs; replace links before rendering.
type markdownTerm struct {
	md      goldmark.Markdown
	ansi    *ansi.ANSIRenderer
	options ansi.Options
}

func newMarkdownTerm(width int, style ansi.StyleConfig) *markdownTerm {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	// Like glamour, replace the whole renderer so extensions add no HTML renderers.
	options := ansi.Options{WordWrap: width, Styles: style, InlineTableLinks: true}
	ar := ansi.NewRenderer(options)
	md.SetRenderer(renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(ar, 1000))))
	return &markdownTerm{md: md, ansi: ar, options: options}
}

func (t *markdownTerm) Render(src string) (string, error) {
	source := []byte(src)
	doc := t.md.Parser().Parse(text.NewReader(source))
	source, err := t.replaceLinks(doc, source)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.md.Renderer().Render(&b, source, doc); err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return b.String(), nil
}

// Append rendered titles to source so existing node offsets stay valid.
func (t *markdownTerm) replaceLinks(doc ast.Node, source []byte) ([]byte, error) {
	var links []*ast.Link
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		// Glamour reads these nodes as plain text, bypassing child rendering.
		if entering && (n.Kind() == ast.KindImage || n.Kind() == astext.KindStrikethrough) {
			return ast.WalkSkipChildren, nil
		}
		if l, ok := n.(*ast.Link); ok && entering {
			links = append(links, l)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, l := range links {
		start := len(source)
		title, err := t.title(l, source)
		if err != nil {
			return nil, err
		}
		source = append(source, title...)
		repl := ast.NewTextSegment(text.NewSegment(start, len(source)))
		l.Parent().ReplaceChild(l.Parent(), l, repl)
	}
	return source, nil
}

func (t *markdownTerm) title(l *ast.Link, source []byte) (string, error) {
	// Autolink titles must inherit the outer link's destination.
	var autolinks []*ast.AutoLink
	_ = ast.Walk(l, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if a, ok := n.(*ast.AutoLink); ok && entering {
			autolinks = append(autolinks, a)
		}
		return ast.WalkContinue, nil
	})
	for _, a := range autolinks {
		start := len(source)
		source = append(source, a.Label(source)...)
		a.Parent().ReplaceChild(a.Parent(), a, ast.NewTextSegment(text.NewSegment(start, len(source))))
	}
	element := t.ansi.NewElement(l, source).Renderer
	link, ok := element.(*ansi.LinkElement)
	if !ok || link == nil {
		return "", fmt.Errorf("unexpected Glamour link renderer: %T", element)
	}
	link.SkipHref = true
	ctx := ansi.NewRenderContext(t.options)
	// Recreate block styles so resets inside the title retain heading backgrounds.
	var ancestors []ast.Node
	for n := l.Parent(); n != nil; n = n.Parent() {
		if n.Type() != ast.TypeInline {
			ancestors = append(ancestors, n)
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		if r := t.ansi.NewElement(ancestors[i], source).Renderer; r != nil {
			switch r.(type) {
			case *ansi.TableElement, *ansi.TableCellElement:
				continue
			}
			if err := r.Render(io.Discard, ctx); err != nil {
				return "", err
			}
		}
	}
	var b bytes.Buffer
	if err := link.Render(&b, ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(xansi.Strip(b.String())) == "" {
		b.Reset()
		link.Children = []ansi.ElementRenderer{&ansi.BaseElement{Token: string(l.Destination)}}
		if err := link.Render(&b, ctx); err != nil {
			return "", err
		}
	}
	// The replacement text passes through Glamour's entity and escape decoding again.
	return strings.NewReplacer(`&`, `&amp;`, `\`, `\\`).Replace(b.String()), nil
}
