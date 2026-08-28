# style-guide

A dev-only Bubble Tea catalog for the TUI design system: shared components,
text attributes, semantic roles, markdown elements, and color tokens, rendered
at live terminal width with a dark/light toggle. It has no engine, backend,
network, or auth.

It imports the real shared theme, panel, selector, and chat renderers, so what
you see is exactly what production screens render — no copied style values that
could drift.

## Run

```sh
go run ./tools/style-guide
```

On-screen hints cover the navigation and toggle keys.
