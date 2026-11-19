package tui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
	"github.com/datadog-labs/bits-cli/internal/viz"
)

type widgetTestClient struct {
	render         viz.Result
	open           viz.Status // zero opens successfully
	renders, opens int
	last           viz.Request
	closed         bool
	remote         bool // previews only, like Datadog snapshots
}

func (c *widgetTestClient) Render(_ context.Context, r viz.Request) viz.Result {
	c.renders++
	c.last = r
	return c.render
}

func (c *widgetTestClient) Open(_ context.Context, r viz.Request) viz.Result {
	c.opens++
	c.last = r
	if c.open.Phase == viz.Idle {
		return viz.Result{Status: viz.Status{Phase: viz.Succeeded}}
	}
	return viz.Result{Status: c.open}
}

func (c *widgetTestClient) Interactive() bool { return !c.remote }

func (c *widgetTestClient) Close() error { c.closed = true; return nil }

// chartApp is a get_widget result rendering definition.
func chartApp(definition string) assistant.AppPayload {
	result := `{"structuredContent":{"source_tool":"get_widget","widget_definition":` + definition + `,"from_ts":1700000000,"to_ts":1700003600}}`
	return assistant.AppPayload{ResourceURI: viz.DatavizURI, Result: json.RawMessage(result), RelayHandle: "h"}
}

var (
	timeseries = chartApp(`{"type":"timeseries","title":"CPU"}`)
	toplist    = chartApp(`{"type":"toplist","title":"CPU"}`)
	incident   = assistant.AppPayload{ResourceURI: viz.DatavizURI, Result: json.RawMessage(`{"_meta":{"datadog/show_ui":{"view_spec":{"view":{"name":"incident_detail"}}}}}`)}
)

// widgetModel shows one app block; cmd holds any operation it started, and
// opened records URLs sent to the browser.
func widgetModel(t *testing.T, client viz.Client, support graphics.Support, app assistant.AppPayload) (m *Model, id agent.BlockID, cmd tea.Cmd, opened *[]string) {
	opened = new([]string)
	m = New(agent.New(&spyBackend{t: t, webOrigin: "https://demo.datadoghq.com"}, agent.Config{}), Config{
		Visualization: client,
		OpenURL:       func(_ context.Context, u string) error { *opened = append(*opened, u); return nil },
	})
	m.mode = ModeChat
	m.graphics.Support = support
	m.state.ConversationID = "conv"
	id = agent.BlockID{Key: "widget"}
	m.state.Blocks = []agent.Block{{ID: id, Kind: assistant.KindApp, MessageID: "msg", Complete: true, App: &app}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.syncTranscript()
	_, cmd = m.Update(struct{}{})
	return m, id, cmd, opened
}

// clickWidget clicks the first cell offering action, as the user would;
// WidgetNone clicks a markdown link.
func clickWidget(t *testing.T, m *Model, action chat.WidgetAction) tea.Cmd {
	t.Helper()
	for y := range m.list.Height() {
		for x := range m.list.Width() {
			if hit, ok := m.list.LinkAt(x, y); ok && hit.Action == action {
				at := m.frame.transcript.Min.Add(image.Pt(x, y))
				m.selection.clicks.reset() // a separate click, not a double-click
				m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: at.X, Y: at.Y})
				_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: at.X, Y: at.Y})
				return cmd
			}
		}
	}
	t.Fatalf("no matching link in:\n%s", ansi.Strip(m.list.Document()))
	return nil
}

// widgetActions lists the distinct actions the transcript's cells offer.
func widgetActions(m *Model) []chat.WidgetAction {
	seen := map[chat.WidgetAction]bool{}
	var actions []chat.WidgetAction
	for y := range m.list.Height() {
		for x := range m.list.Width() {
			if hit, ok := m.list.LinkAt(x, y); ok && hit.Action != chat.WidgetNone && !seen[hit.Action] {
				seen[hit.Action] = true
				actions = append(actions, hit.Action)
			}
		}
	}
	return actions
}

