# Releasing Bits

Releases are cut from `main` by the release workflow. Start one with:

```sh
./scripts/release.sh v1.2.3
```

This triggers `.github/workflows/release.yml` on `main`, which:

1. checks that the tag is the next patch, minor, or major version;
2. runs CI and builds the release archives with `./scripts/dist.sh`;
3. attests their build provenance and publishes them as a GitHub release of
   the `main` commit with `./scripts/publish.sh`, which also creates the tag.

Tags are protected, so they are only created by the workflow, with a
dd-octo-sts token scoped by `.github/chainguard/self.release.sts.yaml`.

Release assets, for darwin/arm64, linux/amd64, and linux/arm64:

```
bits_<version>_<os>_<arch>.tar.gz   bits, README.md, LICENSE, LICENSE-3rdparty.csv
bits_<version>_checksums.txt        SHA-256 of every archive
```

Verify a download with:

```sh
sha256sum --check --ignore-missing bits_<version>_checksums.txt
gh attestation verify bits_<version>_<os>_<arch>.tar.gz \
    --repo datadog-labs/bits-cli \
    --signer-workflow datadog-labs/bits-cli/.github/workflows/release.yml \
    --source-ref refs/heads/main
```

`./scripts/dist.sh v1.2.3` produces the same archives locally in `dist/`.

## Homebrew

The `bits` formula of the [datadog-labs/homebrew-pack](https://github.com/datadog-labs/homebrew-pack)
tap is rendered from `.github/homebrew/bits.rb.erb`. After a release, propose it to
the tap with:

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
