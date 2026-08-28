package styles

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var rawColorPattern = regexp.MustCompile(`#[[:xdigit:]]{3,8}`)

// TUI components and screens consume Theme roles; this package remains the
// single source of raw TUI colors.
func TestTUIDoesNotDefineRawColorsOutsideStyles(t *testing.T) {
	centralStylesDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			absolutePath, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if absolutePath == centralStylesDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rawColorPattern.Match(body) || strings.Contains(string(body), "lipgloss.Color(") {
			t.Errorf("%s defines a raw color; add a semantic token/style in internal/tui/styles instead", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
