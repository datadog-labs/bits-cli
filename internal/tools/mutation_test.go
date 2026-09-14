package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

func TestMutationLockerCancelWhileWaiting(t *testing.T) {
	l := newMutationLocker()
	unlock, err := l.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		u, err := l.lock(ctx)
		if err == nil {
			u()
		}
		done <- err
	}()

	// The waiter must block while the lock is held, not spin through.
	select {
	case err := <-done:
		t.Fatalf("second lock returned %v while the lock was held; expected it to block", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Cancelling must release the waiter without waiting for the holder.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not return; lock wait is not cancellation-aware")
	}
	unlock()
}

func TestWorkspaceWriteApproval(t *testing.T) {
	policy := workspaceWriteApproval("/root")

	t.Run("gates the whole workspace with one key", func(t *testing.T) {
		req, needs := policy(agent.ToolCall{Name: spec.WriteFile, Input: `{"path":"a/b.txt"}`})
		if !needs {
			t.Fatal("mutation must require approval")
		}
		if req.Key.Tool != approvalKeyWorkspaceWrite {
			t.Errorf("Key.Tool = %q, want %q", req.Key.Tool, approvalKeyWorkspaceWrite)
		}
		if req.Key.Resource != "/root" {
			t.Errorf("Key.Resource = %q, want workspace root", req.Key.Resource)
		}
	})

	t.Run("write_file and edit_file share one grant across paths", func(t *testing.T) {
		w, _ := policy(agent.ToolCall{Name: spec.WriteFile, Input: `{"path":"a.txt"}`})
		e, _ := policy(agent.ToolCall{Name: spec.EditFile, Input: `{"path":"b.txt"}`})
		if w.Key != e.Key {
			t.Errorf("mutating tools do not share a grant: %+v vs %+v", w.Key, e.Key)
		}
	})
}

func TestEditFileSerializesConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("aaa\nbbb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// One shared locker so both edits serialize their read-modify-write.
	tool := newEditFileTool(r, dir, newMutationLocker())

	inputs := []string{
		`{"path":"f.txt","edits":[{"old_text":"aaa","new_text":"AAA"}]}`,
		`{"path":"f.txt","edits":[{"old_text":"bbb","new_text":"BBB"}]}`,
	}
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tool.Handler(context.Background(), agent.ToolCall{Name: spec.EditFile, Input: in}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// If the two edits raced on a shared snapshot, one change would be lost.
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "AAA\nBBB\n" {
		t.Errorf("content = %q, want %q (a lost update indicates broken serialization)", got, "AAA\nBBB\n")
	}
}

func TestSafeReplaceConcurrentDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	names := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := safeReplace(context.Background(), r, name, []byte(name)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	entries, _ := os.ReadDir(dir)
	got := 0
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("leftover temp file: %s", e.Name())
			continue
		}
		got++
	}
	if got != len(names) {
		t.Errorf("wrote %d files, want %d", got, len(names))
	}
}
