# Releasing Bits

Releases are cut from `main` by the release workflow. Start one with:

```sh
./scripts/release.sh v1.2.3
```

This triggers `.github/workflows/release.yml` on `main`, which:

1. checks that the tag is the next patch, minor, or major version;
2. runs CI for the tag, which builds and tests each OS on its own runner and
   packages the release archives, SBOMs, and [install scripts](#install-scripts)
   (see CONTRIBUTING.md);
3. attests the provenance of these assets and publishes them as a GitHub release of
   the `main` commit with `./scripts/publish.sh`, which also creates the tag;
4. opens a pull request that updates the [Homebrew](#homebrew) formula and merges
   once approved.

Tags are protected, so they are only created by the workflow, with a
dd-octo-sts token scoped by `.github/chainguard/self.release.sts.yaml`.

Release assets, for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64,
windows/amd64, and windows/arm64:

```
bits_<version>_<os>_<arch>.tar.gz            macOS/Linux install prefix
bits_<version>_windows_<arch>.zip           Windows install prefix
bits_<version>_<os>_<arch>.tar.gz.sbom.json  SPDX JSON SBOM of a macOS/Linux archive
bits_<version>_windows_<arch>.zip.sbom.json SPDX JSON SBOM of a Windows archive
install.sh                                  macOS/Linux installer for this release
install.ps1                                 Windows installer for this release
bits_<version>_checksums.txt                SHA-256 of every other asset
```

Each archive is an install prefix, which Homebrew and the install scripts
install as is:

```
bin/bits                  bits.exe on Windows
libexec/bits/<tool>/      libexec helpers built for the platform, if any
share/doc/bits/           README.md, LICENSE, LICENSE-3rdparty.csv
```

Verify a download with:

```sh
sha256sum --check --ignore-missing bits_<version>_checksums.txt
gh attestation verify bits_<version>_<os>_<arch>.tar.gz \
    --repo datadog-labs/bits-cli \
    --signer-workflow datadog-labs/bits-cli/.github/workflows/release.yml \
    --source-ref refs/heads/main
```

For Windows, verify `bits_<version>_windows_<arch>.zip` with the same
attestation command.

Build archives and checksums locally in `dist/` with either:

```sh
./scripts/dist.sh v1.2.3                        # build every platform, then package

VERSION=1.2.3 ./scripts/build.sh linux/amd64 …  # what CI runs on each OS
./scripts/package.sh v1.2.3                     # every platform's prefix in build/
```

`package.sh` refuses an executable that `go version -m` does not report as a
clean build of the current commit for its platform, so commit first.
`dist.sh` runs on macOS with the Xcode command line tools, which build the
macOS-only [ddviz](libexec/ddviz/README.md) helper. Add `--sbom` before the
tag to include SBOMs; this requires [Syft](https://github.com/anchore/syft).
Release platforms are listed in `scripts/platforms.sh`.

## Release identity

Only the publish job uses the existing `protected-main-env` environment; its
STS policy requires that identity and a manual release run on `main`.
Homebrew keeps its branch identity. The environment needs no approval gate
or settings change. Merge the workflow and policy changes together after
in-flight publish jobs finish; older publish jobs must be restarted as a
fresh release run on `main` to obtain a token.

## Install scripts

`scripts/install.sh` and `scripts/install.ps1` leave the release they install
unset; `package.sh` sets it in the copies it puts in `dist/`. Since the scripts
keep their names, `releases/latest/download/install.sh` always installs the
latest release ([INSTALL.md](INSTALL.md) has the user commands).

Each installation then updates itself with the code of the release it runs,
so released binaries and scripts depend on these, which must not change:

- the repository, `datadog-labs/bits-cli`, and `INSTALL.md` at its root,
  which bits links when it cannot update itself;
- the asset names above and the `sha256sum` format of the checksums;
- archives that hold `bin/`, `libexec/`, and `share/` at their root, and only
  regular files and directories: older binaries unpack nothing else;
- `releases/latest` redirecting to the tag page of the latest release;
- the layout under the installation root: `versions/v<version>/` with an
  `.install-complete` marker, and `current` pointing at one of them;
- `bits install`, with no arguments, run from `versions/v<version>/bin`,
  which switches to that version. A layout change goes in there.

`scripts/install_test.go` and `internal/update` test these.

## Homebrew

The `bits` formula of the [datadog-labs/homebrew-pack](https://github.com/datadog-labs/homebrew-pack)
tap is rendered from `.github/homebrew/bits.rb.erb`. After each release, the release
workflow's homebrew job opens the tap pull request "Update bits to v1.2.3" and
enables auto-merge, so it merges once a tap reviewer approves it. It runs
`./scripts/homebrew.rb --output DIR v1.2.3` to verify the release and render the
formula, then commits it as a signed API commit with a dd-octo-sts token scoped by
the tap's `.github/chainguard/bits-cli.release.sts.yaml`. Re-running the job
updates the same branch and pull request.

To propose a release by hand, for example when the job failed, run:

```sh
./scripts/homebrew.rb v1.2.3
```

The script verifies the release archives against the release checksums and
their build provenance attestations, commits the formula to a branch of the tap
(or of your fork of it), and opens a draft pull request. `--dry-run` verifies the
release and previews the formula diff, push destination, and PR title and body.
It does not commit, create a fork, push, or create or update a PR.
It needs Ruby and an authenticated `gh`.
Commits use your git signing configuration.
