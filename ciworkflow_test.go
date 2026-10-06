package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests pin the CI wiring around the release trust boundary: GitHub
// Actions runs ordinary checks unprivileged (including fork PRs), only
// strict stable tags may produce release artifacts, and the internal GitLab
// pipeline is the tag-only release flow feeding the manual publisher.

func loadYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func as[T any](t *testing.T, v any, what string) T {
	t.Helper()
	out, ok := v.(T)
	if !ok {
		t.Fatalf("%s: unexpected type %T", what, v)
	}
	return out
}

func jobMap(t *testing.T, wf map[string]any, name string) map[string]any {
	t.Helper()
	return as[map[string]any](t, as[map[string]any](t, wf["jobs"], "jobs")[name], name+" job")
}

func jobStep(t *testing.T, wf map[string]any, jobName, stepName string) map[string]any {
	t.Helper()
	for _, s := range as[[]any](t, jobMap(t, wf, jobName)["steps"], jobName+" steps") {
		if step := as[map[string]any](t, s, jobName+" step"); step["name"] == stepName {
			return step
		}
	}
	t.Fatalf("no %q step in %q job", stepName, jobName)
	return nil
}

// runStepBody executes a workflow step's `run` body under the GitHub
// Actions Linux shell semantics (bash with -e and pipefail) from dir.
func runStepBody(t *testing.T, dir, body string, extraEnv ...string) error {
	t.Helper()
	cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", body)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Run()
}

func TestWorkflowUnprivilegedTrustBoundary(t *testing.T) {
	wf := loadYAML(t, ".github/workflows/ci.yml")
	triggers := as[map[string]any](t, wf["on"], "workflow triggers")
	if _, ok := triggers["pull_request"]; !ok {
		t.Error("workflow must run on pull_request so fork PRs get ordinary checks")
	}
	for _, forbidden := range []string{"pull_request_target", "workflow_run"} {
		if _, ok := triggers[forbidden]; ok {
			t.Errorf("workflow must not use %s (privileged fork access)", forbidden)
		}
	}
	tags := as[[]any](t, as[map[string]any](t, triggers["push"], "push trigger")["tags"], "push tags")
	if len(tags) != 1 || tags[0] != "v*" {
		t.Errorf("push tags = %v, want [v*] so tag pushes reach the workflow", tags)
	}
	perms := as[map[string]any](t, wf["permissions"], "permissions")
	if len(perms) != 1 || perms["contents"] != "read" {
		t.Errorf("permissions = %v, want only contents: read", perms)
	}
	if raw, err := os.ReadFile(".github/workflows/ci.yml"); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(raw), "secrets.") {
		t.Error("workflow must not reference any secrets")
	}
	pinned := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for name, v := range as[map[string]any](t, wf["jobs"], "jobs") {
		job := as[map[string]any](t, v, name+" job")
		if _, ok := job["permissions"]; ok {
			t.Errorf("%s job must not override the read-only permissions", name)
		}
		for i, s := range as[[]any](t, job["steps"], name+" steps") {
			step := as[map[string]any](t, s, name+" step")
			uses, _ := step["uses"].(string)
			if uses == "" {
				continue
			}
			if !pinned.MatchString(uses) {
				t.Errorf("%s job step %d uses %q, want a full-commit SHA pin", name, i, uses)
			}
			if strings.HasPrefix(uses, "actions/checkout") {
				with := as[map[string]any](t, step["with"], name+" checkout with")
				if with["persist-credentials"] != false {
					t.Errorf("%s job checkout must set persist-credentials: false", name)
				}
			}
		}
	}
}

func TestWorkflowReleaseTagGating(t *testing.T) {
	wf := loadYAML(t, ".github/workflows/ci.yml")
	resolve := jobStep(t, wf, "build", "Resolve release version")
	if cond, _ := resolve["if"].(string); !strings.Contains(cond, "refs/tags/") {
		t.Errorf("resolve step if = %q, must be gated on tag refs", cond)
	}
	if env := as[map[string]any](t, resolve["env"], "resolve env"); env["CI_COMMIT_TAG"] != "${{ github.ref_name }}" {
		t.Errorf("resolve env CI_COMMIT_TAG = %v, want ${{ github.ref_name }}", env["CI_COMMIT_TAG"])
	}
	job := jobMap(t, wf, "dogbrew-tarballs")
	if cond, _ := job["if"].(string); !strings.Contains(cond, "refs/tags/v") {
		t.Errorf("dogbrew-tarballs if = %q, must be gated on v tag refs", cond)
	}
	run, _ := jobStep(t, wf, "dogbrew-tarballs", "Package Dogbrew tarballs")["run"].(string)
	if !strings.Contains(run, "dogbrew.sh") || strings.Contains(run, "--publish") {
		t.Errorf("packaging step must run scripts/dogbrew.sh without --publish: %q", run)
	}
}

