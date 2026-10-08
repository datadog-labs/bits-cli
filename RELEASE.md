# Releasing Bits

Releases are cut from `main` by the release workflow. Start one with:

```sh
./scripts/release.sh v1.2.3
```

This triggers `.github/workflows/release.yml` on `main`, which:

1. checks that the tag is the next patch, minor, or major version;
2. runs CI and builds the release archives with `./scripts/dist.sh`;
3. attests their build provenance and publishes them as a GitHub release of
   the `main` commit with `./scripts/publish.sh`, which also creates the tag
   and writes install and verification instructions into the release notes.

Tags are protected, so they are only created by the workflow, with a
dd-octo-sts token scoped by `.github/chainguard/self.release.sts.yaml`.

Release assets, for darwin/arm64, linux/amd64, and linux/arm64:

```
bits_<version>_<os>_<arch>.tar.gz   bits, README.md, LICENSE, LICENSE-3rdparty.csv
bits_<version>_<os>_<arch>.sbom.json  SPDX SBOM of the Go modules in bits
bits_<version>_checksums.txt        SHA-256 of every archive and SBOM
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
