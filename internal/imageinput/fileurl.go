package imageinput

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// FileURL references a local image by absolute path until it is loaded.
// Windows paths use the standard forms file:///C:/a and file://host/share/a.
func FileURL(p string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(p)}
	switch vol := filepath.VolumeName(p); {
	case strings.HasPrefix(vol, `\\`):
		u.Host, u.Path, _ = strings.Cut(u.Path[2:], "/")
		u.Path = "/" + u.Path
	case vol != "":
		u.Path = "/" + u.Path
	}
	return u.String()
}

// FilePath decodes a FileURL; ok is false for any other URL.
func FilePath(s string) (p string, ok bool, err error) {
	if !strings.HasPrefix(s, "file:") {
		return "", false, nil
	}
	u, err := url.Parse(s)
	if err == nil && u.RawQuery == "" && u.Fragment == "" {
		p = filepath.FromSlash(u.Path)
		if u.Host != "" && u.Host != "localhost" {
			p = `\\` + u.Host + p
		} else if rest := strings.TrimPrefix(p, `\`); filepath.VolumeName(rest) != "" {
			p = rest
		}
	}
	if !filepath.IsAbs(p) {
		return "", true, fmt.Errorf("invalid image file URL %q", s)
	}
	return p, true, nil
}
