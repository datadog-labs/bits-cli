package editor

import "testing"

var (
	benchmarkEditorHeight int
	benchmarkEditorView   string
)

func BenchmarkEditorHeightAndViewAfterInvalidation(b *testing.B) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "single_line", value: "Show me error logs from the checkout service"},
		{
			name: "multiline",
			value: "Investigate the checkout latency regression.\n" +
				"Compare it with the previous deployment.\n" +
				"Focus on the slowest endpoints.\n" +
				"Include the affected regions.\n" +
				"Check database and cache timings.\n" +
				"Summarize the likely root cause.\n" +
				"List the supporting evidence.\n" +
				"Suggest the next debugging steps.",
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			e := New()
			e.SetWidth(80)
			e.ta.SetValue(tc.value)
			placeholders := [...]string{"Working on it…", "Ask Bits…"}
			i := 0

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				e.SetPlaceholder(placeholders[i&1])
				benchmarkEditorHeight = e.Height()
				benchmarkEditorView = e.View()
				i++
			}
		})
	}
}
