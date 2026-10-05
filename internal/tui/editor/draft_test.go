package editor

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDraftRecoveryPreservesAttachmentIdentityAndEditTracking(t *testing.T) {
	e := New()
	e.Focus()
	e.Update(tea.PasteMsg{Content: "/skill:review @api"})
	attachment := Attachment{Type: "service", ID: "api-id", Label: "API", CandidateID: "candidate", SearchFlowID: "flow"}
	e.SetEntityResults("api", RemoteReady, []Candidate{{Kind: CandidateEntity, ID: "api-id", Label: "API", Insert: "@service:API", Attachment: &attachment}})
	e.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	e.Update(tea.PasteMsg{Content: "  Keep CASE\nspacing  "})
	original := e.Value()
	before := e.Attachments()
	if len(before) != 1 {
		t.Fatal("attachment not selected")
	}
	draft := e.TakeDraft()
	if e.Value() != "" || len(e.Attachments()) != 0 {
		t.Fatal("submission did not clear composer")
	}
	// Async menu and repaint updates do not count as draft edits.
	e.Update(nil)
	e.SetCommands([]CommandSpec{{Name: "skill:review"}})
	if !e.RestoreDraft(draft) || e.Value() != original || !reflect.DeepEqual(e.Attachments(), before) {
		t.Fatal("draft content or attachments were lost")
	}
	if !e.RemoveLastAttachment() || len(e.Attachments()) != 0 || e.Value() == original {
		t.Fatal("restored attachment spans were not editable")
	}
	if e.RestoreDraft(draft) {
		t.Fatal("same draft restored over a later edit")
	}
}
