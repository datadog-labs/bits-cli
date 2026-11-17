package resume

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// openIn uses a fresh config directory per test and returns the history of
// each directory, in order.
func openIn(t *testing.T, dirs ...string) []History {
	t.Helper()
	t.Setenv("BITS_CONFIG_DIR", t.TempDir())
	histories := make([]History, len(dirs))
	for i, dir := range dirs {
		h, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		histories[i] = h
	}
	return histories
}

func open(t *testing.T, h History, id string, minutes int) {
	t.Helper()
	if err := h.Record(context.Background(), id, epoch.Add(time.Duration(minutes)*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func recent(t *testing.T, h History) []string {
	t.Helper()
	ids, err := h.Recent()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestRecentOrdersByLastOpening(t *testing.T) {
	type opening struct {
		dir     int // index into the two workspaces
		id      string
		minutes int
	}
	cases := []struct {
		name     string
		openings []opening
		want     [2][]string
	}{
		{"none", nil, [2][]string{nil, nil}},
		{
			"most recent first, per workspace",
			[]opening{{0, "a", 1}, {1, "b", 2}, {0, "c", 3}},
			[2][]string{{"c", "a"}, {"b"}},
		},
		{
			"reopening moves a conversation up",
			[]opening{{0, "a", 1}, {0, "b", 2}, {0, "a", 3}},
			[2][]string{{"a", "b"}, nil},
		},
		{
			"opening elsewhere moves it there",
			[]opening{{0, "a", 1}, {1, "a", 2}},
			[2][]string{nil, {"a"}},
		},
		{
			"an older record does not replace a newer one",
			[]opening{{1, "a", 5}, {0, "a", 2}},
			[2][]string{nil, {"a"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := openIn(t, t.TempDir(), t.TempDir())
			for _, o := range tc.openings {
				open(t, h[o.dir], o.id, o.minutes)
			}
			for i := range h {
				if got := recent(t, h[i]); !slices.Equal(got, tc.want[i]) {
					t.Errorf("workspace %d recent = %q, want %q", i, got, tc.want[i])
				}
			}
		})
	}
}

// Sessions in several terminals record at once; the lock keeps every record.
func TestConcurrentRecordsAreAllKept(t *testing.T) {
	dir := t.TempDir()
	h := openIn(t, dir)[0]
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			// A separate History per writer, as separate processes would have.
			h, err := Open(dir)
			if err == nil {
				err = h.Record(context.Background(), fmt.Sprintf("c%02d", i), epoch.Add(time.Duration(i)*time.Second))
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := recent(t, h); len(got) != writers || got[0] != fmt.Sprintf("c%02d", writers-1) {
		t.Fatalf("recent = %q", got)
	}
}

// A damaged file is reported to the reader and replaced by the next record.
func TestCorruptHistory(t *testing.T) {
	h := openIn(t, t.TempDir())[0]
	if err := os.WriteFile(h.path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Recent(); err == nil || !strings.Contains(err.Error(), h.path()) {
		t.Fatalf("Recent error = %v, want one naming %s", err, h.path())
	}
	open(t, h, "a", 1)
	if got := recent(t, h); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("recent after replacing = %q", got)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	h := openIn(t, t.TempDir())[0]
	for i := range maxConversations + 1 {
		open(t, h, fmt.Sprintf("c%04d", i), i)
	}
	got := recent(t, h)
	if len(got) != maxConversations || slices.Contains(got, "c0000") || got[0] != fmt.Sprintf("c%04d", maxConversations) {
		t.Fatalf("kept %d, first %q, oldest kept: %t", len(got), got[0], slices.Contains(got, "c0000"))
	}
}
