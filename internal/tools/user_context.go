package tools

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"runtime"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/workspace"
)

// localEnvironment is the model-facing description of the local machine. Its
// XML encoding is the exact text sent to the assistant.
type localEnvironment struct {
	XMLName     xml.Name         `xml:"local_environment"`
	Workspace   string           `xml:"workspace"`
	Git         *localGit        `xml:"git"`
	Shell       string           `xml:"shell,omitempty"`
	Platform    string           `xml:"platform"`
	Permissions localPermissions `xml:"permissions"`
}

type localGit struct {
	Root   string `xml:"root"`
	Branch string `xml:"branch,omitempty"`
}

type localPermissions struct {
	Mode agent.PermissionsMode `xml:"mode,attr"`
}

// UserContext returns the agent.TurnInput.UserContext describing the local
// environment the client tools run in, or nil without a workspace.
func UserContext(ws *workspace.Workspace, set *agent.ToolSet) func(context.Context) string {
	if ws == nil {
		return nil
	}
	return func(ctx context.Context) string {
		return renderLocalEnvironment(ctx, ws, set.PermissionsMode())
	}
}

func renderLocalEnvironment(ctx context.Context, ws *workspace.Workspace, mode agent.PermissionsMode) string {
	env := localEnvironment{
		Workspace:   ws.Path(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Permissions: localPermissions{Mode: mode},
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
