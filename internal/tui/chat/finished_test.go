package chat

import (
	"testing"
	"time"
)

func TestFormatFinishedAt(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 26, 15, 12, 0, 0, time.Local), "Oct 26, 3:12 PM"},
		{time.Date(2026, 3, 2, 9, 5, 0, 0, time.Local), "Mar 2, 9:05 AM"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local), "Jan 1, 12:00 AM"},
		{time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local), "Jul 4, 12:00 PM"},
	}
	for _, c := range cases {
		if got := FormatFinishedAt(c.at); got != c.want {
			t.Errorf("FormatFinishedAt(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

// TestFormatFinishedAtConvertsToLocalTime pins the .Local() conversion: Task
// 5 will feed this a run-finished instant that is plausibly UTC, and a
// missing .Local() call would silently render the wrong clock hour with
// nothing above to catch it (the table above builds every case directly in
// time.Local, so t.Local() is a no-op there and dropping it keeps all four
// green).
//
// The fixed zone's offset is deliberately built as the local offset + 1
// hour, so its wall clock always differs from local's by exactly an hour,
// regardless of what the local zone actually is. "want" is derived from
// at.Local(), and the assertion below confirms formatting "at" in its own
// zone (i.e. without the .Local() call FormatFinishedAt makes) produces a
// different string — proving this test would fail if that call were removed.
func TestFormatFinishedAtConvertsToLocalTime(t *testing.T) {
	_, localOffset := time.Now().Zone()
	shifted := time.FixedZone("shifted", localOffset+3600)
	at := time.Date(2026, 6, 15, 10, 30, 0, 0, shifted)

	const layout = "Jan 2, 3:04 PM"
	want := at.Local().Format(layout)
	if withoutLocal := at.Format(layout); withoutLocal == want {
		t.Fatalf("setup: formatting %q in its own zone matches the local-converted want %q; this case cannot detect a missing .Local() call", withoutLocal, want)
	}

	if got := FormatFinishedAt(at); got != want {
		t.Errorf("FormatFinishedAt(%v) = %q, want %q", at, got, want)
	}
}
