package imageinput

import (
	"runtime"
	"testing"
)

func TestFileURLsRoundTripLocalPathsOnly(t *testing.T) {
	paths := []string{"/Users/me/My Shot #1?.png", "/tmp/100%.png"}
	if runtime.GOOS == "windows" {
		paths = []string{`C:\Users\me\My Shot #1?.png`, `C:\Temp\100%.png`}
	}
	for _, p := range paths {
		if got, ok, err := FilePath(FileURL(p)); !ok || err != nil || got != p {
			t.Fatalf("FilePath(FileURL(%q)) = %q, %v, %v", p, got, ok, err)
		}
	}
	if _, ok, err := FilePath("data:image/png;base64,AAAA"); ok || err != nil {
		t.Fatalf("a data URL is a file URL: %v, %v", ok, err)
	}
	path, ok, err := FilePath("file://host/shot.png")
	if runtime.GOOS == "windows" {
		if !ok || err != nil || path != `\\host\shot.png` {
			t.Fatalf("UNC file URL = %q, %v, %v", path, ok, err)
		}
	} else if !ok || err == nil {
		t.Fatalf("a remote file URL was accepted: %v, %v", ok, err)
	}
}
