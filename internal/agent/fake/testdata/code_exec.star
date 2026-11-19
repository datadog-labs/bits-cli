"""execute_code and search_datadog_sdk QA for BITS_FAKE_BACKEND=1. Start with
`BITS_FAKE_BACKEND=1 go run .`, then:

  load("code_exec.star", "all"); all()

or one endpoint's wire shapes:

  load("code_exec.star", "regular"); regular()
  load("code_exec.star", "multi_agent"); multi_agent()

The regular endpoint sends the script's result as the output, and a failure as
the backend's error text. The multi-agent endpoint sends no output. Both title
the call and put the script in a Markdown detail, which the transcript ignores.

What to check:
- Every call joins the closed "Analyzed" group, which shows a call only
  while it runs. Open the group to see each call with its intent.
- Collapsed: each call is one header line with its intent.
- Expanded (Ctrl+O, or click the call): the script shows highlighted behind
  │, every line wrapped, then the console, error, or value as indented JSON.
- A failed API call adds "· 1 failed API call" to the header.
- Expanded multi-agent calls show only their script, never "(no output)".
"""

_SEARCH = """const ops = catalog.operations.filter(o => o.tags?.includes("Logs"));
return ops.map(o => ({ id: o.id, summary: o.summary })).slice(0, 3);"""

_COUNT = """import { client, v2 } from "@datadog/datadog-api-client";

const logs = new v2.LogsApi(client.createConfiguration());
const result = await logs.aggregateLogs({
  body: {
    filter: {
      query: "service:checkout status:error",
      from: dd.time.hoursAgo(1),
      to: dd.time.now(),
    },
    compute: [{ aggregation: "count" }],
    groupBy: [{ facet: "@error.message", limit: 3, sort: { aggregation: "count", order: "desc", type: "measure" } }],
  },
});

const buckets = result.data?.buckets ?? [];
console.log(`${buckets.length} error families`);
return buckets.map(b => ({ message: b.by?.["@error.message"], count: b.computes?.c0 }));"""

_PARTIAL = """import { client, v1, v2 } from "@datadog/datadog-api-client";

const config = client.createConfiguration();
const [monitors, incidents] = await Promise.allSettled([
  new v1.MonitorsApi(config).listMonitors({ tags: "service:checkout" }),
  new v2.IncidentsApi(config).listIncidents(),
]);
return {
  monitors: monitors.status === "fulfilled" ? monitors.value.length : null,
  incidents: incidents.status === "fulfilled" ? incidents.value.data.length : String(incidents.reason),
};"""

_THROW = """throw new Error("boom");"""

_COUNT_OUT = '{"outcome":"success","success":true,"value":[{"count":1204,"message":"context deadline exceeded"},{"count":87,"message":"connection reset by peer"},{"count":12,"message":"invalid card number"}],"console":["[log] 3 error families"],"duration":"651ms","diagnostics":{"api_calls":{"total":1,"succeeded":1,"failed":0},"console_truncated":false}}'

_PARTIAL_OUT = '{"outcome":"partial","success":true,"partial":true,"value":{"incidents":"Error: 403 Forbidden","monitors":4},"duration":"1.2s","diagnostics":{"api_calls":{"total":2,"succeeded":1,"failed":1,"failures":[{"target":"v2.IncidentsApi.listIncidents","code":"403","message":"Forbidden"}]},"console_truncated":false}}'

_THROW_OUT = """{"description": "The tool raised an unknown error: ''MCP server:datadog error: [{'type': 'text', 'text': '{\\"outcome\\":\\"failure\\",\\"failure_code\\":\\"script_error\\",\\"success\\":false,\\"error\\":\\"Error: boom\\\\\\\\n    at <anonymous> (script.js:1:11)\\",\\"duration\\":\\"324ms\\"}'}]''"}"""

def _fence(code):
    return "```javascript\n" + code + "\n```"

def _run(code, intent, out, err=False, multi_agent=False, delay="1s"):
    title = "Failed to run code" if err else "Ran code"
    detail = _fence(code)
    if multi_agent:
        out = ""
        if err:
            detail = "Tool call failed."
    tool("execute_code", {"code": code, "telemetry": {"intent": intent}}, out=out, err=err, title=title, detail=detail, delay=delay)

def _search(delay):
    tool([
        ("tool_search_tool_regex", {"pattern": "execute_code|search_datadog_sdk", "limit": 10}, '{"tool_references": ["execute_code", "search_datadog_sdk"]}'),
        ("search_datadog_sdk", {"code": _SEARCH}, '[{"id":"v2.LogsApi.aggregateLogs","summary":"Aggregate events"},{"id":"v2.LogsApi.listLogs","summary":"Search logs"},{"id":"v2.LogsApi.listLogsGet","summary":"Search logs (GET)"}]'),
    ], delay=delay)

def regular(delay="1s"):
    think("I'll find the logs API, then count checkout errors.")
    _search(delay)
    _run(_COUNT, "Count checkout error logs by message in the last hour", _COUNT_OUT, delay=delay)
    _run(_PARTIAL, "Count checkout monitors and open incidents", _PARTIAL_OUT, delay=delay)
    _run(_THROW, "Throw on purpose to check error handling", _THROW_OUT, err=True, delay=delay)
    say("Checkout logged 1,304 errors in the last hour, mostly context deadlines.")

def multi_agent(delay="1s"):
    think("Same reads on the multi-agent endpoint, which sends no results.")
    _run(_COUNT, "Count checkout error logs by message in the last hour", _COUNT_OUT, multi_agent=True, delay=delay)
    _run(_THROW, "Throw on purpose to check error handling", "", err=True, multi_agent=True, delay=delay)
    say("Done.")

def all(delay="1s"):
    regular(delay)
    multi_agent(delay)
