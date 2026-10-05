package agent

import (
	"bytes"
	"regexp"
	"strings"

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
