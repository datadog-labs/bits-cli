# Releasing Bits

## Internal release flow

From the commit to release (normally `main`), run:

```sh
./scripts/release.sh v1.2.3
```

The tag must be a stable semantic version (`v<major>.<minor>.<patch>`) with
no leading zeroes in its components.

Pushing a **stable tag** can start two pipelines, but neither is automatic
by tag-push alone:

- **GitHub Actions** (`.github/workflows/ci.yml`) lints and tests the tag,
  builds all supported targets, and packages the three Dogbrew platform
  tarballs (`bits-darwin-arm64`, `bits-linux-amd64`, `bits-linux-arm64`) as
  workflow artifacts. Nothing is published from Actions: the workflow has
  no secrets and no write permissions, so nothing fork-visible can reach
  the publisher. Non-stable tags (for example `v1.2.3-rc1`) get ordinary
  development builds instead.
- **GitLab** (`.gitlab-ci.yml`) keeps the internal stable-tag pipeline.
  Whether it runs at all depends on the Labs CodeSync approval propagating
  the tag to the internal repository, which is not yet proven; do not
  assume a successful GitLab pipeline just from pushing the tag. Once the
  pipeline is there, open it in GitLab and manually start its
  `publish-to-dogbrew` job to publish the internal Dogbrew archives. This
  manual gate is unchanged and remains the only path to Dogbrew.
