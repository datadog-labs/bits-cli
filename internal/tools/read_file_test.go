package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
)

type sizeMaskingFS struct{ fs.FS }

func (fsys sizeMaskingFS) Open(name string) (fs.File, error) {
	f, err := fsys.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return sizeMaskingFile{File: f}, nil
}

type sizeMaskingFile struct{ fs.File }

func (f sizeMaskingFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return sizeMaskingInfo{FileInfo: info}, nil
}

type sizeMaskingInfo struct{ fs.FileInfo }

func (sizeMaskingInfo) Size() int64 { return 0 }

type blockingFile struct {
	fs.File
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	closeErr  error
}

func (f *blockingFile) Read([]byte) (int, error) {
	f.startOnce.Do(func() { close(f.started) })
	<-f.closed
	return 0, fs.ErrClosed
}

func (f *blockingFile) Close() error {
	f.closeOnce.Do(func() {
		close(f.closed)
		f.closeErr = f.File.Close()
	})
	return f.closeErr
}

type singleFileFS struct{ file fs.File }

func (fsys singleFileFS) Open(string) (fs.File, error) { return fsys.file, nil }

func assertError(t *testing.T, result agent.ToolResult, wantSnip string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected IsError, output: %s", result.Output)
	}
	if wantSnip != "" && !strings.Contains(result.Output, wantSnip) {
		t.Errorf("expected %q in output, got: %s", wantSnip, result.Output)
	}
}

func invokeReadFile(t *testing.T, fsys fs.FS, input any) agent.ToolResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	tool := newReadFileTool(fsys, "/workspace")
	result, err := tool.Handler(context.Background(), agent.ToolCall{
		ID:    "t",
		Name:  "read_file",
		Input: string(data),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReadFileTool(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tool := newReadFileTool(fstest.MapFS{"hello.txt": {Data: []byte("content")}}, "/workspace")
		_, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"hello.txt"}`})
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("cancellation interrupts an active read", func(t *testing.T) {
		base, err := (fstest.MapFS{"hello.txt": {Data: []byte("content")}}).Open("hello.txt")
		if err != nil {
			t.Fatal(err)
		}
		file := &blockingFile{File: base, started: make(chan struct{}), closed: make(chan struct{})}
		tool := newReadFileTool(singleFileFS{file: file}, "/workspace")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"hello.txt"}`})
			done <- err
		}()

		<-file.started
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("read did not stop after cancellation")
		}
	})

	t.Run("absolute path", func(t *testing.T) {
		r := invokeReadFile(t, fstest.MapFS{}, map[string]any{"path": "/etc/passwd"})
		if !r.IsError {
			t.Fatal("expected IsError for absolute path")
		}
	})

	t.Run("dotdot traversal", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		r := invokeReadFile(t, root.FS(), map[string]any{"path": "../../etc/passwd"})
		if !r.IsError {
			t.Fatal("expected IsError for dotdot traversal")
		}
	})

	t.Run("directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			"subdir/file.txt": &fstest.MapFile{Data: []byte("content")},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "subdir"})
		if !r.IsError {
			t.Fatal("expected IsError for directory path")
		}
	})

	t.Run("non-regular file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"pipe": {Data: []byte("content"), Mode: fs.ModeNamedPipe},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "pipe"})
		assertError(t, r, "not a regular file")
	})

	t.Run("binary file", func(t *testing.T) {
		fsys := fstest.MapFS{
			"data.bin": &fstest.MapFile{Data: []byte("hello\x00world")},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "data.bin"})
		if !r.IsError {
			t.Fatal("expected IsError for binary file")
		}
		if !strings.Contains(r.Output, "binary") {
			t.Errorf("expected 'binary' in output, got: %s", r.Output)
		}
	})

	t.Run("size limit survives a stale stat result", func(t *testing.T) {
		data := make([]byte, maxFileSize+1)
		fsys := sizeMaskingFS{FS: fstest.MapFS{"growing.txt": {Data: data}}}
		r := invokeReadFile(t, fsys, map[string]any{"path": "growing.txt"})
		assertError(t, r, "exceeds 10 MB")
	})

	t.Run("file not found", func(t *testing.T) {
		r := invokeReadFile(t, fstest.MapFS{}, map[string]any{"path": "missing.txt"})
		if !r.IsError {
			t.Fatal("expected IsError for missing file")
		}
	})

	t.Run("valid file no offset or limit", func(t *testing.T) {
		content := "line1\nline2\nline3"
		fsys := fstest.MapFS{
			"hello.txt": &fstest.MapFile{Data: []byte(content)},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "hello.txt"})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if r.Title != "hello.txt" {
			t.Errorf("title = %q, want %q", r.Title, "hello.txt")
		}
		if r.Output != content {
			t.Errorf("output = %q, want %q", r.Output, content)
		}
	})

	t.Run("relative path syntax is normalized", func(t *testing.T) {
		fsys := fstest.MapFS{"hello.txt": {Data: []byte("content")}}
		r := invokeReadFile(t, fsys, map[string]any{"path": "./hello.txt"})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if r.Title != "hello.txt" || r.Output != "content" {
			t.Errorf("result = (%q, %q), want (hello.txt, content)", r.Title, r.Output)
		}
	})

	t.Run("offset beyond file end", func(t *testing.T) {
		fsys := fstest.MapFS{
			"small.txt": &fstest.MapFile{Data: []byte("a\nb\nc")},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "small.txt", "offset": 10})
		if !r.IsError {
			t.Fatal("expected IsError for offset beyond end of file")
		}
	})

	t.Run("trailing newline does not create an extra line", func(t *testing.T) {
		fsys := fstest.MapFS{
			"small.txt": &fstest.MapFile{Data: []byte("a\nb\n")},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "small.txt", "offset": 3})
		assertError(t, r, "beyond end of file (2 lines)")
	})

	t.Run("offset plus line cap truncation", func(t *testing.T) {
		total := maxReadLines + 5
		lines := make([]string, total)
		for i := range lines {
			lines[i] = fmt.Sprintf("line%d", i+1)
		}
		fsys := fstest.MapFS{
			"big.txt": &fstest.MapFile{Data: []byte(strings.Join(lines, "\n"))},
		}
		const off = 3
		r := invokeReadFile(t, fsys, map[string]any{"path": "big.txt", "offset": off})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		wantRange := fmt.Sprintf("Showing lines %d\u2013%d of %d", off, off+maxReadLines-1, total)
		if !strings.Contains(r.Output, wantRange) {
			t.Errorf("output missing %q:\n%s", wantRange, r.Output)
		}
		wantOffset := fmt.Sprintf("offset=%d", off+maxReadLines)
		if !strings.Contains(r.Output, wantOffset) {
			t.Errorf("output missing %q:\n%s", wantOffset, r.Output)
		}
	})

	t.Run("user limit shows more lines notice", func(t *testing.T) {
		lines := make([]string, 10)
		for i := range lines {
			lines[i] = fmt.Sprintf("line%d", i+1)
		}
		fsys := fstest.MapFS{
			"ten.txt": &fstest.MapFile{Data: []byte(strings.Join(lines, "\n"))},
		}
		r := invokeReadFile(t, fsys, map[string]any{"path": "ten.txt", "limit": 3})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if !strings.Contains(r.Output, "7 more lines") {
			t.Errorf("expected '7 more lines' in output:\n%s", r.Output)
		}
		if !strings.Contains(r.Output, "offset=4") {
			t.Errorf("expected 'offset=4' in output:\n%s", r.Output)
		}
	})
}

