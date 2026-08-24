package chat

import (
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
