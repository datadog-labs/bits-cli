package agent

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DataDog/bits-cli/internal/assistant"
)

const (
	skillFileLimit    = 64 * 1024
	skillReadLimit    = 1024 * 1024
	skillCatalogLimit = 64 * 1024
	skillEntryLimit   = 4096
	skillDepthLimit   = 32
	skillRootLimit    = 128
)

// ClientSkills shares one conversation registry between completion and the
// engine. Discovery configuration is immutable; the cache owns synchronization.
type ClientSkills struct {
	workspace string
	extra     []string
	cache     skillRegistryCache
}

// NewClientSkills discovers skills relative to an explicit workspace. Extra
// directories are searched in flag order after project directories.
func NewClientSkills(workspace string, extra []string) *ClientSkills {
	return &ClientSkills{workspace: workspace, extra: slices.Clone(extra)}
}

// WithClientSkills attaches a client skill store owned exclusively by this engine.
func WithClientSkills(skills *ClientSkills) Option {
	return func(e *Engine) { e.clientSkills = skills }
}

func (s *ClientSkills) reset() {
	if s != nil {
		s.cache.reset()
	}
}

// apply adds the catalog, then an explicitly invoked skill's instructions.
func (s *ClientSkills) apply(ctx context.Context, opts assistant.SendOptions, invoked string) assistant.SendOptions {
	if s == nil {
		return opts
	}
	if registry, err := s.cache.load(ctx, s.discover); err == nil {
		catalog := registry.catalog
		if catalog == "" && opts.ConversationID != "" {
			catalog = clientSkillsRemovalNotice
		}
		opts.CustomUserContext = joinUserContext(opts.CustomUserContext, catalog)
	}
	opts.CustomUserContext = joinUserContext(opts.CustomUserContext, invoked)
	return opts
}

func joinUserContext(context, block string) string {
	if context == "" || block == "" {
		return context + block
	}
	return context + "\n\n" + block
}

// Keep context markers stable so updates supersede catalogs already in conversation history.
const clientSkillsRemovalNotice = "<available-local-client-skills>\nThis is the latest available-local-client-skills update. Disregard any earlier <available-local-client-skills> blocks.\nNo local client skills are currently available in this user's local project context.\n</available-local-client-skills>"

// SkillSummary is the presentation metadata exposed to completion clients.
type SkillSummary struct {
	Name        string
	Description string
}

// registeredSkill stays inside the engine with its confined-read metadata.
type registeredSkill struct {
	Name                   string
	Description            string
	Path                   string
	DisableModelInvocation bool
	boundary               string
	// defaultName applies when a reread document omits its name.
	defaultName string
}

// ClientSkills lists the shared conversation registry. An empty successful result
// means no skills; failures remain distinguishable and can be retried.
func (e *Engine) ClientSkills(ctx context.Context) ([]SkillSummary, error) {
	if e.clientSkills == nil {
		return nil, nil
	}
	registry, err := e.clientSkills.cache.load(ctx, e.clientSkills.discover)
	if err != nil {
		return nil, err
	}
	summaries := make([]SkillSummary, 0, len(registry.skills))
	for _, skill := range registry.skills {
		summaries = append(summaries, SkillSummary{Name: skill.Name, Description: skill.Description})
	}
	return summaries, nil
}

type skillLocation struct{ path, boundary string }

func (s *ClientSkills) discover(ctx context.Context) []registeredSkill {
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
	scan := skillScan{ctx: ctx, remainingEntries: skillEntryLimit, remainingBytes: skillReadLimit, skills: make(map[string]registeredSkill)}
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
	skills := make([]registeredSkill, 0, len(names))
	for _, name := range names {
		skills = append(skills, scan.skills[name])
	}
	return skills
}

func renderClientSkills(skills []registeredSkill) string {
	if !slices.ContainsFunc(skills, func(skill registeredSkill) bool { return !skill.DisableModelInvocation }) {
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
	skills                           map[string]registeredSkill
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
	defaultName := filepath.Base(filepath.Dir(source))
	document, consumed, err := readSkillDocument(root, relative, min(skillFileLimit, s.remainingBytes-1), defaultName)
	s.remainingBytes -= consumed
	if err != nil {
		return
	}
	if _, exists := s.skills[document.Name]; !exists {
		skill := registeredSkill{Name: document.Name, Description: document.Description, Path: resolved, DisableModelInvocation: document.DisableModelInvocation, boundary: boundary, defaultName: defaultName}
		s.skills[skill.Name] = skill
	}
}