// Each capability combination offers its best action on the title and image,
// with no action row and nothing that reads as an error.
func TestWidgetModes(t *testing.T) {
	for name, tc := range map[string]struct {
		client       viz.Client
		support      graphics.Support
		action       chat.WidgetAction
		box, renders bool
		animate      bool
	}{
		"link":   {action: chat.WidgetWeb},
		"pane":   {client: &widgetTestClient{}, support: graphics.Unsupported, action: chat.WidgetOpen},
		"inline": {client: &widgetTestClient{}, support: graphics.Supported, action: chat.WidgetOpen, box: true, renders: true, animate: true},
		// Datadog snapshots: a preview with a web link, never the panel.
		"remote link":          {client: &widgetTestClient{remote: true}, support: graphics.Unsupported, action: chat.WidgetWeb},
		"remote inline":        {client: &widgetTestClient{remote: true}, support: graphics.Supported, action: chat.WidgetWeb, box: true, renders: true, animate: true},
		"remote probe pending": {client: &widgetTestClient{remote: true}, support: graphics.Unknown, action: chat.WidgetWeb, box: true, renders: true, animate: true},
		// The box is reserved before the terminal answers the probe.
		"probe pending": {client: &widgetTestClient{}, support: graphics.Unknown, action: chat.WidgetOpen, box: true, renders: true, animate: true},
	} {
		t.Run(name, func(t *testing.T) {
			m, id, _, _ := widgetModel(t, tc.client, tc.support, timeseries)
			defer m.Detach()
			doc := ansi.Strip(m.list.Document())
			if actions := widgetActions(m); len(actions) != 1 || actions[0] != tc.action {
				t.Errorf("actions = %v, want [%v]", actions, tc.action)
			}
			if strings.Contains(doc, "Rendering") != tc.box {
				t.Errorf("box shown = %v, want %v:\n%s", !tc.box, tc.box, doc)
			}
			for _, s := range []string{"Open", "Datadog", "unavailable", "not installed"} {
				if strings.Contains(doc, s) {
					t.Errorf("unexpected %q:\n%s", s, doc)
				}
			}
			e := m.widgets.entries[id]
			if rendering := e.controller != nil && e.controller.State().Render.Phase == viz.Loading; rendering != tc.renders {
				t.Errorf("rendering = %v, want %v", rendering, tc.renders)
			}
			// No run is active: a widget's spinner must not depend on one.
			if m.animClock.armed != tc.animate {
				t.Errorf("animating = %v, want %v", m.animClock.armed, tc.animate)
			}
		})
	}
}

// A helper that cannot run moves the whole session to links, and a failure
// worth retrying offers Retry with its reason.
func TestWidgetCompletions(t *testing.T) {
	client := &widgetTestClient{render: viz.Result{Status: viz.Status{Phase: viz.Failed, Reason: "Query failed.", Retry: viz.RetryAllowed}}}
	m, _, render, opened := widgetModel(t, client, graphics.Supported, timeseries)
	defer m.Detach()

	// The render finishes after streaming pushed the widget off screen: its
	// spinner must still stop.
	long := strings.Repeat("line\n\n", 60)
	m.state.Blocks = append(m.state.Blocks, agent.Block{ID: agent.BlockID{Key: "text"}, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: long}})
	m.syncTranscript()
	m.Update(struct{}{})
	if _, visible := m.list.Widgets(); len(visible) != 0 || !m.animClock.armed {
		t.Fatal("test setup: widget should be off screen and still rendering")
	}
	runWidgetTasks(m, render)
	if m.animClock.armed {
		t.Fatal("an offscreen widget kept the animation clock running")
	}
	m.state.Blocks = m.state.Blocks[:1]
	m.syncTranscript()
	m.Update(struct{}{})
	if doc := ansi.Strip(m.list.Document()); !strings.Contains(doc, "Query failed.") || !strings.Contains(doc, "Open chart in Datadog") || !slices.Equal(widgetActions(m), []chat.WidgetAction{chat.WidgetRetry, chat.WidgetWeb}) {
		t.Fatalf("retryable failure not offered:\n%s", doc)
	}
	runWidgetTasks(m, clickWidget(t, m, chat.WidgetWeb))
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "https://demo.datadoghq.com/dataviz/sandbox?") {
		t.Fatalf("opened %q after a failed preview", *opened)
	}
	runWidgetTasks(m, clickWidget(t, m, chat.WidgetRetry))
	if client.renders != 2 {
		t.Fatalf("renders = %d, want 2", client.renders)
	}

	client.render = viz.Result{Status: viz.Status{Phase: viz.Unavailable}}
	runWidgetTasks(m, clickWidget(t, m, chat.WidgetRetry))
	m.Update(struct{}{})
	if m.widgetMode() != widgetLink || widgetActions(m)[0] != chat.WidgetWeb {
		t.Fatalf("unavailable helper did not downgrade to links: %v", widgetActions(m))
	}
}

