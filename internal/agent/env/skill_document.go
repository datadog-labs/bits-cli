package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type skillMetadata struct {
	Name                   string
	Description            string
	DisableModelInvocation bool
}

// skillDocument keeps the body exactly as read, including its line endings.
// Discovery retains only metadata; invocation rereads the current document.
type skillDocument struct {
	skillMetadata
	body string
}

// Colons separate namespaces; each component follows the existing name rules.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+([:-][a-z0-9]+)*$`)

func parseSkillDocument(content []byte, defaultName string) (skillDocument, bool) {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 || strings.TrimSuffix(lines[0], "\r") != "---" {
		return skillDocument{}, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") != "---" {
			continue
		}
		var metadata yaml.Node
		if yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &metadata) != nil || len(metadata.Content) != 1 || metadata.Content[0].Kind != yaml.MappingNode {
			return skillDocument{}, false
		}
		// Require strings rather than YAML's implicit scalar conversions.
		mapping := metadata.Content[0].Content
		for j := 0; j < len(mapping); j += 2 {
			if mapping[j].Value == "model-invocable" || mapping[j].Value == "disable-model-invocation" {
				if mapping[j+1].Tag != "!!bool" {
					return skillDocument{}, false
				}
			}
			if mapping[j].Value == "name" || mapping[j].Value == "description" {
				if mapping[j+1].Tag != "!!str" {
					return skillDocument{}, false
				}
			}
		}
		var raw struct {
			Name                   string `yaml:"name"`
			Description            string `yaml:"description"`
			DisableModelInvocation bool   `yaml:"disable-model-invocation"`
			ModelInvocable         *bool  `yaml:"model-invocable"`
		}
		if metadata.Decode(&raw) != nil {
			return skillDocument{}, false
		}
		skill := skillMetadata{Name: raw.Name, Description: raw.Description, DisableModelInvocation: raw.DisableModelInvocation || (raw.ModelInvocable != nil && !*raw.ModelInvocable)}
		if strings.TrimSpace(skill.Name) == "" {
			skill.Name = defaultName
		}
		skill.Description = strings.Join(strings.Fields(skill.Description), " ")
		return skillDocument{skillMetadata: skill, body: strings.Join(lines[i+1:], "\n")}, len(skill.Name) <= 64 && skillNamePattern.MatchString(skill.Name) && len(skill.Description) > 0
	}
	return skillDocument{}, false
}

var errInvalidSkillDocument = errors.New("oversized, not valid UTF-8, or has invalid metadata")

// readSkillDocument reads one SKILL.md. consumed is the byte count charged
// against a discovery budget, including the truncation probe.
func readSkillDocument(path string, limit int, defaultName string) (document skillDocument, consumed int, err error) {
	content, err := readBoundedFile(path, limit)
	consumed = len(content)
	if err != nil {
		return skillDocument{}, consumed, err
	}
	if consumed > limit || !utf8.Valid(content) {
		return skillDocument{}, consumed, errInvalidSkillDocument
	}
	document, ok := parseSkillDocument(content, defaultName)
	if !ok {
		return skillDocument{}, consumed, errInvalidSkillDocument
	}
	return document, consumed, nil
}

type loadedSkill struct {
	Name, Path, BaseDirectory, Body string
}

// Invoke resolves a name against the cached registry and rereads its current body
// before rendering the invocation.
func (s *Skills) Invoke(ctx context.Context, name, arguments string) (string, error) {
	registry, err := s.cache.load(ctx, s.discover)
	if err != nil {
		return "", fmt.Errorf("discover client skills: %w", err)
	}
	for _, skill := range registry.skills {
		if skill.Name == name {
			loaded, err := skill.load()
			if err != nil {
				return "", err
			}
			return renderSkillInvocation(loaded, arguments), nil
		}
	}
	return "", fmt.Errorf("unknown client skill: %s", name)
}

// load rereads a registered file.
func (s registeredSkill) load() (loadedSkill, error) {
	document, _, err := readSkillDocument(s.Path, skillFileLimit, s.defaultName)
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
