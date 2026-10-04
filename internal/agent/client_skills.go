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
// access and sends the same catalog with each top-level user message.
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
	// DisableModelInvocation keeps the skill out of the catalog. The skill still
	// claims its name, so it shadows lower-precedence skills with the same name.
	DisableModelInvocation bool  `yaml:"disable-model-invocation"`
	ModelInvocable         *bool `yaml:"model-invocable"`
	path                   string
	boundary               string
	boundaryInfo           os.FileInfo
}

// Colons separate namespaces; each component follows the existing name rules.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+([:-][a-z0-9]+)*$`)

func parseClientSkill(content []byte, defaultName string) (clientSkill, bool) {
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
			if mapping[j].Value == "model-invocable" || mapping[j].Value == "disable-model-invocation" {
				if mapping[j+1].Tag != "!!bool" {
					return clientSkill{}, false
				}
			}
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
		if skill.ModelInvocable != nil && !*skill.ModelInvocable {
			skill.DisableModelInvocation = true
		}
		if strings.TrimSpace(skill.Name) == "" {
			skill.Name = defaultName
		}
		skill.Description = strings.Join(strings.Fields(skill.Description), " ")
		return skill, len(skill.Name) <= 64 && skillNamePattern.MatchString(skill.Name) && len(skill.Description) > 0
	}
	return clientSkill{}, false
}

// LocalSkill is metadata for an explicitly invokable local skill. User-only
// skills remain available here even though they are omitted from the catalog.
type LocalSkill struct {
	Name                   string
	Description            string
	Path                   string
	DisableModelInvocation bool
	boundary               string
	boundaryInfo           os.FileInfo
}

// DiscoverLocalSkills scans immutable configuration without touching the
// conversation snapshot, so completion can run alongside an engine operation.
func (e *Engine) DiscoverLocalSkills(ctx context.Context) []LocalSkill {
	if e.clientSkills == nil {
		return nil
	}
	return e.clientSkills.discover(ctx)
}

type skillLocation struct{ path, boundary string }

func (s *ClientSkills) discover(ctx context.Context) []LocalSkill {
	active, err := filepath.Abs(s.workspace)
	if err != nil {
		return nil
	}
	active, err = filepath.EvalSymlinks(active)
	if err != nil {
		return nil
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
	skills := make([]LocalSkill, 0, len(names))
	for _, name := range names {
		skill := scan.skills[name]
		skills = append(skills, LocalSkill{Name: skill.Name, Description: skill.Description, Path: skill.path, DisableModelInvocation: skill.DisableModelInvocation, boundary: skill.boundary, boundaryInfo: skill.boundaryInfo})
	}
	return skills
}

func (s *ClientSkills) snapshot(ctx context.Context) string {
	skills := s.discover(ctx)
	if !slices.ContainsFunc(skills, func(skill LocalSkill) bool { return !skill.DisableModelInvocation }) {
		return ""
	}
	var catalog strings.Builder
	catalog.WriteString("<available-local-client-skills>\nThis is the latest available-local-client-skills update. Disregard any earlier <available-local-client-skills> blocks.\nAvailable local client skills in this user's local project context:\n")
	const footer = "Local client skills are different from enabled skills. They are local to this CLI. Do not load these local client skills with the Skill tool. When a skill is invoked, read its SKILL.md with available client tools before following it.\n</available-local-client-skills>"
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		entry := fmt.Sprintf("<skill name=\"%s\" path=\"%s\">%s</skill>\n", html.EscapeString(skill.Name), html.EscapeString(filepath.ToSlash(skill.Path)), html.EscapeString(skill.Description))
		if catalog.Len()+len(entry)+len(footer) > skillCatalogLimit {
			continue
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
	if err != nil || !withinInstructionsRoot(boundary, resolved) {
		return
	}
	relative, err := filepath.Rel(boundary, resolved)
	if err != nil {
		return
	}
	info, err := root.Stat(relative)
	if err != nil {
		return
	}
	if info.IsDir() {
		if seen[resolved] {
			return
		}
		seen[resolved] = true
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
	skill, ok := parseClientSkill(content, filepath.Base(filepath.Dir(source)))
	if !ok {
		return
	}
	if _, exists := s.skills[skill.Name]; !exists {
		skill.path = resolved
		skill.boundary = boundary
		skill.boundaryInfo, err = root.Stat(".")
		if err != nil {
			return
		}
		s.skills[skill.Name] = skill
	}
}

// invocationContext rereads the registered file through its discovery boundary.
// Bodies are loaded only for explicit user turns, never for menu discovery.
func (s LocalSkill) invocationContext(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.boundary == "" || !withinInstructionsRoot(s.boundary, s.Path) {
		return "", fmt.Errorf("local skill %q has no registered read boundary", s.Name)
	}
	root, err := os.OpenRoot(s.boundary)
	if err != nil {
		return "", fmt.Errorf("open local skill %q root: %w", s.Name, err)
	}
	defer func() { _ = root.Close() }()
	boundaryInfo, err := root.Stat(".")
	if err != nil {
		return "", fmt.Errorf("stat local skill %q root: %w", s.Name, err)
	}
	if s.boundaryInfo == nil || !os.SameFile(s.boundaryInfo, boundaryInfo) {
		return "", fmt.Errorf("local skill %q discovery root changed; start a new conversation to rescan", s.Name)
	}
	relative, err := filepath.Rel(s.boundary, s.Path)
	if err != nil {
		return "", fmt.Errorf("resolve local skill %q: %w", s.Name, err)
	}
	info, err := root.Stat(relative)
	if err != nil {
		return "", fmt.Errorf("stat local skill %q: %w", s.Name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local skill %q is not a regular file", s.Name)
	}
	content, truncated, err := readInstructions(root, relative, skillFileLimit)
	if err != nil {
		return "", fmt.Errorf("read local skill %q: %w", s.Name, err)
	}
	if truncated || !utf8.Valid(content) {
		return "", fmt.Errorf("local skill %q is oversized or is not valid UTF-8", s.Name)
	}
	metadata, valid := parseClientSkill(content, filepath.Base(filepath.Dir(s.Path)))
	if !valid || metadata.Name != s.Name {
		return "", fmt.Errorf("local skill %q metadata changed or is invalid; start a new conversation to rescan", s.Name)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Validation above guarantees a frontmatter delimiter. Preserve the body
	// exactly, including line endings and trailing whitespace.
	text := string(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	offset := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		offset += len(line)
		if i > 0 && strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") == "---" {
			break
		}
	}
	return fmt.Sprintf("<invoked-local-client-skill name=\"%s\" path=\"%s\" base-directory=\"%s\">\nThe user explicitly invoked this local skill. Follow these instructions, resolving relative references from its base directory.\n<instructions>%s</instructions>\n<arguments>%s</arguments>\n</invoked-local-client-skill>", html.EscapeString(s.Name), html.EscapeString(filepath.ToSlash(s.Path)), html.EscapeString(filepath.ToSlash(filepath.Dir(s.Path))), html.EscapeString(text[offset:]), html.EscapeString(arguments)), nil
}
