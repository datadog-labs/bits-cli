# Releasing Bits

## Internal release flow

From the commit to release (normally `main`), run:

```sh
./scripts/release.sh v1.2.3
```

The tag must be a stable semantic version (`v<major>.<minor>.<patch>`) with
no leading zeroes in its components.

The matching GitLab tag pipeline builds the supported binaries. To publish
the internal Dogbrew archives, open that pipeline in GitLab and manually start
its `publish-to-dogbrew` job.
