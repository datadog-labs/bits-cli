package styles

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		if rawColorPattern.Match(body) {
			t.Errorf("%s defines a raw color; add a semantic token/style in internal/tui/styles instead", path)
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, body, 0)
		if err != nil {
			return err
		}
		if hasLiteralLipglossColor(file) {
			t.Errorf("%s defines a literal Lipgloss color; add a semantic token/style in internal/tui/styles instead", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasLiteralLipglossColor(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Color" {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "lipgloss" {
			return true
		}
		if _, ok := call.Args[0].(*ast.BasicLit); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func TestHasLiteralLipglossColor(t *testing.T) {
	for _, test := range []struct {
		name string
		src  string
		want bool
	}{
		{name: "literal", src: `package p; func f() { _ = lipgloss.Color("#123456") }`, want: true},
		{name: "dynamic", src: `package p; func f(color string) { _ = lipgloss.Color(color) }`},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "test.go", test.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := hasLiteralLipglossColor(file); got != test.want {
				t.Errorf("hasLiteralLipglossColor() = %t, want %t", got, test.want)
			}
		})
	}
}
