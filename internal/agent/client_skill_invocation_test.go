package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
)

func TestClientSkillInvocationLoadsCurrentBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills/review", "review", "Review")
	provider := env.NewSkills(root, nil)
	summaries, err := provider.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	skill := summaries[0]
	writeSkillFile(t, root, ".agents/skills/review", "\xef\xbb\xbf---\r\nname: review\r\ndescription: Updated\r\n---\r\nCurrent <instructions> & body\r\n\t ")
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	engine := New(backend, Config{ClientSkills: provider})
	arguments := "  Preserve CASE\n\t and trailing space "
	for range 2 {
		drain(engine.StartTurn(context.Background(), TurnInput{Message: "/skill:review" + arguments, Skill: &SkillInvocation{Name: skill.Name, Arguments: arguments}}))
	}
	for _, opts := range backend.opts {
		userContext := opts.CustomUserContext
		if !strings.Contains(userContext, "<instructions>Current &lt;instructions&gt; &amp; body\r\n\t </instructions>") || !strings.Contains(userContext, "<arguments>"+arguments+"</arguments>") || strings.Contains(userContext, "BODY MUST") {
			t.Fatalf("skill context = %q", userContext)
		}
	}
}

func TestClientSkillInvocationReadFailuresNeverSend(t *testing.T) {
	for _, failure := range []string{"removed", "oversized", "invalid utf8", "invalid metadata", "renamed", "directory", "escaping symlink"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			writeSkill(t, root, ".agents/skills/review", "review", "Review")
			provider := env.NewSkills(root, nil)
			summaries, err := provider.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			skill := summaries[0]
			if err := os.Remove(filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "removed":
			case "directory":
				if err := os.Mkdir(filepath.Join(root, ".agents/skills/review/SKILL.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "escaping symlink":
				outside := filepath.Join(t.TempDir(), "SKILL.md")
				if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
					t.Fatal(err)
				}
			default:
				content := "---\nname: review\ndescription: Review\n---\n"
				switch failure {
				case "oversized":
					content += strings.Repeat("x", 64*1024)
				case "invalid utf8":
					content += "\xff"
				case "invalid metadata":
					content = "---\nname: [\n---\n"
				case "renamed":
					content = strings.ReplaceAll(content, "name: review", "name: another")
				}
				if err := os.WriteFile(filepath.Join(root, ".agents/skills/review/SKILL.md"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			backend := &conversationRecordingBackend{messages: make(map[string][]string)}
			engine := New(backend, Config{ClientSkills: provider})
			var failureErr error
			for event := range engine.StartTurn(context.Background(), TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: skill.Name}}) {
				if event.Err != nil {
					failureErr = event.Err
				}
			}
			// The failed turn keeps its user block, like a backend failure.
			if failureErr == nil || len(backend.opts) != 0 || len(engine.Snapshot()) != 1 {
				t.Fatalf("failure = %v, backend requests = %d, transcript = %+v", failureErr, len(backend.opts), engine.Snapshot())
			}
		})
	}
}

func TestClientSkillInvocationResolvesCurrentRegistry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkillFile(t, root, ".agents/skills/review", "---\nname: review\ndescription: Project\n---\nPROJECT BODY")
	writeSkillFile(t, root, "extras/review", "---\nname: review\ndescription: Extra\n---\nEXTRA BODY")
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	engine := New(backend, Config{ClientSkills: env.NewSkills(root, []string{"extras"})})
	summaries := mustClientSkills(t, engine)
	summaries[0].Name = "tampered"
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}}))
	if !strings.Contains(backend.opts[0].CustomUserContext, "PROJECT BODY") {
		t.Fatal("invocation did not use registry precedence")
	}
	if err := os.Remove(filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	// No menu refresh is needed: the engine resolves the new conversation's registry.
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}}))
	if !strings.Contains(backend.opts[1].CustomUserContext, "EXTRA BODY") {
		t.Fatal("invocation retained the previous conversation's path")
	}
	var gotErr error
	for event := range engine.StartTurn(context.Background(), TurnInput{Message: "/skill:tampered", Skill: &SkillInvocation{Name: "tampered"}}) {
		if event.Err != nil {
			gotErr = event.Err
		}
	}
	if gotErr == nil || len(backend.opts) != 2 {
		t.Fatal("unregistered skill reached backend")
	}
}

func TestClientSkillInvocationKeepsDiscoveredDefaultName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkillFile(t, root, ".agents/skills/shared", "---\ndescription: Unnamed\n---\nSHARED BODY")
	if err := os.MkdirAll(filepath.Join(root, ".agents/skills/review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".agents/skills/shared/SKILL.md"), filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	engine := New(backend, Config{ClientSkills: env.NewSkills(root, nil)})
	for event := range engine.StartTurn(context.Background(), TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	if len(backend.opts) != 1 || !strings.Contains(backend.opts[0].CustomUserContext, "SHARED BODY") {
		t.Fatal("symlinked skill did not resolve under its discovered name")
	}
}
