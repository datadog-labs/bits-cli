package tui

import (
	"bytes"
	"cmp"
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
	"github.com/datadog-labs/bits-cli/internal/tui/splash"
	"github.com/datadog-labs/bits-cli/internal/viz"
)

// widgetMode is how much of a widget this setup can show.
type widgetMode uint8

const (
	widgetLink   widgetMode = iota // no renderer, or nothing it can show: a link to the web sandbox
	widgetPane                     // a panel without terminal images: open the panel
	widgetInline                   // terminal images: an inline preview, and the panel if any
)

const (
	// Images take four fifths of the transcript width, centered in a
	// full-width band, up to a compact chart.
	widgetMaxColumns = 80
	widgetMaxRows    = 16
	// Until a snapshot arrives, the box assumes ddviz's usual aspect ratio.
	widgetDefaultAspect = 450.0 / 820.0
)

// widgetMode is the only place capabilities combine. A pending probe counts as
// inline, so the box is reserved before the terminal answers.
func (m *Model) widgetMode() widgetMode {
	switch {
	case m.widgets.client == nil || m.widgets.unavailable:
		return widgetLink
	case m.graphics.Support == graphics.Unsupported && !m.widgets.client.Interactive():
		return widgetLink
	case m.graphics.Support == graphics.Unsupported:
		return widgetPane
	default:
		return widgetInline
	}
}

func (m *Model) syncWidgets() tea.Cmd {
	switch {
	case m.op.loggingOut() || m.loggedOut:
		// Until the program quits, nothing may start with revoked credentials.
		m.list.ClearWidgetViews()
		return m.widgets.images.Enqueue(graphics.Delete(m.widgets.release()...))
	case m.mode != ModeChat || m.frame.tooSmall:
		// Nobody sees the transcript: start nothing, keep what exists.
		return nil
	}
	return m.widgets.sync(m.list, m.widgetMode(), m.styles.IsDark, m.conversationEpoch, m.state.ConversationID)
}

// quitWith frees the images this session stored in the terminal, then runs
// quit. They go first, in one write: once the program stops, nothing reaches
// the terminal, and it would keep them across runs.
func (m *Model) quitWith(quit tea.Cmd) tea.Cmd {
	m.widgets.closed = true // the rest of this Update must not start work again
	if m.list != nil {
		m.list.ClearWidgetViews()
	}
	images := m.widgets.release()
	if m.graphics.Support == graphics.Supported {
		images = append(images, graphics.Image{ID: splash.ImageID})
	}
	deleteCmd := m.widgets.images.Enqueue(graphics.Delete(images...))
	return tea.Batch(deleteCmd, m.widgets.images.DrainThen(quit))
}

// widgets owns app operations and stored images for the transcript.
type widgets struct {
	images      graphics.Queue
	client      viz.Client // nil: links only
	origin      string     // web app origin for sandbox links, "" if unknown
	unavailable bool       // the renderer turned out not to run; links for the session
	closed      bool       // quitting: nothing starts again
	entries     map[agent.BlockID]*widgetEntry
	epoch       uint64
	nextID      int
}

// widgetEntry is one app revision: its operations and stored image.
type widgetEntry struct {
	rev int
	// request captures the first payload and theme for this block revision.
	request    viz.Request
	summary    assistant.ViewSummary // of the latest result; empty until there is one
	controller *viz.Controller       // created on first use outside link mode
	image      graphics.Image
	snapshot   *viz.Snapshot // the one stored under image.ID
}

// sync reconciles every app block with its operations and view, so heights
// never change on scroll and finished work stops its spinner. Only visible
// blocks start renders and send pixels.
func (w *widgets) sync(list *chat.List, mode widgetMode, dark bool, epoch uint64, conversationID string) tea.Cmd {
	if w.closed {
		return nil
	}
	var cmds []tea.Cmd
	if w.epoch != epoch {
		cmds = append(cmds, w.images.Enqueue(graphics.Delete(w.release()...)))
		w.epoch = epoch
	}
	live := make(map[agent.BlockID]bool)
	// A second pass catches a widget that a resized box moved into view.
	for range 2 {
		blocks, visible := list.Widgets()
		views := make(map[agent.BlockID]chat.WidgetView, len(blocks))
		for _, block := range blocks {
			if !block.Complete || (block.App == nil && block.Widget == nil) {
				continue
			}
			live[block.ID] = true
			e, cmd := w.entry(block, conversationID, dark)
			cmds = append(cmds, cmd)
			view, cmd := w.view(e, mode, visible[block.ID], list.Width())
			cmds = append(cmds, cmd)
			views[block.ID] = view
		}
		if !list.SetWidgetViews(views) {
			break
		}
	}
	for id := range w.entries {
		if !live[id] {
			cmds = append(cmds, w.remove(id))
		}
	}
	return tea.Batch(cmds...)
}

