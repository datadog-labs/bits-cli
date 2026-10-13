"""Preview with BITS_FAKE_BACKEND=1:
  load("fakedata/links.star", "links"); links()
"""

_INLINE = (
    "See [the Datadog docs](https://docs.datadoghq.com/logs/?tab=search&lt=2) for details, " +
    "or **check [`dd-trace-go`](https://github.com/DataDog/dd-trace-go)** and " +
    "_[monitors](https://app.datadoghq.com/monitors/manage)_.\n\n" +
    "Formatted titles: [**bold**, *italic*, and `code`](https://example.com/formatted).\n\n" +
    "A long one wraps: [this is a deliberately very long link title that should wrap " +
    "across several lines in a narrow terminal](https://example.com/wrap).\n\n" +
    "~~before [docs](https://example.com) after~~\n\n" +
    "![use [A & B](https://docs.example)](https://image.example)\n\n"
)

_LIST = (
    "- list [item link](https://example.com/item)\n" +
    "- anchor [jump](#links), empty [](https://example.com/empty)\n" +
    "- nested [<https://inner.example.com>](https://outer.example.com), " +
    "bare https://autolink.example.com and <ops@example.com>\n\n"
)

_TABLE = (
    "## Table link cases\n\n" +
    "| Case | Link | Value |\n" +
    "|---|---|---:|\n" +
    "| Named | [overview](https://app.datadoghq.com/dashboard/abc) | 42 |\n" +
    "| Formatted | [**bold** and `code`](https://example.com/formatted) | 7 |\n" +
    "| Empty | [](https://example.com/empty) | 0 |\n" +
    "| Nested URL | [<https://inner.example.com>](https://outer.example.com) | 1 |\n" +
    "| Nested email | [<ops@example.com>](https://outer.example.com) | 2 |\n" +
    "| Autolinks | <ops@example.com> and https://example.com | 3 |\n" +
    "| Escapes | [A & B \\\\*star](https://example.com/?a=1&lt=2) | 4 |\n" +
    "| Reference | [docs][ref] | 5 |\n" +
    "| Strikethrough | ~~before [docs](https://example.com) after~~ | 6 |\n\n" +
    "## Table wrapping\n\n" +
    "| Service | Documentation |\n" +
    "|---|---|\n" +
    "| web | [a long documentation title that should wrap within its table cell](https://example.com/web) |\n" +
    "| api | [another long title with **bold text** and `code` inside the link](https://example.com/api) |\n\n"
)

def links():
    rows = [
        "| service-%d | [dashboard %d](https://example.com/dashboard/%d) |" % (i, i, i)
        for i in range(1, 41)
    ]
    say(
        "# Links\n\n" + _INLINE + _LIST + _TABLE +
        "## Larger linked table\n\n| Service | Dashboard |\n|---|---|\n" +
        "\n".join(rows) + "\n\n" +
        "Reference-style: [docs][ref].\n\n" +
        "[ref]: https://docs.datadoghq.com/\n",
    )
