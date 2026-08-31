package tools

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/DataDog/bits-cli/internal/agent"
)

// New opens root as a confined workspace and returns the three read tools:
// read_file, list_files, and grep_files.
// All three share one approval key; a single allow-session decision covers all of them.
func New(root string) ([]agent.Tool, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open workspace %q: %w", root, err)
	}
	fsys := r.FS()
	return newTools(fsys, root), nil
}

func newTools(fsys fs.FS, root string) []agent.Tool {
	return []agent.Tool{
		newReadFileTool(fsys, root),
		newListFilesTool(fsys, root),
		newGrepFilesTool(fsys, root),
	}
}
