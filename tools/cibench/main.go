// Command cibench converts Go benchmark output into structured CI artifacts.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const measurePrefix = "bits_cli.benchmark"

type benchmarkSummary struct {
	Package string             `json:"go_package"`
	Name    string             `json:"benchmark"`
	Samples int                `json:"samples"`
	Metrics map[string]float64 `json:"metrics"`
}

type runMetadata struct {
	CommitSHA   string         `json:"git_sha,omitempty"`
	CommitRef   string         `json:"git_ref,omitempty"`
	PipelineID  string         `json:"ci_pipeline_id,omitempty"`
	JobID       string         `json:"ci_job_id,omitempty"`
	RequestKind string         `json:"ddci_request_kind,omitempty"`
	GoVersion   string         `json:"go_version"`
	GOOS        string         `json:"goos"`
	GOARCH      string         `json:"goarch"`
	Runner      runnerMetadata `json:"runner"`
}

type runnerMetadata struct {
	ID             string   `json:"id,omitempty"`
	Description    string   `json:"description,omitempty"`
	Tags           []string `json:"tags"`
	Version        string   `json:"version,omitempty"`
	ExecutableArch string   `json:"executable_arch,omitempty"`
	CPUModel       string   `json:"cpu_model,omitempty"`
	CPURequest     string   `json:"cpu_request,omitempty"`
	CPULimit       string   `json:"cpu_limit,omitempty"`
	MemoryRequest  string   `json:"memory_request,omitempty"`
	MemoryLimit    string   `json:"memory_limit,omitempty"`
	GOMAXPROCS     string   `json:"gomaxprocs,omitempty"`
}

type benchmarkSamples struct {
	Package string
	Name    string
	Samples int
	Metrics map[string][]float64
}

type benchmarkArtifact struct {
	Benchmarks []benchmarkSummary `json:"benchmarks"`
	Metadata   runMetadata        `json:"metadata"`
}

func main() {
	input := flag.String("input", "benchmark.txt", "Go benchmark output")
	output := flag.String("output", "benchmark.json", "structured benchmark artifact")
	measuresOutput := flag.String("measures-output", "benchmark-measures.json", "CI Visibility measures")
	tagsOutput := flag.String("tags-output", "benchmark-tags.json", "CI Visibility tags")
	flag.Parse()

	if err := run(*input, *output, *measuresOutput, *tagsOutput); err != nil {
		log.Fatal(err)
	}
}

