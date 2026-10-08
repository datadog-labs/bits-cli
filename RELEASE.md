# Releasing Bits

Releases are cut from `main` by the release workflow. Start one with:

```sh
./scripts/release.sh v1.2.3
```

This triggers `.github/workflows/release.yml` on `main`, which:

1. checks that the tag is the next patch, minor, or major version;
2. runs CI and builds the release archives with `./scripts/dist.sh --sign`,
   which signs and notarizes the macOS binary with Datadog's Developer ID;
3. attests their build provenance and publishes them as a GitHub release of
   the `main` commit with `./scripts/publish.sh`, which also creates the tag.

Tags are protected, so they are only created by the workflow, with a
dd-octo-sts token scoped by `.github/chainguard/self.release.sts.yaml`.

Signing uses `./scripts/sign-macos.sh` (quill, so it runs on Linux) with
credentials stored as secrets of the `protected-main-env` environment:
`MACOS_SIGN_P12` and `MACOS_SIGN_PASSWORD` for the Developer ID Application
certificate, and `MACOS_NOTARY_KEY`, `MACOS_NOTARY_KEY_ID`, and
`MACOS_NOTARY_ISSUER_ID` for the App Store Connect API key.

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
codesign --verify --strict --verbose=2 bits   # macOS: Developer ID signature
```

`./scripts/dist.sh v1.2.3` produces the same archives locally in `dist/`,
with an unsigned macOS binary unless `--sign` and the credentials are given.
