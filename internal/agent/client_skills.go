package agent

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/DataDog/bits-cli/internal/assistant"
	"gopkg.in/yaml.v3"
)

const (
	skillFileLimit    = 64 * 1024
	skillReadLimit    = 1024 * 1024
	skillCatalogLimit = 64 * 1024
	skillEntryLimit   = 4096
	skillDepthLimit   = 32
	skillRootLimit    = 128
)

// ClientSkills owns a metadata snapshot for a conversation. The engine serializes
// access and sends the same catalog on every request, including continuations.
type ClientSkills struct {
	workspace string
	extra     []string
	prepared  bool
	catalog   string
}

// NewClientSkills discovers skills relative to an explicit workspace. Extra
// directories are searched in flag order after project directories.
func NewClientSkills(workspace string, extra []string) *ClientSkills {
	return &ClientSkills{workspace: workspace, extra: slices.Clone(extra)}
}

// WithClientSkills attaches a catalog owned exclusively by this engine.
func WithClientSkills(skills *ClientSkills) Option {
	return func(e *Engine) { e.clientSkills = skills }
}

func (s *ClientSkills) reset() {
	if s != nil {
		s.prepared = false
		s.catalog = ""
	}
}

func (s *ClientSkills) apply(ctx context.Context, opts assistant.SendOptions) assistant.SendOptions {
	if s == nil {
		return opts
	}
	if !s.prepared {
		// New and resumed conversations both rescan. A resumed conversation may
		// carry an older catalog in its history, so an empty rescan sends an
		// explicit notice that supersedes it.
		catalog := s.snapshot(ctx)
		if ctx.Err() != nil {
			return opts
		}
		if catalog == "" && opts.ConversationID != "" {
			catalog = clientSkillsRemovalNotice
		}
		s.catalog = catalog
		s.prepared = true
	}
	if s.catalog != "" {
		if opts.CustomUserContext != "" {
			opts.CustomUserContext += "\n\n"
		}
		opts.CustomUserContext += s.catalog
	}
	return opts
}

const clientSkillsRemovalNotice = "<available-local-client-skills>\nThis is the latest available-local-client-skills update. Disregard any earlier <available-local-client-skills> blocks.\nNo local client skills are currently available in this user's local project context.\n</available-local-client-skills>"

type clientSkill struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	path        string
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func parseClientSkill(content []byte) (clientSkill, bool) {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 || strings.TrimSuffix(lines[0], "\r") != "---" {
		return clientSkill{}, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") != "---" {
			continue
		}
		var metadata yaml.Node
		if yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &metadata) != nil || len(metadata.Content) != 1 || metadata.Content[0].Kind != yaml.MappingNode {
			return clientSkill{}, false
		}
		// Require strings rather than YAML's implicit scalar conversions.
		mapping := metadata.Content[0].Content
		for j := 0; j < len(mapping); j += 2 {
			if mapping[j].Value == "name" || mapping[j].Value == "description" {
				if mapping[j+1].Tag != "!!str" {
					return clientSkill{}, false
				}
			}
		}
		var skill clientSkill
		if metadata.Decode(&skill) != nil {
			return clientSkill{}, false
		}
		skill.Description = strings.TrimSpace(skill.Description)
		return skill, len(skill.Name) <= 64 && skillNamePattern.MatchString(skill.Name) && len(skill.Description) > 0 && utf8.RuneCountInString(skill.Description) <= 1024
	}
	return clientSkill{}, false
}

type skillLocation struct{ path, boundary string }

