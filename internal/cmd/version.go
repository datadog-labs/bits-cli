package cmd

import "runtime/debug"

// releaseVersion is set by the release build with -ldflags. Keeping it empty
// for ordinary builds preserves the development version reported by the Go
// toolchain.
var releaseVersion string

// BuildVersion reports the module version when available, otherwise a
// development version with the short VCS revision recorded by the toolchain.
func BuildVersion() string {
	if releaseVersion != "" {
		// Release artifacts store their package version without a prefix, while
		// the CLI presents versions in the same form as Go module versions.
		return "v" + releaseVersion
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if version := info.Main.Version; version != "" && version != "(devel)" {
		return version
	}
	revision := vcsRevision(info)
	if revision == "" {
		return "dev"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	return "dev-" + revision
}

func vcsRevision(info *debug.BuildInfo) string {
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