// webAction uses the single-chart contract or the saved app's conversation
// identity. The app does not need to be restored to open its conversation.
func (w *widgets) webAction(e *widgetEntry) chat.WidgetAction {
	if w.origin == "" {
		return chat.WidgetNone
	}
	if e.summary.Chart != nil && (e.request.LegacyWidget != nil || e.request.App.ResourceURI == viz.DatavizURI) {
		return chat.WidgetWeb
	}
	if e.request.CanRestore() {
		return chat.WidgetConversation
	}
	return chat.WidgetNone
}

func (w *widgets) view(e *widgetEntry, mode widgetMode, visible bool, width int) (chat.WidgetView, tea.Cmd) {
	view := chat.WidgetView{Title: e.summary.Title}
	request := e.request
	var state viz.State
	if e.controller != nil {
		request.App = e.controller.App()
		state = e.controller.State()
	}
	app := request.App
	hasResult := len(app.Result) > 0
	local := mode != widgetLink && w.client.Interactive()
	supported := app.ResourceURI == viz.DatavizURI
	canOpen := local && supported && (hasResult && app.RelayHandle != "" || request.CanRestore())
	canSnapshot := !local && supported && (e.summary.Chart != nil || !hasResult && request.CanRestore())
	if request.LegacyWidget != nil {
		view.Title = cmp.Or(view.Title, "Widget")
		canOpen = local && e.summary.Chart != nil
		canSnapshot = !local && e.summary.Chart != nil
	}
	preview := mode == widgetInline && (canOpen || canSnapshot)

	var cmd tea.Cmd
	if (canOpen || preview) && e.controller == nil {
		e.controller = viz.NewController(w.client, e.request)
	}
	if preview && visible && state.Render.Phase == viz.Idle {
		cmd = widgetTask(e.controller.Render(context.Background()))
		state = e.controller.State()
	}
	fallback := w.webAction(e)
	view.Note = failureReason(state.Open)
	switch {
	case state.Open.Phase == viz.Loading:
		view.Loading = "Opening"
	case preview && state.Render.Phase != viz.Idle && state.CanRender():
		view.Action, view.Link = chat.WidgetRetry, fallback
	case canOpen && state.CanOpen():
		view.Action = chat.WidgetOpen
	default:
		view.Action = fallback
	}
	if preview {
		if state.Image != nil || state.Render.Phase == viz.Loading || state.Render.Phase == viz.Idle {
			var draw tea.Cmd
			view.Image, draw = e.place(state.Image, width, visible)
			cmd = tea.Batch(cmd, w.images.Enqueue(draw))
		}
		if view.Loading == "" && state.Render.Phase == viz.Loading {
			view.Loading = "Rendering"
		}
		if view.Note == "" {
			view.Note = failureReason(state.Render)
		}
	}
	view.Hidden = request.LegacyWidget == nil && view.Action == chat.WidgetNone && view.Link == chat.WidgetNone && view.Image.Rows == 0 && view.Loading == "" && view.Note == ""
	return view, cmd
}

// entry returns the block's current entry, replacing one whose view changed.
func (w *widgets) entry(block agent.Block, conversationID string, dark bool) (*widgetEntry, tea.Cmd) {
	e := w.entries[block.ID]
	request := viz.Request{LegacyWidget: block.Widget, ConversationID: conversationID, MessageID: block.MessageID, Dark: dark}
	if block.App != nil {
		request.App = *block.App // revisions replace payloads without mutating them
	}
	// A newly assigned conversation ID lets an existing app be restored.
	if e != nil && e.request.ConversationID == conversationID && (e.rev == block.Rev || sameView(e.request, request)) {
		e.rev = block.Rev
		return e, nil
	}
	cmd := w.remove(block.ID)
	if w.entries == nil {
		w.entries = make(map[agent.BlockID]*widgetEntry)
	}
	w.nextID++
	summary := request.App.Summary()
	if block.Widget != nil {
		summary = assistant.ViewSummary{Title: block.Widget.Title, Chart: block.Widget.Chart(time.Now())}
	}
	e = &widgetEntry{
		rev:     block.Rev,
		request: request,
		summary: summary,
		image:   graphics.Image{ID: 0xB10000 + w.nextID},
	}
	w.entries[block.ID] = e
	return e, cmd
}

func sameView(left, right viz.Request) bool {
	if a, b := left.LegacyWidget, right.LegacyWidget; a != nil || b != nil {
		return a != nil && b != nil && a.Title == b.Title && bytes.Equal(a.Definition, b.Definition)
	}
	a, b := left.App, right.App
	return a.ToolCallID == b.ToolCallID && a.ToolName == b.ToolName && a.ResourceURI == b.ResourceURI &&
		a.RelayHandle == b.RelayHandle && a.Restorable == b.Restorable &&
		bytes.Equal(a.ToolInput, b.ToolInput) && bytes.Equal(a.Result, b.Result)
}

