# libexec helpers

Programs that bits runs and ships with itself, one directory per helper.
`scripts/build.sh` builds them into the install prefix next to `bin/bits`:

```
<prefix>/                 build/<os>-<arch>, a release archive, or the Homebrew keg
  bin/bits
  libexec/bits/<tool>/    built by libexec/<tool>/build.sh
```

Bits resolves a helper executable with `libexec.Binary("<tool>")` from
[`internal/libexec`](../internal/libexec), which resolves
`<prefix>/libexec/bits/<tool>/<tool>` from the bits executable, or
`$BITS_LIBEXEC_DIR/<tool>/<tool>` when set (`.exe` is appended on Windows).
Resolution does not check existence, permissions, or platform compatibility:
start the returned path with `exec.CommandContext` and handle its error.
`libexec.Dir("<tool>")` remains available for locating an existing asset directory.

| Helper | Platforms |
|---|---|
| [ddviz](ddviz/README.md) | macOS 13+ |

## Adding a helper

Create `libexec/<tool>/build.sh`. It is called as:

```sh
GOOS=darwin GOARCH=arm64 VERSION=1.2.3 BUILD_DIR=build libexec/<tool>/build.sh OUT_DIR
```

- It writes the helper, and any file it needs, into `OUT_DIR` only.
- `VERSION` is empty for development builds.
- Intermediate files go under `$BUILD_DIR/.cache/<tool>/`.
- For a platform the helper does not support, it exits 0 without writing anything.
- For a supported platform the host cannot build, it fails.
  `BUILD_LIBEXEC=0 scripts/build.sh` builds bits alone.

Run it standalone to stage a helper for tests:

```sh
libexec/<tool>/build.sh /tmp/libexec/<tool>
BITS_LIBEXEC_DIR=/tmp/libexec go test ./...
```