// Old work must not reach a replacement conversation or a newer revision.
func TestWidgetLifecycle(t *testing.T) {
	client := &widgetTestClient{}
	m, id, _, _ := widgetModel(t, client, graphics.Unsupported, timeseries)
	original := m.widgets.entries[id]
	open := clickWidget(t, m, chat.WidgetOpen)
	if open == nil || original.controller.State().Open.Phase != viz.Loading {
		t.Fatal("open not started")
	}
	m.Update(struct{}{})
	if !strings.Contains(ansi.Strip(m.list.Document()), "Opening") {
		t.Fatalf("opening not shown:\n%s", ansi.Strip(m.list.Document()))
	}

	m.state.Blocks[0].Rev++
	m.syncTranscript()
	m.Update(struct{}{})
	if m.widgets.entries[id] != original {
		t.Fatal("a revision with the same app replaced the entry")
	}
	m.state.Blocks[0].App = &toplist
	m.state.Blocks[0].Rev++
	m.syncTranscript()
	m.Update(struct{}{})
	if m.widgets.entries[id] == original || original.controller.State().CanOpen() {
		t.Fatal("a changed app did not replace the entry")
	}
	runWidgetTasks(m, open)
	if m.widgets.entries[id].controller.State().Open.Phase != viz.Idle {
		t.Fatal("the old revision's open reached the new one")
	}

	replacement := m.widgets.entries[id]
	m.conversationEpoch++
	m.state.Blocks = nil
	m.syncTranscript()
	m.Update(struct{}{})
	if len(m.widgets.entries) != 0 || replacement.controller.State().CanOpen() {
		t.Fatal("conversation change kept widget work")
	}
	m.Detach()
	if !client.closed {
		t.Fatal("client not closed")
	}
}

func TestStreamedAppAdoptsConversationID(t *testing.T) {
	for _, revise := range []bool{false, true} {
		t.Run(fmt.Sprintf("revised=%t", revise), func(t *testing.T) {
			client := &widgetTestClient{}
			m, id, _, _ := widgetModel(t, client, graphics.Unsupported, timeseries)
			defer m.Detach()
			m.widgets.release()
			m.state.ConversationID = ""
			m.Update(struct{}{})
			original := m.widgets.entries[id]
			pending := clickWidget(t, m, chat.WidgetOpen)

			m.state.ConversationID = "assigned"
			if revise {
				m.state.Blocks[0].Rev++
				m.syncTranscript()
			}
			m.Update(struct{}{})
			if m.widgets.entries[id] == original || original.controller.State().CanOpen() {
				t.Fatal("conversation ID assignment kept the old widget's work")
			}
			runWidgetTasks(m, pending)
			if m.widgets.entries[id].controller.State().Open.Phase != viz.Idle {
				t.Fatal("old completion reached the replacement widget")
			}
			runWidgetTasks(m, clickWidget(t, m, chat.WidgetOpen))
			if client.last.ConversationID != "assigned" || client.last.MessageID != "msg" {
				t.Fatalf("restore keys = %q, %q", client.last.ConversationID, client.last.MessageID)
			}
		})
	}
}

