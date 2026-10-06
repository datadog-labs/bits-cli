package agent

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
)

// SkillInvocation is a user request. The engine resolves its name against the
// current conversation registry, independently of menu loading.
type SkillInvocation struct {
	Name      string
	Arguments string
}

type loadedSkill struct {
	Name, Path, BaseDirectory, Body string
}

func (e *Engine) prepareSkill(ctx context.Context, invocation SkillInvocation) (string, error) {
	if e.clientSkills == nil {
		return "", fmt.Errorf("unknown client skill: %s", invocation.Name)
	}
	registry, err := e.clientSkills.cache.load(ctx, e.clientSkills.discover)
	if err != nil {
		return "", fmt.Errorf("discover client skills: %w", err)
	}
	for _, skill := range registry.skills {
		if skill.Name == invocation.Name {
			loaded, err := skill.load()
			if err != nil {
				return "", err
			}
			return renderSkillInvocation(loaded, invocation.Arguments), nil
		}
	}
	return "", fmt.Errorf("unknown client skill: %s", invocation.Name)
}

// load rereads a registered file through its discovery boundary.
func (s registeredSkill) load() (loadedSkill, error) {
	root, err := os.OpenRoot(s.boundary)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("open client skill %q root: %w", s.Name, err)
	}
	defer func() { _ = root.Close() }()
	relative, err := filepath.Rel(s.boundary, s.Path)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("resolve client skill %q: %w", s.Name, err)
	}
	document, _, err := readSkillDocument(root, relative, skillFileLimit, s.defaultName)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("read client skill %q: %w", s.Name, err)
	}
	if document.Name != s.Name {
		return loadedSkill{}, fmt.Errorf("client skill %q was renamed; start a new conversation to rescan", s.Name)
	}
	return loadedSkill{Name: s.Name, Path: s.Path, BaseDirectory: filepath.Dir(s.Path), Body: document.body}, nil
}

func renderSkillInvocation(s loadedSkill, arguments string) string {
	return fmt.Sprintf("<invoked-local-client-skill name=\"%s\" path=\"%s\" base-directory=\"%s\">\nThe user explicitly invoked this client skill. Follow these instructions, resolving relative references from its base directory.\n<instructions>%s</instructions>\n<arguments>%s</arguments>\n</invoked-local-client-skill>", html.EscapeString(s.Name), html.EscapeString(filepath.ToSlash(s.Path)), html.EscapeString(filepath.ToSlash(s.BaseDirectory)), html.EscapeString(s.Body), html.EscapeString(arguments))
}
