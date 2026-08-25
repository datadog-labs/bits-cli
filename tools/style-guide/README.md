# style-guide

A dev-only Bubble Tea page that catalogs the chat TUI's visual language: every
text attribute, semantic style role, markdown element, and color token, rendered
at live terminal width with a dark/light toggle. It has no engine, backend,
network, or auth.

It imports the real `internal/tui/chat` styles (`chat.DefaultStyles`,
`chat.RenderBlock`, `chat.RenderMarkdown`), so what you see is exactly what a
transcript renders — no copied style values that could drift.

## Run

```sh
go run ./tools/style-guide
```

On-screen hints cover the navigation and toggle keys.