// A preview's pixels go to the terminal once, and leave with their widget.
func TestWidgetInlineImage(t *testing.T) {
	client := &widgetTestClient{render: rendered(t)}
	m, id, render, _ := widgetModel(t, client, graphics.Supported, timeseries)
	defer m.Detach()
	if raw := runWidgetTasks(m, render); !strings.Contains(raw, "a=T") {
		t.Fatal("the preview was not transmitted")
	}
	if !strings.ContainsRune(m.list.Document(), kitty.Placeholder) || m.animClock.armed {
		t.Fatal("the preview is not drawn, or its spinner kept running")
	}
	if _, cmd := m.Update(struct{}{}); strings.Contains(runWidgetTasks(m, cmd), "a=T") {
		t.Fatal("an unchanged preview was transmitted again")
	}
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40}); !strings.Contains(runWidgetTasks(m, cmd), "a=p") {
		t.Fatal("a resized preview was not placed again without its pixels")
	}
	m.state.Blocks[0].App = &toplist
	m.state.Blocks[0].Rev++
	m.syncTranscript()
	old := m.widgets.entries[id].image.ID
	_, cmd := m.Update(struct{}{})
	if !strings.Contains(runWidgetTasks(m, cmd), fmt.Sprintf("a=d,d=I,i=%d", old)) {
		t.Fatal("the old revision's image was not deleted")
	}

	// Quitting frees what the terminal stores before the program stops writing.
	stored := m.widgets.entries[id].image.ID
	_, quit := m.quit()
	if raw := runWidgetTasks(m, quit); !strings.Contains(raw, fmt.Sprintf("a=d,d=I,i=%d", stored)) {
		t.Fatalf("quitting did not delete preview %d: %q", stored, raw)
	}
	// Until the program stops, later messages start nothing again.
	if _, cmd := m.Update(struct{}{}); len(m.widgets.entries) != 0 || strings.Contains(runWidgetTasks(m, cmd), "a=T") {
		t.Fatal("work restarted while quitting")
	}
}

// rendered is a successful preview of ddviz's usual size.
func rendered(t *testing.T) viz.Result {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 820, 450))); err != nil {
		t.Fatal(err)
	}
	snapshot, err := viz.NewSnapshot(data.Bytes(), viz.Availability{Available: true})
	if err != nil {
		t.Fatal(err)
	}
	return viz.Result{Status: viz.Status{Phase: viz.Succeeded}, Image: snapshot}
}

// A click that finds the helper unable to run still goes somewhere: the web
// sandbox, which every widget offers from then on. A stored preview changes
// nothing.
func TestWidgetOpenFallsBackToWeb(t *testing.T) {
	for name, support := range map[string]graphics.Support{"pane": graphics.Unsupported, "after a preview": graphics.Supported} {
		t.Run(name, func(t *testing.T) {
			client := &widgetTestClient{render: rendered(t), open: viz.Status{Phase: viz.Unavailable}}
			m, _, render, opened := widgetModel(t, client, support, timeseries)
			defer m.Detach()
			runWidgetTasks(m, render)
			runWidgetTasks(m, clickWidget(t, m, chat.WidgetOpen))
			if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "https://demo.datadoghq.com/dataviz/sandbox?") {
				t.Fatalf("opened %q", *opened)
			}
			if actions := widgetActions(m); len(actions) != 1 || actions[0] != chat.WidgetWeb {
				t.Fatalf("actions = %v, want the web sandbox", actions)
			}
		})
	}
}

