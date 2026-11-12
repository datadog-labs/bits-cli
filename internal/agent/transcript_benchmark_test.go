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

func BenchmarkTranscriptInterleavedStreaming(b *testing.B) {
	for _, fragments := range []int{800, 3200} {
		b.Run("fragments_"+strconv.Itoa(fragments), func(b *testing.B) {
			messages := []assistant.Message{
				assistant.AssistantMessage("first", assistant.TextContent("stream ")),
				assistant.AssistantMessage("second", assistant.TextContent("stream ")),
			}
			b.ReportAllocs()
			for range b.N {
				tr := NewTranscript()
				for i := range fragments {
					tr.run = RunID(i%2 + 1)
					tr.AppendMessage(messages[i%2])
				}
				tr.FinalizeAll()
				benchmarkTranscriptContent = tr.Blocks()[0].Markdown.Content
			}
			if len(benchmarkTranscriptContent) != fragments/2*len("stream ") {
				b.Fatal("lost interleaved content")
			}
		})
	}
}