// apply routes a completion to its controller. A helper that cannot run
// switches every widget to links rather than failing them one by one. When
// that answers an Open, it returns the web page to open instead, so the
// click still goes somewhere.
func (w *widgets) apply(done viz.Completion) string {
	for _, e := range w.entries {
		if e.controller == nil {
			continue
		}
		// Only an Open in flight can answer the click; a snapshot marking its
		// widget not interactive leaves an idle Open, a limit of that widget.
		opening := e.controller.State().Open.Phase == viz.Loading
		if !e.controller.Apply(done) {
			continue
		}
		// A restore replaces the app, saved or live, with a new result; a failed
		// restore leaves none, and the last summary stands.
		if app := e.controller.App(); len(app.Result) > 0 {
			e.summary = app.Summary()
		}
		state := e.controller.State()
		openFailed := opening && state.Open.Phase == viz.Unavailable
		if state.Render.Phase == viz.Unavailable || openFailed {
			w.unavailable = true
		}
		if openFailed {
			return w.webURL(e, w.webAction(e))
		}
		return ""
	}
	return ""
}

func (w *widgets) webURL(e *widgetEntry, action chat.WidgetAction) string {
	var target string
	var err error
	switch action {
	case chat.WidgetWeb:
		chart := e.summary.Chart
		if e.request.LegacyWidget != nil {
			chart = e.request.LegacyWidget.Chart(time.Now())
		}
		if chart == nil {
			return ""
		}
		target, err = viz.SandboxURL(w.origin, *chart)
	case chat.WidgetConversation:
		target, err = browser.ConversationURL(w.origin, e.request.ConversationID)
	case chat.WidgetNone, chat.WidgetOpen, chat.WidgetRetry:
		return ""
	}
	if err != nil {
		return ""
	}
	return target
}

// click starts the operation or constructs the selected web destination.
func (w *widgets) click(hit chat.LinkHit) (tea.Cmd, string) {
	e := w.entries[hit.BlockID]
	if e == nil {
		return nil, ""
	}
	switch hit.Action {
	case chat.WidgetWeb, chat.WidgetConversation:
		return nil, w.webURL(e, hit.Action)
	case chat.WidgetOpen, chat.WidgetRetry:
		if e.controller == nil {
			return nil, ""
		}
		if hit.Action == chat.WidgetRetry {
			return widgetTask(e.controller.Render(context.Background())), ""
		}
		return widgetTask(e.controller.Open(context.Background())), ""
	case chat.WidgetNone:
	}
	return nil, ""
}

// release drops every entry, canceling its work, and returns the images the
// terminal still stores for them.
func (w *widgets) release() []graphics.Image {
	var stored []graphics.Image
	for id := range w.entries {
		if image, ok := w.drop(id); ok {
			stored = append(stored, image)
		}
	}
	return stored
}

// close releases the client once the program has stopped. Images must be
// freed before that, by quitWith.
func (w *widgets) close() {
	w.release()
	if w.client != nil {
		_ = w.client.Close()
	}
}

func (w *widgets) remove(id agent.BlockID) tea.Cmd {
	image, ok := w.drop(id)
	if !ok {
		return nil
	}
	return w.images.Enqueue(graphics.Delete(image))
}

// drop removes an entry without scheduling a terminal write. release collects
// those writes into one deletion command.
func (w *widgets) drop(id agent.BlockID) (graphics.Image, bool) {
	e := w.entries[id]
	if e == nil {
		return graphics.Image{}, false
	}
	delete(w.entries, id)
	if e.controller != nil {
		e.controller.Close()
	}
	return e.image, e.snapshot != nil
}

// place sizes the box to the transcript width. When visible, it stores a new
// snapshot, or only re-places a stored one whose size changed. Without pixels
// the box is reserved at its final size.
func (e *widgetEntry) place(snapshot *viz.Snapshot, width int, visible bool) (graphics.Image, tea.Cmd) {
	aspect := widgetDefaultAspect
	if snapshot != nil {
		aspect = float64(snapshot.Height()) / float64(snapshot.Width())
	}
	// Cells are about twice as tall as wide.
	columns := max(1, min(widgetMaxColumns, width*4/5))
	box := graphics.Image{ID: e.image.ID, Columns: columns, Rows: max(4, min(widgetMaxRows, int(float64(columns)*aspect/2)))}
	if snapshot == nil {
		box.ID = 0
		return box, nil
	}
	if snapshot == e.snapshot && box == e.image {
		return box, nil
	}
	if !visible {
		box.ID = 0
		return box, nil
	}
	resized := snapshot == e.snapshot
	e.image, e.snapshot = box, snapshot
	if resized {
		return box, box.Place()
	}
	return box, box.Transmit(snapshot.PNG())
}

func widgetTask(task viz.Task) tea.Cmd {
	if task == nil {
		return nil
	}
	return func() tea.Msg { return task() }
}

// failureReason is shown only for failures; unsupported setups degrade
// silently through widgetMode.
func failureReason(s viz.Status) string {
	if s.Phase != viz.Failed {
		return ""
	}
	if s.Reason == "" {
		return "Visualization could not be loaded."
	}
	return s.Reason
}