func TestReadWindow(t *testing.T) {
	join := func(n int, s string) string { return strings.Join(repeatLines(n, s), "\n") }

	tests := []struct {
		name       string
		content    string
		offset     int
		limit      *int
		totalLines int
		wantExact  string   // when set, output must equal this exactly (zero-copy path)
		wantSnips  []string // substrings that must be present
		noSnips    []string // substrings that must be absent
	}{
		{
			name:       "no truncation returns content unchanged",
			content:    join(10, "hello"),
			offset:     1,
			totalLines: 10,
			wantExact:  join(10, "hello"),
			noSnips:    []string{"Use offset=", "more lines"},
		},
		{
			name:       "byte cap",
			content:    join(5, strings.Repeat("a", maxReadBytes/3)),
			offset:     1,
			totalLines: 5,
			wantSnips:  []string{"Showing lines 1\u20132 of 5", "offset=3"},
		},
		{
			// The last line has no trailing newline, so a final line of exactly
			// maxReadBytes must fit rather than be reported as over the limit.
			name:       "final line exactly at byte cap fits",
			content:    strings.Repeat("a", maxReadBytes),
			offset:     1,
			totalLines: 1,
			wantExact:  strings.Repeat("a", maxReadBytes),
		},
		{
			// A non-final line of maxReadBytes is charged for its newline and so
			// exceeds the cap.
			name:       "non-final line at byte cap exceeds",
			content:    strings.Repeat("a", maxReadBytes) + "\nb",
			offset:     1,
			totalLines: 2,
			wantSnips:  []string{"exceeds", "offset=2"},
		},
		{
			name:       "line at offset exceeds byte limit",
			content:    "b\nb\n" + strings.Repeat("a", maxReadBytes+1) + "\n" + join(7, "b"),
			offset:     3,
			totalLines: 10,
			wantSnips:  []string{"exceeds", "offset=4"},
		},
		{
			name:       "continuation hint line numbers with offset",
			content:    join(maxReadLines+501, "x"),
			offset:     501,
			totalLines: maxReadLines + 501,
			wantSnips: []string{
				fmt.Sprintf("Showing lines 501\u2013%d of %d", 500+maxReadLines, maxReadLines+501),
				fmt.Sprintf("offset=%d", 501+maxReadLines),
			},
		},
		{
			name:       "limit reaching end adds no hint",
			content:    join(3, "y"),
			offset:     1,
			limit:      new(5),
			totalLines: 3,
			wantExact:  join(3, "y"),
			noSnips:    []string{"more lines", "Use offset="},
		},
		{
			name:       "offset and limit reaching end yield a plain slice of content",
			content:    "a\nb\nc\nd",
			offset:     3,
			limit:      new(2),
			totalLines: 4,
			wantExact:  "c\nd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readWindow(tt.content, tt.offset, tt.limit, tt.totalLines)
			if tt.wantExact != "" && got != tt.wantExact {
				t.Fatalf("output = %q, want %q", got, tt.wantExact)
			}
			for _, snip := range tt.wantSnips {
				if !strings.Contains(got, snip) {
					t.Errorf("output missing %q:\n%s", snip, got)
				}
			}
			for _, snip := range tt.noSnips {
				if strings.Contains(got, snip) {
					t.Errorf("output unexpectedly contains %q:\n%s", snip, got)
				}
			}
		})
	}
}

func repeatLines(n int, s string) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = s
	}
	return lines
}