func (s *ClientSkills) snapshot(ctx context.Context) string {
	active, err := filepath.Abs(s.workspace)
	if err != nil {
		return ""
	}
	active, err = filepath.EvalSymlinks(active)
	if err != nil {
		return ""
	}
	repository, _ := instructionRepository(ctx, active)
	boundary := active
	if repository != "" && withinInstructionsRoot(repository, active) {
		boundary = repository
	}
	// Nearest project directories win, followed by explicit roots in flag order,
	// then the default user root. Traversal is bounded across all roots together.
	var locations []skillLocation
	for dir := active; ; dir = filepath.Dir(dir) {
		locations = append(locations, skillLocation{filepath.Join(dir, ".agents", "skills"), boundary})
		if dir == boundary || filepath.Dir(dir) == dir || len(locations) >= skillRootLimit {
			break
		}
	}
	home, _ := os.UserHomeDir()
	for _, extra := range s.extra {
		if len(locations) >= skillRootLimit {
			break
		}
		if strings.HasPrefix(extra, "~/") && home != "" {
			extra = filepath.Join(home, extra[2:])
		}
		if extra == "" {
			continue
		}
		if !filepath.IsAbs(extra) {
			extra = filepath.Join(active, extra)
		}
		locations = append(locations, skillLocation{extra, extra})
	}
	if home != "" && len(locations) < skillRootLimit {
		root := filepath.Join(home, ".agents", "skills")
		locations = append(locations, skillLocation{root, root})
	}
	scan := skillScan{ctx: ctx, remainingEntries: skillEntryLimit, remainingBytes: skillReadLimit, skills: make(map[string]clientSkill)}
	for _, location := range locations {
		if ctx.Err() != nil || scan.remainingEntries <= 0 || scan.remainingBytes <= 0 {
			break
		}
		resolvedBoundary, resolveErr := filepath.EvalSymlinks(location.boundary)
		if resolveErr != nil {
			continue
		}
		root, openErr := os.OpenRoot(resolvedBoundary)
		if openErr != nil {
			continue
		}
		scan.walk(root, resolvedBoundary, location.path, 0, make(map[string]bool))
		_ = root.Close()
	}
	names := make([]string, 0, len(scan.skills))
	for name := range scan.skills {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	var catalog strings.Builder
	catalog.WriteString("<available-local-client-skills>\nThis is the latest available-local-client-skills update. Disregard any earlier <available-local-client-skills> blocks.\nAvailable local client skills in this user's local project context:\n")
	const footer = "Local client skills are different from enabled skills. They are local to this CLI. Do not load these local client skills with the Skill tool. When a skill is invoked, read its SKILL.md with available client tools before following it. Use read_file with a workspace-relative path for files inside the active directory; for other paths use exec_command subject to its normal permissions.\n</available-local-client-skills>"
	for _, name := range names {
		skill := scan.skills[name]
		entry := fmt.Sprintf("<skill name=\"%s\" path=\"%s\">%s</skill>\n", html.EscapeString(skill.Name), html.EscapeString(filepath.ToSlash(skill.path)), html.EscapeString(skill.Description))
		if catalog.Len()+len(entry)+len(footer) > skillCatalogLimit {
			break
		}
		catalog.WriteString(entry)
	}
	catalog.WriteString(footer)
	return catalog.String()
}

type skillScan struct {
	ctx                              context.Context
	remainingEntries, remainingBytes int
	skills                           map[string]clientSkill
}

func (s *skillScan) walk(root *os.Root, boundary, source string, depth int, seen map[string]bool) {
	if s.ctx.Err() != nil || depth > skillDepthLimit || s.remainingEntries <= 0 || s.remainingBytes <= 0 {
		return
	}
	s.remainingEntries--
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || !withinInstructionsRoot(boundary, resolved) || seen[resolved] {
		return
	}
	seen[resolved] = true
	relative, err := filepath.Rel(boundary, resolved)
	if err != nil {
		return
	}
	info, err := root.Stat(relative)
	if err != nil {
		return
	}
	if info.IsDir() {
		dir, openErr := root.Open(relative)
		if openErr != nil {
			return
		}
		// Read at most the remaining budget. Omit an oversized directory entirely
		// rather than choosing entries according to filesystem iteration order.
		entries, _ := dir.ReadDir(s.remainingEntries + 1)
		_ = dir.Close()
		if len(entries) > s.remainingEntries {
			s.remainingEntries = 0
			return
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			s.walk(root, boundary, filepath.Join(resolved, entry.Name()), depth+1, seen)
		}
		return
	}
	if filepath.Base(source) != "SKILL.md" || !info.Mode().IsRegular() {
		return
	}
	// Reserve the extra byte readInstructions uses to detect truncation.
	if s.remainingBytes <= 1 {
		s.remainingBytes = 0
		return
	}
	limit := min(skillFileLimit, s.remainingBytes-1)
	content, truncated, err := readInstructions(root, relative, limit)
	if truncated {
		s.remainingBytes -= limit + 1
	} else {
		s.remainingBytes -= len(content)
	}
	if err != nil || truncated || !utf8.Valid(content) {
		return
	}
	skill, ok := parseClientSkill(content)
	if !ok {
		return
	}
	if _, exists := s.skills[skill.Name]; !exists {
		skill.path = resolved
		s.skills[skill.Name] = skill
	}
}
