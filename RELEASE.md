# Releasing Bits

Releases are cut from `main` by the release workflow. Start one with:

```sh
./scripts/release.sh v1.2.3
```

This triggers `.github/workflows/release.yml` on `main`, which:

1. checks that the tag is the next patch, minor, or major version;
2. runs CI for the tag, which builds and tests each OS on its own runner and
   packages the release archives and SBOMs (see CONTRIBUTING.md);
3. attests the provenance of these assets and publishes them as a GitHub release of
   the `main` commit with `./scripts/publish.sh`, which also creates the tag. It
   uploads them to a draft and publishes it only if the draft's asset digests
   match the attested files, since anyone with write access can edit a draft,
   then marks it latest once the published release passes the same check;
4. opens a pull request that updates the [Homebrew](#homebrew) formula and merges
   once approved.

Keep immutable releases enabled in the repository settings before publishing.
If the setting is off, publication can create a mutable release and tag that
the workflow will refuse to promote.

If publication succeeds but a later lookup or promotion fails, rerun the failed
publish job in the same workflow run. It resumes promotion only when the existing
release is immutable and its tag commit and asset digests match that run. A fresh
workflow dispatch still refuses an existing tag.

Tags are protected, so they are only created by the workflow, with a
dd-octo-sts token scoped by `.github/chainguard/self.release.sts.yaml`.

Release assets, for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64,
windows/amd64, and windows/arm64:

```
bits_<version>_<os>_<arch>.tar.gz            macOS/Linux install prefix
bits_<version>_windows_<arch>.zip           Windows install prefix
bits_<version>_<os>_<arch>.tar.gz.sbom.json  SPDX JSON SBOM of a macOS/Linux archive
bits_<version>_windows_<arch>.zip.sbom.json SPDX JSON SBOM of a Windows archive
bits_<version>_checksums.txt                SHA-256 of every archive and SBOM
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

Users run `scripts/install.sh` and `scripts/install.ps1` from the default
branch ([INSTALL.md](INSTALL.md) has the commands), and they install the latest
release. A change to them reaches users when it merges, so keep them working
with the latest release.

Standalone updates fetch the complete current script before executing it.
The scripts own archive selection, checksum verification, activation, and PATH
setup. The binary only checks the latest release and invokes the installer.
Installer changes reach existing installations when merged, without waiting for
them to upgrade. Review them as executable update code: this path trusts the
default branch over HTTPS in addition to the release workflow.

Released binaries depend on these compatibility contracts:

- the repository, `datadog-labs/bits-cli`, `INSTALL.md` at its root, and the
  scripts' paths on the default branch;
- `releases/latest` redirecting to the tag page of the latest stable release;
- the Unix installer accepting `--root PATH --version vX.Y.Z --update-from vA.B.C`,
  and the PowerShell installer accepting `-Root`, `-Version`, and `-UpdateFrom`;
- these explicit options taking precedence over the environment, and a stale
  updater leaving a changed active selection alone;
- installation discovery through `versions/v<version>/bin/bits` (`bits.exe` on
  Windows), the `.install-complete` marker, and the `current` link or junction.

Keep the latest scripts compatible with supported existing layouts and releases
from v0.0.15. Archive formats and names can evolve in the scripts without changing
old updaters. A layout migration must preserve discovery for the binaries it
continues to support. There is no `bits install` activation protocol.

Both scripts stage on the installation filesystem and switch `current`
atomically. Each invocation cleans up its own staging directory and leaves
previously installed releases available to running sessions. Installers hold a
process-owned lock per root from the active-version check through any archive
download and activation. A completed release is reused without downloading
its archive again. Keep the lock file in place so installers lock the same file.

`scripts/install_test.go`, its platform-specific tests, and `internal/update`
test these contracts using real installers and local release fixtures.

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