func run(inputPath, outputPath, measuresPath, tagsPath string) error {
	input, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open benchmark output: %w", err)
	}
	summaries, cpuModel, parseErr := parseBenchmarks(input)
	closeErr := input.Close()
	if err := errors.Join(parseErr, closeErr); err != nil {
		return err
	}
	if len(summaries) == 0 {
		return errors.New("benchmark output contains no results")
	}

	metadata := runMetadata{
		CommitSHA:   os.Getenv("CI_COMMIT_SHA"),
		CommitRef:   os.Getenv("CI_COMMIT_REF_NAME"),
		PipelineID:  os.Getenv("CI_PIPELINE_ID"),
		JobID:       os.Getenv("CI_JOB_ID"),
		RequestKind: os.Getenv("DDCI_REQUEST_KIND"),
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		Runner:      runnerMetadataFromEnvironment(cpuModel),
	}
	if err := writeJSON(outputPath, benchmarkArtifact{
		Benchmarks: summaries,
		Metadata:   metadata,
	}); err != nil {
		return fmt.Errorf("write structured benchmark artifact: %w", err)
	}
	if err := writeJSON(measuresPath, buildMeasures(summaries)); err != nil {
		return fmt.Errorf("write CI Visibility measures: %w", err)
	}
	if err := writeJSON(tagsPath, buildTags(metadata)); err != nil {
		return fmt.Errorf("write CI Visibility tags: %w", err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

func parseBenchmarks(r io.Reader) ([]benchmarkSummary, string, error) {
	scanner := bufio.NewScanner(r)
	currentPackage := ""
	cpuModel := ""
	byKey := make(map[string]*benchmarkSamples)
	order := make([]string, 0)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "pkg:") {
			currentPackage = strings.TrimSpace(strings.TrimPrefix(line, "pkg:"))
			continue
		}
		if strings.HasPrefix(line, "cpu:") {
			cpuModel = strings.TrimSpace(strings.TrimPrefix(line, "cpu:"))
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 || (len(fields)-2)%2 != 0 {
			return nil, "", fmt.Errorf("malformed benchmark result %q", line)
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			return nil, "", fmt.Errorf("parse iteration count in %q: %w", line, err)
		}
		name := trimCPUSuffix(fields[0])
		key := currentPackage + "\x00" + name
		samples, ok := byKey[key]
		if !ok {
			samples = &benchmarkSamples{
				Package: currentPackage,
				Name:    name,
				Metrics: make(map[string][]float64),
			}
			byKey[key] = samples
			order = append(order, key)
		}
		samples.Samples++
		for i := 2; i < len(fields); i += 2 {
			value, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, "", fmt.Errorf("parse metric value in %q: %w", line, err)
			}
			metric := normalizeMetric(fields[i+1])
			samples.Metrics[metric] = append(samples.Metrics[metric], value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("read benchmark output: %w", err)
	}

	summaries := make([]benchmarkSummary, 0, len(order))
	for _, key := range order {
		sample := byKey[key]
		metrics := make(map[string]float64, len(sample.Metrics))
		for name, values := range sample.Metrics {
			metrics[name] = median(values)
		}
		summaries = append(summaries, benchmarkSummary{
			Package: sample.Package,
			Name:    sample.Name,
			Samples: sample.Samples,
			Metrics: metrics,
		})
	}
	return summaries, cpuModel, nil
}

func runnerMetadataFromEnvironment(cpuModel string) runnerMetadata {
	tags := make([]string, 0)
	_ = json.Unmarshal([]byte(os.Getenv("CI_RUNNER_TAGS")), &tags)
	return runnerMetadata{
		ID:             os.Getenv("CI_RUNNER_ID"),
		Description:    os.Getenv("CI_RUNNER_DESCRIPTION"),
		Tags:           tags,
		Version:        os.Getenv("CI_RUNNER_VERSION"),
		ExecutableArch: os.Getenv("CI_RUNNER_EXECUTABLE_ARCH"),
		CPUModel:       cpuModel,
		CPURequest:     os.Getenv("KUBERNETES_CPU_REQUEST"),
		CPULimit:       os.Getenv("KUBERNETES_CPU_LIMIT"),
		MemoryRequest:  os.Getenv("KUBERNETES_MEMORY_REQUEST"),
		MemoryLimit:    os.Getenv("KUBERNETES_MEMORY_LIMIT"),
		GOMAXPROCS:     os.Getenv("GOMAXPROCS"),
	}
}

func trimCPUSuffix(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i < 0 || i == len(name)-1 {
		return name
	}
	for _, r := range name[i+1:] {
		if !unicode.IsDigit(r) {
			return name
		}
	}
	return name[:i]
}

func normalizeMetric(unit string) string {
	switch unit {
	case "ns/op":
		return "ns_per_op"
	case "B/op":
		return "bytes_per_op"
	case "allocs/op":
		return "allocs_per_op"
	}
	unit = strings.ReplaceAll(unit, "/", " per ")

	var b strings.Builder
	separator := false
	for _, r := range unit {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			separator = false
		} else {
			separator = true
		}
	}
	return b.String()
}

func median(values []float64) float64 {
	slices.Sort(values)
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func buildMeasures(summaries []benchmarkSummary) map[string]float64 {
	measures := make(map[string]float64)
	for _, summary := range summaries {
		packageName := strings.TrimPrefix(summary.Package, "github.com/datadog-labs/bits-cli/")
		benchmarkName := strings.TrimPrefix(summary.Name, "Benchmark")
		base := measurePrefix + "." + normalizeMeasurePath(packageName) + "." + normalizeMeasurePath(benchmarkName)
		measures[base+".samples"] = float64(summary.Samples)
		for name, value := range summary.Metrics {
			measures[base+"."+normalizeMeasureSegment(name)] = value
		}
	}
	return measures
}

func buildTags(metadata runMetadata) map[string]string {
	tags := make(map[string]string)
	add := func(name, value string) {
		if value != "" {
			tags[measurePrefix+"."+name] = value
		}
	}
	add("ddci_request_kind", metadata.RequestKind)
	add("go_version", metadata.GoVersion)
	add("goos", metadata.GOOS)
	add("goarch", metadata.GOARCH)
	add("runner.id", metadata.Runner.ID)
	add("runner.description", metadata.Runner.Description)
	add("runner.tags", strings.Join(metadata.Runner.Tags, ","))
	add("runner.version", metadata.Runner.Version)
	add("runner.executable_arch", metadata.Runner.ExecutableArch)
	add("runner.cpu_model", metadata.Runner.CPUModel)
	add("runner.cpu_request", metadata.Runner.CPURequest)
	add("runner.cpu_limit", metadata.Runner.CPULimit)
	add("runner.memory_request", metadata.Runner.MemoryRequest)
	add("runner.memory_limit", metadata.Runner.MemoryLimit)
	add("runner.gomaxprocs", metadata.Runner.GOMAXPROCS)
	return tags
}

func normalizeMeasurePath(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '/' || r == '.'
	})
	for i := range parts {
		parts[i] = normalizeMeasureSegment(parts[i])
	}
	return strings.Join(parts, ".")
}

func normalizeMeasureSegment(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	separator := false
	previousLowerOrDigit := false
	for _, r := range value {
		switch {
		case unicode.IsUpper(r):
			if b.Len() > 0 && (separator || previousLowerOrDigit) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			separator = false
			previousLowerOrDigit = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if separator && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			separator = false
			previousLowerOrDigit = true
		default:
			separator = true
			previousLowerOrDigit = false
		}
	}
	return b.String()
}
