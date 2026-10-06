# Releasing Bits

## Internal release flow

From the commit to release (normally `main`), run:

```sh
./scripts/release.sh v1.2.3
```

The tag must be a stable semantic version (`v<major>.<minor>.<patch>`) with
no leading zeroes in its components.

Pushing a **stable tag** engages both pipelines, but with different
automation: GitHub Actions runs automatically on the tag push (when
Actions is enabled), the GitLab pipeline only runs once approved CodeSync
propagation has carried the tag to the internal repository, and Dogbrew
publishing is always a separate manual step:

- **GitHub Actions** (`.github/workflows/ci.yml`) lints and tests the tag,
  builds all supported targets, and packages the three Dogbrew platform
  tarballs (`bits-darwin-arm64`, `bits-linux-amd64`, `bits-linux-arm64`) as
  workflow artifacts. Nothing is published from Actions: the workflow has
  no secrets and no write permissions, so nothing fork-visible can reach
  the publisher. Non-stable tags (for example `v1.2.3-rc1`) get ordinary
  development builds instead.
- **GitLab** (`.gitlab-ci.yml`) keeps the internal stable-tag pipeline.
  Do not assume it ran: it exists only after CodeSync propagation has
  carried the tag to the internal repository. Once the pipeline is there,
  open it in GitLab and manually start its `publish-to-dogbrew` job to
  publish the internal Dogbrew archives. This manual gate is unchanged
  and remains the only path to Dogbrew.
