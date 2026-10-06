package agent

import (
	"strconv"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

var benchmarkTranscriptContent string

func BenchmarkTranscriptStreaming(b *testing.B) {
	const fragment = "stream "
	for _, fragments := range []int{50, 200, 800} {
		b.Run("fragments_"+strconv.Itoa(fragments), func(b *testing.B) {
			message := assistant.AssistantMessage("answer", assistant.TextContent(fragment))
			wantLength := fragments * len(fragment)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				transcript := NewTranscript()
				for range fragments {
					transcript.AppendMessage(message)
				}
				transcript.FinalizeAll()
				benchmarkTranscriptContent = transcript.Blocks()[0].Markdown.Content
			}
			b.StopTimer()
			if len(benchmarkTranscriptContent) != wantLength {
				b.Fatalf("content length = %d, want %d", len(benchmarkTranscriptContent), wantLength)
			}
		})
	}
}
