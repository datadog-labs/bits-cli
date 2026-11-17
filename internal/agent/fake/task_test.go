package fake

import (
	"slices"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// TestTaskIDSurvivesReplay checks that a round after call() sees the id task()
// returned when it ran live, rather than starting the task again.
func TestTaskIDSurvivesReplay(t *testing.T) {
	f := &Fake{}
	opts := assistant.SendOptions{ConversationID: "conversation"}
	if _, err := send(t, f, `id = task("t", []); call("x", {}); say(id)`, opts); err != nil {
		t.Fatal(err)
	}
	response := assistant.ClientToolResponse{Status: assistant.ToolStatusSuccess}
	got, err := send(t, f, []assistant.ClientToolResponse{response}, opts)
	if err != nil {
		t.Fatal(err)
	}
	tasks := f.convs["conversation"].tasks
	if len(tasks) != 1 {
		t.Fatalf("started %d tasks, want 1", len(tasks))
	}
	if want := []string{"markdown_fragment:" + tasks[0].id, "usage"}; !slices.Equal(got, want) {
		t.Fatalf("second round %q, want %q", got, want)
	}
}
