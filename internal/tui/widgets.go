package tui

import (
	"context"
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
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
	return m.widgets.sync(m.list, m.widgetMode(), m.styles.IsDark, m.conversationEpoch)
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

// widgets owns widget operations and stored images for the transcript.
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

// widgetEntry is one widget revision: its operations and stored image.
type widgetEntry struct {
	rev        int
	raw        []byte                     // the definition as JSON, to spot real changes
	def        assistant.WidgetDefinition // private copy; viz borrows it
	controller *viz.Controller            // created on first use outside link mode
	image      graphics.Image
	snapshot   *viz.Snapshot // the one stored under image.ID
}

// sync reconciles every widget block with its operations and view, so heights
// never change on scroll and finished work stops its spinner. Only visible
// blocks start renders and send pixels.
func (w *widgets) sync(list *chat.List, mode widgetMode, dark bool, epoch uint64) tea.Cmd {
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
		resized := false
		for _, block := range blocks {
			if !block.Complete || block.Widget == nil {
				continue
			}
			live[block.ID] = true
			e, cmd := w.entry(block)
			cmds = append(cmds, cmd)
			var view chat.WidgetView // a legacy tile: its title, nothing to act on
			if e != nil {
				view, cmd = w.view(e, mode, dark, visible[block.ID], list.Width())
				cmds = append(cmds, cmd)
			}
			resized = list.SetWidgetView(block.ID, view) || resized
		}
		if !resized {
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

func (w *widgets) view(e *widgetEntry, mode widgetMode, dark bool, visible bool, width int) (chat.WidgetView, tea.Cmd) {
	var view chat.WidgetView
	fallback := chat.WidgetNone
	if w.origin != "" {
		fallback = chat.WidgetWeb
	}
	if mode == widgetLink {
		view.Action = fallback
		return view, nil
	}
	if e.controller == nil {
		// The theme is fixed per revision; the terminal reports it at startup.
		e.controller = viz.NewController(w.client, viz.Request{Definition: e.def, Dark: dark})
	}
	var cmd tea.Cmd
	state := e.controller.State()
	if mode == widgetInline && visible && state.Render.Phase == viz.Idle {
		cmd = widgetTask(e.controller.Render(context.Background()))
		state = e.controller.State()
	}

	retry := mode == widgetInline && state.CanRender() && state.Render.Phase != viz.Idle
	switch {
	case state.Open.Phase == viz.Loading:
		view.Loading = "Opening"
	case retry:
		view.Action = chat.WidgetRetry
	case state.CanOpen() && w.client.Interactive(): // a snapshot-only client never opens
		view.Action = chat.WidgetOpen
	default:
		view.Action = fallback
	}
	view.Note = failureReason(state.Open)
	if mode == widgetInline {
		var draw tea.Cmd
		view.Image, draw = e.place(state.Image, width, visible)
		cmd = tea.Batch(cmd, w.images.Enqueue(draw))
		if view.Loading == "" && state.Render.Phase == viz.Loading {
			view.Loading = "Rendering"
		}
		if view.Note == "" {
			view.Note = failureReason(state.Render)
		}
	}
	return view, cmd
}

// entry returns the block's current entry, replacing one whose definition
// changed, or nil when the block has nothing to display.
func (w *widgets) entry(block agent.Block) (*widgetEntry, tea.Cmd) {
	e := w.entries[block.ID]
	if e != nil && e.rev == block.Rev {
		return e, nil
	}
	if block.Widget.WidgetDef == nil {
		return nil, w.remove(block.ID)
	}
	// Blocks are mutated in place as they stream: keep a private copy.
	raw, err := json.Marshal(block.Widget.WidgetDef)
	if err != nil {
		return nil, w.remove(block.ID)
	}
	if e != nil && string(raw) == string(e.raw) {
		e.rev = block.Rev // a revision that changed nothing to draw
		return e, nil
	}
	cmd := w.remove(block.ID)
	var def assistant.WidgetDefinition
	if json.Unmarshal(raw, &def) != nil {
		return nil, cmd
	}
	if w.entries == nil {
		w.entries = make(map[agent.BlockID]*widgetEntry)
	}
	w.nextID++
	e = &widgetEntry{rev: block.Rev, raw: raw, def: def, image: graphics.Image{ID: 0xB10000 + w.nextID}}
	w.entries[block.ID] = e
	return e, cmd
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
		state := e.controller.State()
		openFailed := opening && state.Open.Phase == viz.Unavailable
		if state.Render.Phase == viz.Unavailable || openFailed {
			w.unavailable = true
		}
		if openFailed && w.origin != "" {
			link, _ := viz.SandboxURL(w.origin, e.def)
			return link
		}
		return ""
	}
	return ""
}

// click starts the operation behind a widget's action, or returns the web
// page to open. That URL carries the whole definition, too long for a
// hyperlink, so it is built only on click. The list reports actions only for
// views sync keeps current.
func (w *widgets) click(hit chat.LinkHit) (tea.Cmd, string) {
	e := w.entries[hit.BlockID]
	switch {
	case e == nil:
		return nil, ""
	case hit.Action == chat.WidgetWeb:
		link, _ := viz.SandboxURL(w.origin, e.def)
		return nil, link
	case e.controller == nil:
		return nil, ""
	case hit.Action == chat.WidgetRetry:
		return widgetTask(e.controller.Render(context.Background())), ""
	default:
		return widgetTask(e.controller.Open(context.Background())), ""
	}
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
