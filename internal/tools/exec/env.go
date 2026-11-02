package exec

import (
	"runtime"
	"strings"
)

func envNameKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func shellEnvWithoutExcludedNames(env, names []string) []string {
	excluded := make(map[string]bool, len(names))
	for _, name := range names {
		excluded[envNameKey(name)] = true
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !excluded[envNameKey(name)] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
