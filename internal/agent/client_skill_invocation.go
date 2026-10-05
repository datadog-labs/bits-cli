package agent

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"unicode/utf8"
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
			loaded, err := skill.load(ctx)
			if err != nil {
				return "", err
			}
			return renderSkillInvocation(loaded, invocation.Arguments), nil
		}
	}
	return "", fmt.Errorf("unknown client skill: %s", invocation.Name)
}

// load rereads a registered file through its discovery boundary.
func (s registeredSkill) load(ctx context.Context) (loadedSkill, error) {
	if err := ctx.Err(); err != nil {
		return loadedSkill{}, err
	}
	if s.boundary == "" || !withinInstructionsRoot(s.boundary, s.Path) {
		return loadedSkill{}, fmt.Errorf("client skill %q has no registered read boundary", s.Name)
	}
	root, err := os.OpenRoot(s.boundary)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("open client skill %q root: %w", s.Name, err)
	}
	defer func() { _ = root.Close() }()
	boundaryInfo, err := root.Stat(".")
	if err != nil {
		return loadedSkill{}, fmt.Errorf("stat client skill %q root: %w", s.Name, err)
	}
	if s.boundaryInfo == nil || !os.SameFile(s.boundaryInfo, boundaryInfo) {
		return loadedSkill{}, fmt.Errorf("client skill %q discovery root changed; start a new conversation to rescan", s.Name)
	}
	relative, err := filepath.Rel(s.boundary, s.Path)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("resolve client skill %q: %w", s.Name, err)
	}
	info, err := root.Stat(relative)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("stat client skill %q: %w", s.Name, err)
	}
	if !info.Mode().IsRegular() {
		return loadedSkill{}, fmt.Errorf("client skill %q is not a regular file", s.Name)
	}
	content, truncated, err := readInstructions(root, relative, skillFileLimit)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("read client skill %q: %w", s.Name, err)
	}
	if truncated || !utf8.Valid(content) {
		return loadedSkill{}, fmt.Errorf("client skill %q is oversized or is not valid UTF-8", s.Name)
	}
	document, valid := parseSkillDocument(content, filepath.Base(filepath.Dir(s.Path)))
	if !valid || document.Name != s.Name {
		return loadedSkill{}, fmt.Errorf("client skill %q metadata changed or is invalid; start a new conversation to rescan", s.Name)
	}
	if err := ctx.Err(); err != nil {
		return loadedSkill{}, err
	}
	return loadedSkill{Name: s.Name, Path: s.Path, BaseDirectory: filepath.Dir(s.Path), Body: document.body}, nil
}

func renderSkillInvocation(s loadedSkill, arguments string) string {
	return fmt.Sprintf("<invoked-local-client-skill name=\"%s\" path=\"%s\" base-directory=\"%s\">\nThe user explicitly invoked this client skill. Follow these instructions, resolving relative references from its base directory.\n<instructions>%s</instructions>\n<arguments>%s</arguments>\n</invoked-local-client-skill>", html.EscapeString(s.Name), html.EscapeString(filepath.ToSlash(s.Path)), html.EscapeString(filepath.ToSlash(s.BaseDirectory)), html.EscapeString(s.Body), html.EscapeString(arguments))
}
