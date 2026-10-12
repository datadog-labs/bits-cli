// Package env discovers and renders local instructions, skills, and
// environment information. It has no knowledge of backend requests or delivery.
// The agent owns delivery acknowledgement and conversation invalidation; skill
// metadata is cached here while explicitly invoked bodies are reread on demand.
package env

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// localEnvironment is the model-facing description of the local machine. Its
// XML encoding is the exact text sent to the assistant.
type localEnvironment struct {
	XMLName   xml.Name  `xml:"local_environment"`
	Workspace string    `xml:"workspace"`
	Git       *localGit `xml:"git"`
	Shell     string    `xml:"shell,omitempty"`
	Platform  string    `xml:"platform"`
}

type localGit struct {
	Root   string `xml:"root"`
	Branch string `xml:"branch,omitempty"`
}

// UserContext returns a renderer for the local environment, or nil without
// a workspace. Each call observes current Git state without retaining delivery state.
func UserContext(ws *workspace.Workspace) func(context.Context) string {
	if ws == nil {
		return nil
	}
	return func(ctx context.Context) string {
		return renderLocalEnvironment(ctx, ws)
	}
}

func renderLocalEnvironment(ctx context.Context, ws *workspace.Workspace) string {
	env := localEnvironment{
		Workspace: ws.Path(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if shell := ws.DefaultShellPath(); shell != "" {
		env.Shell = filepath.Base(shell)
	}
	if repository := ws.Repository(ctx); repository.State == workspace.RepositoryPresent {
		env.Git = &localGit{Root: repository.Root, Branch: repository.Branch}
	}
	out, err := xml.MarshalIndent(env, "", "  ")
	if err != nil {
		return ""
	}
	return string(out)
}

func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// readBoundedFile reads a regular file through root, including at most one byte
// beyond limit so callers can choose whether to reject or truncate large files.
func readBoundedFile(root *os.Root, path string, limit int) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	return io.ReadAll(io.LimitReader(file, int64(limit)+1))
}
