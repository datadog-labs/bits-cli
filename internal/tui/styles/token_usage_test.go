package styles

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var rawColorPattern = regexp.MustCompile(`#[[:xdigit:]]{3,8}`)

// Shared components and screens consume Theme roles; palette.go remains the
// single source of raw TUI colors.
func TestComponentsAndLoginDoNotDefineRawColors(t *testing.T) {
	for _, dir := range []string{"../components", "../login"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if rawColorPattern.Match(body) || strings.Contains(string(body), "lipgloss.Color(") {
				t.Errorf("%s defines a raw color; add a semantic token/style in internal/tui/styles instead", path)
			}
		}
	}
}
