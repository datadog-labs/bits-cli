# ddviz

macOS viewer for Datadog visualizations: a WebKit panel hosting the remote
Datadog visualization app (`assets/mcp-host.html`). Bits drives it over
stdio JSON-RPC and configures it with `DDVIZ_*` environment variables,
including `DDVIZ_VERSION`, the version it reports.

It requires macOS 13 and builds with the Xcode command line tools
(`xcode-select --install`):

```sh
libexec/ddviz/build.sh build/ddviz    # build/ddviz/{ddviz,assets/}
```

Swift build state is kept in `build/.cache/ddviz/<arch>/`.

## Origin

Ported from [DataDog/datadog-client-plugin](https://github.com/DataDog/datadog-client-plugin)
at `62bb0c4b92f76cecdf2493448ff5ed6761beb814`:
`plugins/shared-viz/template.ddviz.swift`, `plugins/shared-viz/assets/mcp-host.html`,
and `datadog-icon.png`.

When bringing over upstream changes, keep these adaptations:

- Template placeholders are filled in, and the version comes from `DDVIZ_VERSION`.
- Trusted production `DDVIZ_FRAME_SRC` overrides work without debug mode.
- The host allows 30 seconds for initialization.
- The `message.text` capability is omitted until composer integration exists.
