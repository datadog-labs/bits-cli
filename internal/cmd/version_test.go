package cmd

import "testing"

func TestBuildVersionPrefixesReleaseVersion(t *testing.T) {
	previous := releaseVersion
	releaseVersion = "1.2.3"
	t.Cleanup(func() { releaseVersion = previous })

	if got, want := BuildVersion(), "v1.2.3"; got != want {
		t.Errorf("BuildVersion() = %q, want %q", got, want)
	}
}
