package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/DataDog/bits-cli/internal/workspace"
)

func TestRenderLocalEnvironment(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-b", "feature/<x>").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	shell := ""
	if path := ws.DefaultShellPath(); path != "" {
		shell = "\n  <shell>" + filepath.Base(path) + "</shell>"
	}
	want := `<local_environment>
  <workspace>` + dir + `</workspace>
  <git>
    <root>` + dir + `</root>
    <branch>feature/&lt;x&gt;</branch>
  </git>` + shell + `
  <platform>` + runtime.GOOS + "/" + runtime.GOARCH + `</platform>
</local_environment>`
	if got := renderLocalEnvironment(context.Background(), ws); got != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", got, want)
	}
}
