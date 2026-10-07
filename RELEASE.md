# Releasing Bits

## Release flow

From the commit to release (normally `main`), run:

```sh
./scripts/release.sh v1.2.3
```

The tag must be a stable semantic version (`v<major>.<minor>.<patch>`) with
no leading zeroes in its components.

Pushing a **stable tag** triggers the GitHub Actions release flow
(`.github/workflows/ci.yml`): the tag is linted and tested, all six supported
targets are cross-built, and the three supported platform archives
(`bits-darwin-arm64`, `bits-linux-amd64`, `bits-linux-arm64`) are packaged as
workflow artifacts under the name `release-archives-v<version>`.

Each archive contains the platform binary and a `cli.yaml` manifest with the
release version and the minimal metadata (name, team, description, version)
needed to register the CLI with a package index.

The release archives (and every cross-built binary) are uploaded to GitHub
Actions workflow artifacts; nothing leaves GitHub Actions. No publication
or upload to any package index happens automatically from this repository:
the workflow has no secrets and no write permissions, and the repository
contains no publisher. Making the release archives available through any
package index is a separate manual step performed outside this repository.

Non-stable tags (for example `v1.2.3-rc1`) never produce release artifacts;
they get ordinary development builds only.
