"""Run with BITS_FAKE_BACKEND=1 BITS_VIZ=snapshot and a Bits login:
  load("widgets.star", "gallery"); gallery()

Only assistant messages are faked. Charts render as Datadog snapshots of the
get_widget payload inside each app's result. The fake opens no relay session,
so ddviz cannot load data for these apps. saved() can: see its comment.
CPU metrics depend on the account; an empty chart is not necessarily a failure.

Payloads match the backend's mcp_app content: the title lives inside the
widget definition, and get_widget pins a window in Unix seconds. Fixtures
without one let the chart show the last hour.
"""

def _definition(title, query="avg:system.cpu.user{*}", kind="timeseries"):
    return {"type": kind, "title": title, "requests": [{"q": query}]}

def _app(result, id="", tool="show_ui", resource="ui://apps/dataviz"):
    raw({
        "type": "mcp_app",
        "tool_call_id": "call-" + (id or "fixture"),
        "tool_name": tool,
        "tool_input": {},
        "resource_uri": resource,
        "resource_handle": "dataviz-mcp-ui@prototype",
        "relay_handle": None,
        "tool_result": result,
        "restoration": None,
    }, id=id)

def _chart(definition, id="", from_s=0, to_s=0):
    payload = {"source_tool": "get_widget", "widget_definition": definition}
    if to_s:
        payload["from_ts"] = from_s
        payload["to_ts"] = to_s
    _app({"content": [{"type": "text", "text": definition.get("title", "")}], "structuredContent": payload}, id=id)

def saved(message):
    """A real app the assistant saved, at message in the conversation given by
    --conversation. Opening it restores it from Datadog, with a live relay
    session, so ddviz loads its real data:
      BITS_FAKE_BACKEND=1 bits --conversation CONVERSATION_ID
      load("widgets.star", "saved"); saved("MESSAGE_ID")
    """
    raw({
        "type": "mcp_app_saved",
        "tool_call_id": "call-saved",
        "tool_name": "show_ui",
        "resource_uri": "ui://apps/dataviz",
        "resource_handle": "dataviz-mcp-ui@prototype",
    }, id=message)

def gallery():
    say("## Widget gallery\nResize, then scroll away and back. Previews should be retained. The incident card has no snapshot preview: it shows its title only.")
    _chart(_definition("CPU timeseries"))
    _chart(_definition("CPU query value", kind="query_value"))
    _chart(_definition("CPU by host", query="avg:system.cpu.user{*} by {host}", kind="toplist"))
    _chart(_definition("Empty result", query="avg:system.cpu.user{bits_viz_fixture:deliberately_absent}"))
    _chart(_definition("A long widget title to inspect wrapping and action placement when the terminal becomes narrow"))
    _app({"content": [], "_meta": {"datadog/show_ui": {"view_spec": {"schema_version": 1, "data_tool": {"name": "get_datadog_incident", "args": {"incident_id": "1"}}, "view": {"name": "incident_detail", "version": 1, "args": {"incident_id": "1"}}}}}})
    say("\n".join(["Scroll spacer %d" % i for i in range(40)]))
    say("Scroll back to the charts: cached previews should reappear without fetching again.")

def timing(from_s, to_s):
    """Use a recognizable historical interval within your metric retention."""
    if from_s <= 0 or to_s <= from_s:
        fail(400, "timing requires positive Unix seconds with from_s < to_s")
    say("## Time range\nCompare axis labels and data in the preview and the web sandbox. The chart must use %d–%d seconds, not the current hour." % (from_s, to_s))
    _chart(_definition("Fixed historical range"), from_s=from_s, to_s=to_s)

def burst(count=8):
    say("## Concurrent previews\nScroll through these charts to start previews, then use /new while work is pending. Old completions must not appear in the new conversation.")
    for i in range(count):
        metric = "user" if i % 2 == 0 else "system"
        _chart(_definition("Chart %d: CPU %s" % (i + 1, metric), query="avg:system.cpu.%s{*}" % metric))

def revision(delay="2s"):
    say("## Replaced widget\nThe same message ID changes from user CPU to system CPU. Only the latest revision should remain, even if the first preview is still loading.")
    _chart(_definition("Before: CPU user"), id="widget-revision")
    sleep(delay)
    _chart(_definition("After: CPU system", query="avg:system.cpu.system{*}"), id="widget-revision")

def failures():
    say("## Failure isolation\nInvalid apps should show a failure or a plain \"Datadog view\" without preventing the final valid chart from loading. Retry is offered only when the client advises it; these inputs do not simulate network failures.")
    _chart({"title": "Missing widget type"})
    _chart(_definition("Malformed query", query="avg:"))
    _chart({"type": "bits_fixture_unknown", "title": "Unsupported widget type", "requests": []})
    _app({"content": []}, resource="ui://apps/unknown")
    _chart(_definition("Valid chart after failures"))
