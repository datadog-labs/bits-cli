package editor

import "testing"

func TestQueueSnapshotsSurviveDraftReset(t *testing.T) {
	e := New()
	score := 0.5
	a := Attachment{ID: "one", Type: "service", RankScore: &score, TrackingAttributes: map[string]string{"source": "original"}}
	e.SetValue("first")
	e.AddAttachment(a)
	e.Enqueue(e.Snapshot())
	e.Reset()
	e.SetValue("second")
	e.Enqueue(e.Snapshot())
	score = 1
	a.TrackingAttributes["source"] = "changed"
	first := e.QueuedPrompts()[0]
	if first.Text != "first" || *first.Attachments[0].RankScore != 0.5 || first.Attachments[0].TrackingAttributes["source"] != "original" {
		t.Fatalf("queue did not preserve snapshot: %+v", first)
	}
	e.RemoveQueued(0)
	second := e.QueuedPrompts()[0]
	if second.Text != "second" || e.Value() != "second" {
		t.Fatalf("queue advanced into draft: %+v", second)
	}
	e.PauseQueue()
	e.Reset()
	if !e.QueuePaused() || e.QueueLen() != 1 {
		t.Fatal("draft reset changed queue")
	}
	e.ClearQueue()
	if e.QueueLen() != 0 || e.QueuePaused() {
		t.Fatal("queue did not clear")
	}
}