// The Build step must fail the job when scripts/build.sh fails: the old
// printf "$(./scripts/build.sh)" form swallowed the script's exit status.
func TestWorkflowBuildStepPropagatesBuildFailure(t *testing.T) {
	wf := loadYAML(t, ".github/workflows/ci.yml")
	run, _ := jobStep(t, wf, "build", "Build")["run"].(string)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nprintf 'build/bits-linux-amd64'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "build.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "github_output")
	if err := runStepBody(t, dir, run, "GITHUB_OUTPUT="+out); err == nil {
		t.Fatal("Build step body must fail when scripts/build.sh fails")
	}
	if written, err := os.ReadFile(out); err == nil && strings.Contains(string(written), "binary=") {
		t.Errorf("failed build must not write binary output, got %q", written)
	}
}

// Artifact upload/download normalizes file modes to 0644; the packaging
// step must restore the executable bit. The binaries are never run here.
func TestWorkflowPackagingRestoresExecutableBits(t *testing.T) {
	wf := loadYAML(t, ".github/workflows/ci.yml")
	run, _ := jobStep(t, wf, "dogbrew-tarballs", "Package Dogbrew tarballs")["run"].(string)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"scripts/dogbrew.sh", "cli.yaml"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(file)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "dogbrew.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	const version = "1.2.3"
	platforms := []string{"darwin-arm64", "linux-amd64", "linux-arm64"}
	for _, p := range platforms {
		// Fake binaries arrive with the artifact-normalized 0644 mode.
		if err := os.WriteFile(filepath.Join(dir, "dist", "bits-"+p), []byte("fake binary"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := runStepBody(t, dir, run, "VERSION="+version); err != nil {
		t.Fatalf("packaging step body failed: %v", err)
	}

	cliYAML, err := os.ReadFile("cli.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Mirror the manifest rewrite scripts/dogbrew.sh applies.
	wantManifest := regexp.MustCompile(`(?m)^version:.*$`).
		ReplaceAllString(string(cliYAML), "version: "+version)
	for _, p := range platforms {
		extract := t.TempDir()
		archive := filepath.Join(dir, "dist", "bits-"+p+".tar.gz")
		tar := exec.Command("tar", "-xzf", archive, "-C", extract)
		if out, err := tar.CombinedOutput(); err != nil {
			t.Fatalf("extract %s: %v: %s", archive, err, out)
		}
		manifest, err := os.ReadFile(filepath.Join(extract, "cli.yaml"))
		if err != nil || string(manifest) != wantManifest {
			t.Errorf("archive for %s must contain cli.yaml with version %s", p, version)
		}
		info, statErr := os.Stat(filepath.Join(extract, "bits-"+p))
		if statErr != nil || info.Mode()&0o111 == 0 || info.Size() == 0 {
			t.Errorf("archive for %s must carry an executable, non-empty binary: %v", p, statErr)
		}
	}
}

func TestGitlabPipelineIsStableTagReleaseOnly(t *testing.T) {
	gl := loadYAML(t, ".gitlab-ci.yml")
	const strictStableTag = `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`

	rules := as[[]any](t, as[map[string]any](t, gl["workflow"], "workflow")["rules"], "workflow rules")
	if len(rules) != 2 || as[map[string]any](t, rules[1], "second workflow rule")["when"] != "never" {
		t.Fatalf("workflow rules = %v, want one strict tag rule plus when: never", rules)
	}
	if ifExpr, _ := as[map[string]any](t, rules[0], "first workflow rule")["if"].(string); !strings.Contains(ifExpr, strictStableTag) {
		t.Errorf("workflow rule if = %q, want strict stable tag gate", ifExpr)
	}
	jobs := as[map[string]any](t, gl, ".gitlab-ci.yml")
	for _, name := range []string{"lint", "test", "build", "publish-to-dogbrew"} {
		if _, ok := jobs[name].(map[string]any); !ok {
			t.Errorf("GitLab pipeline must keep the %q job for the release flow", name)
		}
	}
	publish := as[map[string]any](t, jobs["publish-to-dogbrew"], "publish job")
	var script strings.Builder
	for _, line := range as[[]any](t, publish["script"], "publish script") {
		script.WriteString(line.(string) + "\n")
	}
	if !strings.Contains(script.String(), "--publish") {
		t.Error("publish-to-dogbrew must keep its --publish gate")
	}
	first := as[map[string]any](t, as[[]any](t, publish["rules"], "publish rules")[0], "first publish rule")
	if first["when"] != "manual" {
		t.Errorf("publish rule = %v, want when: manual", first)
	}
	if ifExpr, _ := first["if"].(string); !strings.Contains(ifExpr, strictStableTag) {
		t.Errorf("publish rule if = %q, want strict stable tag gate", ifExpr)
	}
}

func TestVersionScriptStrictStableTags(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.0.0": "0.0.0", "v1.2.3": "1.2.3", "v10.20.30": "10.20.30",
	} {
		got, err := runVersionScript(t, tag)
		if err != nil || strings.TrimSpace(got) != want {
			t.Errorf("version.sh(%q) = %q, err %v; want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"", "main", "v1.2", "1.2.3", "v01.2.3", "v1.2.3-rc1", "refs/tags/v1.2.3"} {
		if _, err := runVersionScript(t, tag); err == nil {
			t.Errorf("version.sh with CI_COMMIT_TAG=%q must fail", tag)
		}
	}
}

func runVersionScript(t *testing.T, tag string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/version.sh")
	cmd.Env = append(os.Environ(), "CI_COMMIT_TAG="+tag)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), err
}
