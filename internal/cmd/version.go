package cmd

import "runtime/debug"

// buildVersion reports the module version when available, otherwise a
// development version with the short VCS revision recorded by the toolchain.
func buildVersion() string {
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
