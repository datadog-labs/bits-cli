package chat

import (
	"fmt"
	"strings"
	"testing"
)

var benchmarkMarkdown string

func BenchmarkMarkdownRendererStableConfig(b *testing.B) {
	const source = "## Streaming answer\n\n- one item\n- [linked item](https://example.com)\n\n`inline code`"
	style := markdownStyleConfig(true)
	cases := []struct {
		name   string
		source string
	}{
		{name: "short", source: source},
		{name: "medium", source: strings.Repeat(source+"\n\n", 12)},
		{name: "long", source: strings.Repeat(source+"\n\n", 48)},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			var r markdownRenderer
			benchmarkMarkdown = r.Render(tc.source, 80, style)

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkMarkdown = r.Render(tc.source, 80, style)
			}
		})
	}
}

func BenchmarkMarkdownRendererStreaming(b *testing.B) {
	style := markdownStyleConfig(true)
	doc := generatedMarkdown(40)
	const fragment = 500
	b.ReportAllocs()
	for b.Loop() {
		var r markdownRenderer
		for n := fragment; n < len(doc)+fragment; n += fragment {
			benchmarkMarkdown = r.Render(doc[:min(n, len(doc))], 80, style)
		}
	}
}

func BenchmarkMarkdownLinkedTable(b *testing.B) {
	for _, rows := range []int{400, 800, 1600} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			src := "| link |\n|---|\n" + strings.Repeat("| [docs](https://example.com) |\n", rows)
			r := newMarkdownTerm(80, markdownStyleConfig(true))
			b.ReportAllocs()
			for b.Loop() {
				var err error
				benchmarkMarkdown, err = r.Render(src)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
