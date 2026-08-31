package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/DataDog/bits-cli/internal/agent"
)

type openCountingFS struct {
	fs.FS
	opens int
}

func (fsys *openCountingFS) Open(name string) (fs.File, error) {
	fsys.opens++
	return fsys.FS.Open(name)
}

func (fsys *openCountingFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(fsys.FS, name)
}

func (fsys *openCountingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(fsys.FS, name)
}

func TestGrepFilesTool(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid regex returns error", func(t *testing.T) {
		tool := newGrepFilesTool(fstest.MapFS{}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"[invalid"}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "invalid pattern")
	})

	t.Run("canceled context stops before walking", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tool := newGrepFilesTool(fstest.MapFS{"f.go": {Data: []byte("needle\n")}}, "/workspace")
		_, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		tool := newGrepFilesTool(fstest.MapFS{}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"x","path":"/tmp"}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "absolute")
	})

	t.Run("parent traversal rejected by os.Root", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		tool := newGrepFilesTool(root.FS(), dir)
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"x","path":"../../etc"}`})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatal("expected error for path traversal")
		}
	})

	t.Run("no matches", func(t *testing.T) {
		fsys := fstest.MapFS{"f.go": {Data: []byte("package main\n")}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"xyz123"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "No matches") {
			t.Errorf("expected no-matches message, got: %s", result.Output)
		}
	})

	t.Run("basic match returns file:line: text", func(t *testing.T) {
		fsys := fstest.MapFS{"main.go": {Data: []byte("package main\n\nfunc Foo() {}\n")}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"func Foo"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "main.go:3: func Foo() {}") {
			t.Errorf("unexpected output: %s", result.Output)
		}
	})

	t.Run("case-insensitive by default", func(t *testing.T) {
		fsys := fstest.MapFS{"f.go": {Data: []byte("Hello World\n")}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"hello world"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "Hello World") {
			t.Errorf("case-insensitive match failed: %s", result.Output)
		}
	})

	t.Run("case-sensitive misses wrong case", func(t *testing.T) {
		fsys := fstest.MapFS{"f.go": {Data: []byte("Hello World\n")}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"hello world","case_sensitive":true}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "No matches") {
			t.Errorf("expected no matches for wrong case, got: %s", result.Output)
		}
	})

	t.Run("include glob filters files", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.go": {Data: []byte("needle\n")},
			"main.ts": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle","include":"**/*.go"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "main.go") {
			t.Errorf("expected main.go in results: %s", result.Output)
		}
		if strings.Contains(result.Output, "main.ts") {
			t.Errorf("main.ts must be excluded by include glob: %s", result.Output)
		}
	})

	t.Run("include glob filters a directly specified file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.ts": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle","path":"main.ts","include":"**/*.go"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if result.Output != "No matches found." {
			t.Errorf("output = %q, want no matches", result.Output)
		}
	})

	t.Run("include glob semantics", func(t *testing.T) {
		cases := []struct {
			include string
			path    string
			want    bool
		}{
			{"**/*.go", "main.go", true},
			{"**/*.go", "src/main.go", true},
			{"**/*.go", "a/b/c.go", true},
			{"**/*.go", "main.ts", false},
			{"*.go", "main.go", true},
			{"*.go", "src/main.go", true},
			{"src/*.go", "src/main.go", true},
			{"src/*.go", "lib/main.go", false},
		}
		for _, tc := range cases {
			t.Run(tc.include+"/"+tc.path, func(t *testing.T) {
				fsys := fstest.MapFS{tc.path: {Data: []byte("needle\n")}}
				tool := newGrepFilesTool(fsys, "/workspace")
				input := fmt.Sprintf(`{"pattern":"needle","include":%q}`, tc.include)
				result, err := tool.Handler(ctx, agent.ToolCall{Input: input})
				if err != nil || result.IsError {
					t.Fatalf("unexpected error: %v %s", err, result.Output)
				}
				if got := strings.Contains(result.Output, tc.path); got != tc.want {
					t.Errorf("include %q, path %q: matched=%v, want %v (output: %s)", tc.include, tc.path, got, tc.want, result.Output)
				}
			})
		}
	})

	t.Run("binary files are skipped", func(t *testing.T) {
		fsys := fstest.MapFS{
			"lib.so":  {Data: append([]byte("needle"), 0, 0, 0)},
			"main.go": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "lib.so") {
			t.Errorf("binary file must be skipped: %s", result.Output)
		}
		if !strings.Contains(result.Output, "main.go") {
			t.Errorf("text file must be matched: %s", result.Output)
		}
	})

	t.Run("non-regular files are skipped", func(t *testing.T) {
		fsys := fstest.MapFS{
			"pipe":    {Data: []byte("needle\n"), Mode: fs.ModeNamedPipe},
			"main.go": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "pipe") || !strings.Contains(result.Output, "main.go") {
			t.Errorf("non-regular files must be skipped: %s", result.Output)
		}
	})

	t.Run("text files are opened once after binary probing", func(t *testing.T) {
		fsys := &openCountingFS{FS: fstest.MapFS{"main.go": {Data: []byte("needle\n")}}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		// The other open is the attempted root .gitignore load.
		if fsys.opens != 2 {
			t.Errorf("opens = %d, want 2", fsys.opens)
		}
	})

	t.Run("gitignore excludes files", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":    {Data: []byte("vendor/\n")},
			"main.go":       {Data: []byte("needle\n")},
			"vendor/dep.go": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "vendor") {
			t.Errorf("vendor/ must be gitignored: %s", result.Output)
		}
		if !strings.Contains(result.Output, "main.go") {
			t.Errorf("main.go must be matched: %s", result.Output)
		}
	})

	t.Run("root gitignore applies to a scoped directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":      {Data: []byte("src/secret.txt\n")},
			"src/secret.txt":  {Data: []byte("needle\n")},
			"src/visible.txt": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle","path":"src"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "secret.txt") || !strings.Contains(result.Output, "visible.txt") {
			t.Errorf("root ignore rules must apply to scoped searches: %s", result.Output)
		}
	})

	t.Run("root gitignore applies to a directly searched file", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":     {Data: []byte("src/secret.txt\n")},
			"src/secret.txt": {Data: []byte("needle\n")},
		}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle","path":"src/secret.txt"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "No matches") {
			t.Errorf("directly searched ignored file must be skipped: %s", result.Output)
		}
	})

	t.Run("offset paginates results", func(t *testing.T) {
		lines := make([]string, 10)
		for i := range lines {
			lines[i] = fmt.Sprintf("match%d", i+1)
		}
		fsys := fstest.MapFS{"f.txt": {Data: []byte(strings.Join(lines, "\n"))}}
		tool := newGrepFilesTool(fsys, "/workspace")

		// First page: matches 1–5
		r1, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"match","offset":0}`})
		if err != nil || r1.IsError {
			t.Fatalf("unexpected error: %v %s", err, r1.Output)
		}
		// Second page: offset=5 skips the first 5
		r2, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"match","offset":5}`})
		if err != nil || r2.IsError {
			t.Fatalf("unexpected error: %v %s", err, r2.Output)
		}
		if strings.Contains(r2.Output, ":1: match1") {
			t.Errorf("offset=5 must skip match1: %s", r2.Output)
		}
		if !strings.Contains(r2.Output, "match6") {
			t.Errorf("offset=5 must include match6: %s", r2.Output)
		}
	})

	t.Run("negative offset is rejected", func(t *testing.T) {
		tool := newGrepFilesTool(fstest.MapFS{}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"x","offset":-1}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "offset")
	})

	t.Run("maximum offset cannot overflow the match cap", func(t *testing.T) {
		maxInt := int(^uint(0) >> 1)
		fsys := fstest.MapFS{"f.txt": {Data: []byte("needle\n")}}
		tool := newGrepFilesTool(fsys, "/workspace")
		input := fmt.Sprintf(`{"pattern":"needle","offset":%d}`, maxInt)
		result, err := tool.Handler(ctx, agent.ToolCall{Input: input})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "beyond the last match") {
			t.Errorf("unexpected output: %s", result.Output)
		}
	})

	t.Run("lines above the Scanner default do not abort the search", func(t *testing.T) {
		content := strings.Repeat("x", 70*1024) + "\nneedle\n"
		fsys := fstest.MapFS{"f.txt": {Data: []byte(content)}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "f.txt:2: needle") {
			t.Errorf("search did not continue after the long line: %s", result.Output)
		}
	})

	t.Run("oversized matching text is truncated", func(t *testing.T) {
		content := strings.Repeat("needle", maxGrepBytes) + "\nneedle-again\n"
		fsys := fstest.MapFS{"f.txt": {Data: []byte(content)}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "match truncated") || !strings.Contains(result.Output, "needle-again") {
			t.Errorf("oversized match was not safely truncated: %s", result.Output)
		}
	})

	t.Run("truncation preserves UTF-8", func(t *testing.T) {
		line := strings.Repeat("a", maxGrepMatchBytes-1) + "é"
		if got := truncateGrepMatch([]byte(line)); !utf8.ValidString(got) {
			t.Errorf("truncateGrepMatch returned invalid UTF-8: %q", got)
		}

		output := strings.Repeat("a", maxGrepBytes-len("… [match truncated]\n")-1) + "é\n"
		if got := truncateOutputLine(output, maxGrepBytes); !utf8.ValidString(got) {
			t.Errorf("truncateOutputLine returned invalid UTF-8: %q", got)
		}
	})

	t.Run("match cap produces continuation hint", func(t *testing.T) {
		rows := make([]string, maxGrepMatches+10)
		for i := range rows {
			rows[i] = "needle"
		}
		fsys := fstest.MapFS{"f.txt": {Data: []byte(strings.Join(rows, "\n"))}}
		tool := newGrepFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"pattern":"needle"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if !strings.Contains(result.Output, "more matches") {
			t.Errorf("expected continuation hint: %s", result.Output)
		}
		if !strings.Contains(result.Output, fmt.Sprintf("offset=%d", maxGrepMatches)) {
			t.Errorf("expected offset=%d in hint: %s", maxGrepMatches, result.Output)
		}
	})
}