// A restore replaces the app's summary: a saved app has no result to title it,
// and a live app restored on retry may bring a new chart.
func TestRestoredAppTakesItsTitle(t *testing.T) {
	for _, tc := range []struct {
		name, before string
		app          assistant.AppPayload
	}{
		{"saved", "Datadog view", assistant.AppPayload{ResourceURI: viz.DatavizURI, Restorable: true}},
		{"live", "Old", chartApp(`{"type":"timeseries","title":"Old"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := rendered(t)
			result.App = &timeseries
			m, _, render, _ := widgetModel(t, &widgetTestClient{render: result, open: viz.Status{Phase: viz.Unavailable}}, graphics.Supported, tc.app)
			defer m.Detach()
			if doc := ansi.Strip(m.list.Document()); !strings.Contains(doc, tc.before) {
				t.Fatalf("view before restore:\n%s", doc)
			}
			runWidgetTasks(m, render)
			m.Update(struct{}{})
			if doc := ansi.Strip(m.list.Document()); !strings.Contains(doc, "CPU") || strings.Contains(doc, tc.before) {
				t.Fatalf("restored app view:\n%s", doc)
			}
		})
	}
}

func TestWidgetWithoutLocalPresentation(t *testing.T) {
	saved := assistant.AppPayload{ResourceURI: viz.DatavizURI, Restorable: true}
	savedIncident := incident
	savedIncident.Restorable = true
	withoutRelay := timeseries
	withoutRelay.RelayHandle = ""
	for _, tc := range []struct {
		name     string
		app      assistant.AppPayload
		client   viz.Client
		support  graphics.Support
		noOrigin bool
		action   chat.WidgetAction
		target   string
	}{
		{name: "saved app without renderer", app: saved, action: chat.WidgetConversation, target: "https://demo.datadoghq.com/ask/conv"},
		{name: "saved app with snapshots and no graphics", app: saved, client: &widgetTestClient{remote: true}, support: graphics.Unsupported, action: chat.WidgetConversation, target: "https://demo.datadoghq.com/ask/conv"},
		{name: "saved incident with snapshots", app: savedIncident, client: &widgetTestClient{remote: true}, support: graphics.Supported, action: chat.WidgetConversation, target: "https://demo.datadoghq.com/ask/conv"},
		{name: "unsaved incident with snapshots", app: incident, client: &widgetTestClient{remote: true}, support: graphics.Supported},
		{name: "unsaved incident without relay", app: incident, client: &widgetTestClient{}},
		{name: "saved app without web origin", app: saved, noOrigin: true},
		{name: "relay-less chart uses the sandbox", app: withoutRelay, client: &widgetTestClient{}, action: chat.WidgetWeb, target: "https://demo.datadoghq.com/dataviz/sandbox?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, id, _, opened := widgetModel(t, tc.client, tc.support, tc.app)
			defer m.Detach()
			if tc.noOrigin {
				m.widgets.origin = ""
				m.Update(struct{}{})
			}
			if m.widgets.entries[id].controller != nil {
				t.Fatal("fallback started renderer work")
			}
			if tc.action == chat.WidgetNone {
				m.list.SetHeader("")
				if doc := ansi.Strip(m.list.Document()); doc != "" || len(widgetActions(m)) != 0 {
					t.Fatalf("hidden view occupies space or takes clicks: %q", doc)
				}
				return
			}
			if tc.action == chat.WidgetConversation {
				title := tc.app.Summary().Title
				if title == "" {
					title = "Datadog view"
				}
				if doc := ansi.Strip(m.list.Document()); !strings.Contains(doc, title+" · Open conversation ↗") {
					t.Fatalf("conversation action not on the title row: %q", doc)
				}
			}
			if actions := widgetActions(m); !slices.Equal(actions, []chat.WidgetAction{tc.action}) {
				t.Fatalf("actions = %v, want %v", actions, tc.action)
			}
			runWidgetTasks(m, clickWidget(t, m, tc.action))
			if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], tc.target) {
				t.Fatalf("opened %q, want %s", *opened, tc.target)
			}
			if client, ok := tc.client.(*widgetTestClient); ok && client.opens != 0 {
				t.Fatal("web action restored the app")
			}
		})
	}
}

func TestSavedAppPreviewKeepsConversationUntilRestored(t *testing.T) {
	chart := rendered(t)
	chart.App = &timeseries
	for _, tc := range []struct {
		name   string
		result viz.Result
		title  string
		action chat.WidgetAction
	}{
		{"chart", chart, "CPU", chat.WidgetWeb},
		{"product card", viz.Result{App: &incident, Status: viz.Status{Phase: viz.Failed, Reason: "This view can't be shown here."}}, "Incident detail", chat.WidgetConversation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &widgetTestClient{remote: true, render: tc.result}
			m, _, render, _ := widgetModel(t, client, graphics.Supported, assistant.AppPayload{ResourceURI: viz.DatavizURI, Restorable: true})
			defer m.Detach()
			if render == nil || !slices.Equal(widgetActions(m), []chat.WidgetAction{chat.WidgetConversation}) {
				t.Fatalf("rendering saved app offers %v", widgetActions(m))
			}
			runWidgetTasks(m, render)
			if client.last.ConversationID != "conv" || client.last.MessageID != "msg" {
				t.Fatalf("restore keys = %q, %q", client.last.ConversationID, client.last.MessageID)
			}
			if actions := widgetActions(m); !slices.Equal(actions, []chat.WidgetAction{tc.action}) {
				t.Fatalf("restored view offers %v, want %v", actions, tc.action)
			}
			if doc := ansi.Strip(m.list.Document()); !strings.Contains(doc, tc.title) || strings.Contains(doc, "can't be shown") {
				t.Fatalf("restored view:\n%s", doc)
			}
		})
	}
}

// A rich definition makes a sandbox URL longer than any OSC 8 hyperlink or web
// link check allows; the action must still open it whole.
func TestLargeWidgetLinkOpens(t *testing.T) {
	random := make([]byte, 12<<10)
	_, _ = rand.Read(random)
	note := chartApp(fmt.Sprintf(`{"type":"note","content":%q}`, hex.EncodeToString(random)))
	m, _, _, opened := widgetModel(t, nil, graphics.Unsupported, note)
	runWidgetTasks(m, clickWidget(t, m, chat.WidgetWeb))
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "https://demo.datadoghq.com/dataviz/sandbox?") || len((*opened)[0]) <= 8<<10 {
		t.Fatalf("opened %d URLs", len(*opened))
	}
}

// runWidgetTasks runs cmd, applies viz completions it yields, and returns the
// raw terminal output written along the way.
func runWidgetTasks(m *Model, cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	var raw strings.Builder
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			raw.WriteString(runWidgetTasks(m, c))
		}
	case tea.RawMsg:
		fmt.Fprint(&raw, msg.Msg)
		_, next := m.Update(msg)
		raw.WriteString(runWidgetTasks(m, next))
	case viz.Completion:
		_, next := m.Update(msg)
		raw.WriteString(runWidgetTasks(m, next))
	case tea.QuitMsg:
	}
	return raw.String()
}

// A Datadog snapshot draws inline and leaves the web sandbox as the action.
func TestRemoteWidgetPreview(t *testing.T) {
	result := rendered(t)
	result.Image, _ = viz.NewSnapshot(result.Image.PNG(), viz.Availability{})
	client := &widgetTestClient{remote: true, render: result}
	m, _, render, _ := widgetModel(t, client, graphics.Supported, timeseries)
	defer m.Detach()
	if raw := runWidgetTasks(m, render); !strings.Contains(raw, "a=T") {
		t.Fatal("snapshot not transmitted")
	}
	m.Update(struct{}{})
	if actions := widgetActions(m); len(actions) != 1 || actions[0] != chat.WidgetWeb || client.opens != 0 {
		t.Fatalf("actions = %v, opens = %d", actions, client.opens)
	}
}
