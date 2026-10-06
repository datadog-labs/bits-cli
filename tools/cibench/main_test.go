package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseBenchmarksAggregatesSampleMedians(t *testing.T) {
	input := `pkg: github.com/datadog-labs/bits-cli/internal/agent
BenchmarkTranscriptStreaming/fragments_800-1  5  240 ns/op  120 B/op  3 allocs/op  61 terminal-writes/op
BenchmarkTranscriptStreaming/fragments_800-1  5  200 ns/op  100 B/op  2 allocs/op  57 terminal-writes/op
BenchmarkTranscriptStreaming/fragments_800-1  5  220 ns/op  110 B/op  2 allocs/op  59 terminal-writes/op
`

	got, _, err := parseBenchmarks(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse benchmarks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("summaries = %d, want 1", len(got))
	}
	wantMetrics := map[string]float64{
		"ns_per_op":              220,
		"bytes_per_op":           110,
		"allocs_per_op":          2,
		"terminal_writes_per_op": 59,
	}
	if got[0].Package != "github.com/datadog-labs/bits-cli/internal/agent" {
		t.Fatalf("package = %q", got[0].Package)
	}
	if got[0].Name != "BenchmarkTranscriptStreaming/fragments_800" {
		t.Fatalf("name = %q", got[0].Name)
	}
	if got[0].Samples != 3 {
		t.Fatalf("samples = %d, want 3", got[0].Samples)
	}
	if diff := compareMetrics(got[0].Metrics, wantMetrics); diff != "" {
		t.Fatal(diff)
	}
}

func TestBuildMeasuresCreatesStableCIVisibilityNames(t *testing.T) {
	summaries := []benchmarkSummary{{
		Package: "github.com/datadog-labs/bits-cli/internal/agent",
		Name:    "BenchmarkTranscriptStreaming/fragments_800",
		Samples: 7,
		Metrics: map[string]float64{
			"ns_per_op":    220,
			"bytes_per_op": 110,
		},
	}}

	want := map[string]float64{
		"bits_cli.benchmark.internal.agent.transcript_streaming.fragments_800.samples":      7,
		"bits_cli.benchmark.internal.agent.transcript_streaming.fragments_800.ns_per_op":    220,
		"bits_cli.benchmark.internal.agent.transcript_streaming.fragments_800.bytes_per_op": 110,
	}
	if diff := compareMetrics(buildMeasures(summaries), want); diff != "" {
		t.Fatal(diff)
	}
}

