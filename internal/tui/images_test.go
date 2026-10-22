package tui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

func writePNG(t *testing.T, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A dropped image is read only when the turn starts: a failed turn keeps the
// file for a retry, and a sent prompt is recalled with the image as sent.
func TestADroppedImageIsRetriedFromItsFileAndRecalledAsSent(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 2)}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Focus()
	submit := func() {
		_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		for event := range agent.Follow(m.events, m.op.run) {
			m.applyEvent(event)
		}
		_ = m.endOperation()
	}
	path := writePNG(t, "My Shot.png")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = m.dispatch(tea.PasteMsg{Content: "'" + path + "'"})
	if got := m.editor.Attachments(); len(got) != 1 || got[0].ID != imageinput.FileURL(path) || got[0].Label != "My Shot.png" || m.editor.Value() != "" {
		t.Fatalf("drop = %q with attachments %+v", m.editor.Value(), got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	submit()
	if len(backend.requests) != 0 {
		t.Fatal("a missing image did not fail the turn before sending")
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyUp})
	submit()
	first := <-backend.requests
	if first.Context == nil || len(first.Context.Entities) != 1 || !strings.HasPrefix(first.Context.Entities[0].ID, "data:image/png;base64,") {
		t.Fatalf("retry context = %+v", first.Context)
	}

	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyUp})
	submit()
	if second := <-backend.requests; !reflect.DeepEqual(second.Context, first.Context) {
		t.Fatalf("recalled image sent %+v, want %+v", second.Context, first.Context)
	}
}

func TestADropOverTheImageLimitAttachesNothing(t *testing.T) {
	m := New(agent.New(&acceptingBackend{}, agent.Config{}))
	m.editor.Focus()
	var paths []string
	for range imageinput.MaxImages + 1 {
		paths = append(paths, writePNG(t, "shot.png"))
	}

	_, _ = m.dispatch(tea.PasteMsg{Content: strings.Join(paths, " ")})

	if got := m.editor.Attachments(); len(got) != 0 || m.editor.Value() != "" {
		t.Fatalf("value = %q, attachments = %+v", m.editor.Value(), got)
	}
}

func TestTextMentioningAnImagePathIsPastedAsText(t *testing.T) {
	m := New(agent.New(&acceptingBackend{}, agent.Config{}))
	m.editor.Focus()
	text := "look at " + writePNG(t, "shot.png")

	_, _ = m.dispatch(tea.PasteMsg{Content: text})

	if m.editor.Value() != text || len(m.editor.Attachments()) != 0 {
		t.Fatalf("value = %q, attachments = %+v", m.editor.Value(), m.editor.Attachments())
	}
}
