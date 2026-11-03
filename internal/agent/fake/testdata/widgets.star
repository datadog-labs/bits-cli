"""Run with BITS_FAKE_BACKEND=1 and a built ddviz helper:
  load("widgets.star", "gallery"); gallery()

Only assistant messages are faked. Rendering needs a Bits login and MCP access.
CPU metrics depend on the account; an empty chart is not necessarily a failure.

Payloads match the backend's: the title lives inside widget_def, and the
backend always pins a fixed time window in milliseconds. Fixtures without one
let the chart show recent data.
"""

def _definition(title, query="avg:system.cpu.user{*}", kind="timeseries", from_ms=None, to_ms=None):
    definition = {"type": kind, "title": title, "requests": [{"q": query}]}
    if from_ms != None:
        definition["time"] = {"type": "fixed", "from": from_ms, "to": to_ms}
    return definition

def _widget(definition, id=""):
    raw({"type": "widget_def", "widget_def": definition}, id=id)

def gallery():
    say("## Widget gallery\nOpen while a preview loads, reopen it, resize, then scroll away and back. Previews should be retained; opening should reuse one interactive pane.")
    _widget(_definition("CPU timeseries"))
    _widget(_definition("CPU query value", kind="query_value"))
    _widget(_definition("CPU by host", query="avg:system.cpu.user{*} by {host}", kind="toplist"))
    _widget(_definition("Empty result", query="avg:system.cpu.user{bits_viz_fixture:deliberately_absent}"))
    _widget(_definition("A long widget title to inspect wrapping and action placement when the terminal becomes narrow"))
    say("\n".join(["Scroll spacer %d" % i for i in range(40)]))
    say("Scroll back to the charts: cached previews should reappear without fetching again.")

def timing(from_ms, to_ms):
    """Use a recognizable historical interval within your metric retention."""
    if from_ms <= 0 or to_ms <= from_ms:
        fail(400, "timing requires positive Unix milliseconds with from_ms < to_ms")
    say("## Time range\nCompare axis labels and data in the preview, the interactive pane, and the web sandbox. The chart must use %d–%d milliseconds, not the current hour. Reopening must preserve that range." % (from_ms, to_ms))
    _widget(_definition("Fixed historical range", from_ms=from_ms, to_ms=to_ms))

def burst(count=8):
    say("## Concurrent previews\nScroll through these charts to start previews. Open one while previews are loading; then use /new while work is pending. Old completions must not appear in the new conversation.")
    for i in range(count):
        metric = "user" if i % 2 == 0 else "system"
        _widget(_definition("Chart %d: CPU %s" % (i + 1, metric), query="avg:system.cpu.%s{*}" % metric))

def revision(delay="2s"):
    say("## Replaced widget\nThe same widget ID changes from user CPU to system CPU. Only the latest revision should remain, even if the first preview is still loading.")
    _widget(_definition("Before: CPU user"), id="widget-revision")
    sleep(delay)
    _widget(_definition("After: CPU system", query="avg:system.cpu.system{*}"), id="widget-revision")

def failures():
    say("## Failure isolation\nInvalid definitions should show readable failures without preventing the final valid chart from loading. Retry is offered only when the client advises it; these inputs do not simulate network failures.")
    _widget({"title": "Missing widget type"})
    _widget(_definition("Malformed query", query="avg:"))
    _widget({"type": "bits_fixture_unknown", "title": "Unsupported widget type", "requests": []})
    # Legacy content: no backend writes it today; it shows only its title.
    raw({"type": "widget", "title": "Legacy tile without a widget definition", "tile_def": {}, "timeframe": {"start": 1700000000000, "end": 1700003600000, "paused": True}})
    _widget(_definition("Valid chart after failures"))
