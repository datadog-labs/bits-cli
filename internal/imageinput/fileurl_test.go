package imageinput

import "testing"

func TestFileURLsRoundTripLocalPathsOnly(t *testing.T) {
	for _, p := range []string{"/Users/me/My Shot #1?.png", "/tmp/100%.png"} {
		if got, ok, err := FilePath(FileURL(p)); !ok || err != nil || got != p {
			t.Fatalf("FilePath(FileURL(%q)) = %q, %v, %v", p, got, ok, err)
		}
	}
	if _, ok, err := FilePath("data:image/png;base64,AAAA"); ok || err != nil {
		t.Fatalf("a data URL is a file URL: %v, %v", ok, err)
	}
	if _, ok, err := FilePath("file://host/shot.png"); !ok || err == nil {
		t.Fatalf("a remote file URL was accepted: %v, %v", ok, err)
	}
}