func TestRunWritesArtifactMeasuresAndRunnerTags(t *testing.T) {
	t.Setenv("CI_COMMIT_SHA", "abc123")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	t.Setenv("CI_PIPELINE_ID", "42")
	t.Setenv("CI_JOB_ID", "84")
	t.Setenv("DDCI_REQUEST_KIND", "REQUEST_KIND_INTEGRATION_REQUEST")
	t.Setenv("CI_RUNNER_ID", "12345")
	t.Setenv("CI_RUNNER_DESCRIPTION", "kubernetes-runner")
	t.Setenv("CI_RUNNER_TAGS", `["arch:arm64","kubernetes"]`)
	t.Setenv("CI_RUNNER_VERSION", "18.2.0")
	t.Setenv("CI_RUNNER_EXECUTABLE_ARCH", "linux/arm64")
	t.Setenv("KUBERNETES_CPU_REQUEST", "1")
	t.Setenv("KUBERNETES_CPU_LIMIT", "1")
	t.Setenv("KUBERNETES_MEMORY_REQUEST", "2Gi")
	t.Setenv("KUBERNETES_MEMORY_LIMIT", "2Gi")
	t.Setenv("GOMAXPROCS", "1")

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "benchmark.txt")
	outputPath := filepath.Join(dir, "benchmark.json")
	measuresPath := filepath.Join(dir, "benchmark-measures.json")
	tagsPath := filepath.Join(dir, "benchmark-tags.json")
	input := `goos: linux
goarch: arm64
pkg: github.com/datadog-labs/bits-cli/internal/agent
cpu: AWS Graviton4
BenchmarkTranscriptStreaming/fragments_800-1  5  220 ns/op
`
	if err := os.WriteFile(inputPath, []byte(input), 0o644); err != nil {
		t.Fatalf("write benchmark input: %v", err)
	}
	if err := run(inputPath, outputPath, measuresPath, tagsPath); err != nil {
		t.Fatalf("run reporter: %v", err)
	}
	payload, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read benchmark artifact: %v", err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(payload, &artifact); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	benchmarks, ok := artifact["benchmarks"].([]any)
	if !ok || len(benchmarks) != 1 {
		t.Fatalf("benchmarks = %#v, want one", artifact["benchmarks"])
	}
	metadata, ok := artifact["metadata"].(map[string]any)
	if !ok || metadata["git_sha"] != "abc123" || metadata["git_ref"] != "main" {
		t.Fatalf("metadata = %#v", artifact["metadata"])
	}
	wantRunner := map[string]any{
		"id":              "12345",
		"description":     "kubernetes-runner",
		"tags":            []any{"arch:arm64", "kubernetes"},
		"version":         "18.2.0",
		"executable_arch": "linux/arm64",
		"cpu_model":       "AWS Graviton4",
		"cpu_request":     "1",
		"cpu_limit":       "1",
		"memory_request":  "2Gi",
		"memory_limit":    "2Gi",
		"gomaxprocs":      "1",
	}
	if got := metadata["runner"]; !reflect.DeepEqual(got, wantRunner) {
		t.Fatalf("runner metadata = %#v, want %#v", got, wantRunner)
	}

	measuresJSON, err := os.ReadFile(measuresPath)
	if err != nil {
		t.Fatalf("read measures: %v", err)
	}
	var measures map[string]float64
	if err := json.Unmarshal(measuresJSON, &measures); err != nil {
		t.Fatalf("decode measures: %v", err)
	}
	wantMeasures := map[string]float64{
		"bits_cli.benchmark.internal.agent.transcript_streaming.fragments_800.samples":   1,
		"bits_cli.benchmark.internal.agent.transcript_streaming.fragments_800.ns_per_op": 220,
	}
	if diff := compareMetrics(measures, wantMeasures); diff != "" {
		t.Fatal(diff)
	}

	tagsJSON, err := os.ReadFile(tagsPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	var tags map[string]string
	if err := json.Unmarshal(tagsJSON, &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	wantTags := map[string]string{
		"bits_cli.benchmark.ddci_request_kind":      "REQUEST_KIND_INTEGRATION_REQUEST",
		"bits_cli.benchmark.go_version":             runtime.Version(),
		"bits_cli.benchmark.goarch":                 runtime.GOARCH,
		"bits_cli.benchmark.goos":                   runtime.GOOS,
		"bits_cli.benchmark.runner.cpu_limit":       "1",
		"bits_cli.benchmark.runner.cpu_model":       "AWS Graviton4",
		"bits_cli.benchmark.runner.cpu_request":     "1",
		"bits_cli.benchmark.runner.description":     "kubernetes-runner",
		"bits_cli.benchmark.runner.executable_arch": "linux/arm64",
		"bits_cli.benchmark.runner.gomaxprocs":      "1",
		"bits_cli.benchmark.runner.id":              "12345",
		"bits_cli.benchmark.runner.memory_limit":    "2Gi",
		"bits_cli.benchmark.runner.memory_request":  "2Gi",
		"bits_cli.benchmark.runner.tags":            "arch:arm64,kubernetes",
		"bits_cli.benchmark.runner.version":         "18.2.0",
	}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Fatalf("tags = %#v, want %#v", tags, wantTags)
	}
}

func compareMetrics(got, want map[string]float64) string {
	if len(got) != len(want) {
		return "metric count differs"
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			return "metric " + name + " differs"
		}
	}
	return ""
}
