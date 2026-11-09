package viz

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func imageBytes() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	return b.Bytes()
}

func TestSnapshotOwnsItsBytes(t *testing.T) {
	data := imageBytes()
	snapshot, err := NewSnapshot(data, Availability{Available: false, Reason: "Snapshots only"})
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 0
	encoded := snapshot.PNG()
	if &encoded[0] != &snapshot.PNG()[0] {
		t.Fatal("PNG copied the encoded image")
	}
	if snapshot.PNG()[0] != 137 {
		t.Fatal("snapshot bytes were mutated")
	}
	if snapshot.Interactive().Available || snapshot.Width() != 2 || snapshot.Height() != 3 {
		t.Fatal("snapshot metadata changed")
	}
	if _, err := NewSnapshot([]byte("invalid"), Availability{}); err == nil {
		t.Fatal("accepted invalid PNG")
	}
}

func request(t *testing.T) Request {
	t.Helper()
	return Request{
		Definition: assistant.WidgetDefinition{Type: "timeseries"},
	}
}
