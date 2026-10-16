package version

import "testing"

func TestBuildPrefixesReleaseVersion(t *testing.T) {
	previous := releaseVersion
	releaseVersion = "1.2.3"
	t.Cleanup(func() { releaseVersion = previous })

	if got, want := Build(), "v1.2.3"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

func TestStableReleasePolicy(t *testing.T) {
	for _, tc := range []struct {
		version string
		stable  bool
	}{
		{"v1.2.3", true},
		{"dev-abcdef", false},
		{"v1.2.3-rc.1", false},
		{"v1.2.3+local", false},
		{"v1.2", false},
	} {
		if got := IsStable(tc.version); got != tc.stable {
			t.Errorf("IsStable(%q) = %v, want %v", tc.version, got, tc.stable)
		}
	}
}
