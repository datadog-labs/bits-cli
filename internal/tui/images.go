package tui

import (
	"os"
	"path/filepath"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
)

// dropImages attaches dropped image files as file URL chips, all or none.
// It reports whether text was a drop rather than a paste.
func dropImages(e *editor.Editor, text string) (bool, error) {
	paths, ok := imageinput.DroppedPaths(text, os.Stat)
	if !ok {
		return false, nil
	}
	attached := make(map[string]bool)
	for _, a := range e.Attachments() {
		if a.Type == string(assistant.EntityImageURL) {
			attached[a.ID] = true
		}
	}
	var added []editor.Attachment
	for _, path := range paths {
		id := imageinput.FileURL(path)
		if !attached[id] {
			attached[id] = true
			added = append(added, editor.Attachment{Type: string(assistant.EntityImageURL), ID: id, Label: filepath.Base(path)})
		}
	}
	if len(attached) > imageinput.MaxImages {
		return true, imageinput.ErrTooManyImages
	}
	for _, a := range added {
		e.AddAttachment(a)
	}
	return true, nil
}
