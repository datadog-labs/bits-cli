package imageinput

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// FileURL references a local image by absolute path until it is loaded.
func FileURL(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// FilePath decodes a FileURL; ok is false for any other URL.
func FilePath(s string) (p string, ok bool, err error) {
	if !strings.HasPrefix(s, "file:") {
		return "", false, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Host != "" && u.Host != "localhost") || !path.IsAbs(u.Path) || u.RawQuery != "" || u.Fragment != "" {
		return "", true, fmt.Errorf("invalid image file URL %q", s)
	}
	return u.Path, true, nil
}
