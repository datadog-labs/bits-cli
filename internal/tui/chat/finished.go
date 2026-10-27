package chat

import "time"

// FormatFinishedAt renders the instant a response finished for the "Done"
// notice. Local time, no zero-padded day or hour, no timezone suffix — this
// is a glance-at timestamp, not a log line.
func FormatFinishedAt(t time.Time) string {
	return t.Local().Format("Jan 2, 3:04 PM")
}
