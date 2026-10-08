## Install

Download the archive for your platform, extract it, and put `bits` on your `PATH`:

```sh
{{downloads}}
tar -xzf bits_{{version}}_*.tar.gz bits
./bits --version
```

## Verify

```sh
gh release download {{tag}} --repo {{repo}} --pattern bits_{{version}}_checksums.txt
sha256sum --check --ignore-missing bits_{{version}}_checksums.txt
gh attestation verify bits_{{version}}_<os>_<arch>.tar.gz --repo {{repo}} \
    --signer-workflow {{repo}}/.github/workflows/release.yml --source-ref refs/heads/main
```

Each archive has an SPDX SBOM, `bits_{{version}}_<os>_<arch>.sbom.json`, listing the
Go modules compiled into the binary.
