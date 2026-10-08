package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]string
	Jobs        map[string]struct {
		If          string
		Uses        string
		Needs       needs
		Permissions map[string]string
		Steps       []struct {
			Uses string
			Run  string
			With map[string]any
		}
	}
}

// needs accepts both forms of a job's needs: a single job or a list.
type needs []string

func (n *needs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*n = needs{node.Value}
		return nil
	}
	return node.Decode((*[]string)(n))
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secrets.") {
		t.Errorf("%s must not reference secrets", name)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return wf
}

func TestWorkflowsHardening(t *testing.T) {
	pinned := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for _, name := range []string{"ci.yml", "release.yml"} {
		wf := loadWorkflow(t, name)
		for _, trigger := range []string{"pull_request_target", "workflow_run"} {
			if _, ok := wf.On[trigger]; ok {
				t.Errorf("%s must not use %s", name, trigger)
			}
		}
		if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
			t.Errorf("%s permissions = %v, want only contents: read", name, wf.Permissions)
		}
		for jobName, job := range wf.Jobs {
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "${{") {
					t.Errorf("%s/%s: pass expressions through env, not inline in run: %q", name, jobName, step.Run)
				}
				if step.Uses == "" {
					continue
				}
				if !pinned.MatchString(step.Uses) {
					t.Errorf("%s/%s uses %q, want a full commit SHA pin", name, jobName, step.Uses)
				}
				if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != false {
					t.Errorf("%s/%s checkout must set persist-credentials: false", name, jobName)
				}
			}
		}
	}
}

func TestCIWorkflowIsUnprivileged(t *testing.T) {
	wf := loadWorkflow(t, "ci.yml")
	for _, trigger := range []string{"pull_request", "workflow_call"} {
		if _, ok := wf.On[trigger]; !ok {
			t.Errorf("ci.yml must run on %s", trigger)
		}
	}
	for name, job := range wf.Jobs {
		if job.Permissions != nil {
			t.Errorf("ci.yml job %s must not override permissions", name)
		}
	}
}

func TestReleaseWorkflowGating(t *testing.T) {
	wf := loadWorkflow(t, "release.yml")
	if len(wf.On) != 1 || wf.On["workflow_dispatch"] == nil {
		t.Errorf("release.yml must only run on workflow_dispatch, got %v", wf.On)
	}
	if wf.Jobs["check"].If != "github.ref == 'refs/heads/main'" {
		t.Errorf("check job if = %q, must only release main", wf.Jobs["check"].If)
	}
	// release.sh --check compares against every existing tag.
	fullClone := false
	for _, step := range wf.Jobs["check"].Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["fetch-depth"] == 0 {
			fullClone = true
		}
	}
	if !fullClone {
		t.Error("check job must check out the full history and tags (fetch-depth: 0)")
	}
	if wf.Jobs["ci"].Uses != "./.github/workflows/ci.yml" {
		t.Errorf("release ci job uses %q, want ./.github/workflows/ci.yml", wf.Jobs["ci"].Uses)
	}
	for job, needs := range map[string][]string{"ci": {"check"}, "dist": {"check"}, "publish": {"ci", "dist"}} {
		for _, need := range needs {
			if !slices.Contains(wf.Jobs[job].Needs, need) {
				t.Errorf("%s must need %s, got %v", job, need, wf.Jobs[job].Needs)
			}
		}
	}
	// The GITHUB_TOKEN never gets write access to the repository: the
	// release and its tag are created with a dd-octo-sts token.
	for name, job := range wf.Jobs {
		if name != "publish" && job.Permissions != nil {
			t.Errorf("only the publish job may set permissions; %s sets %v", name, job.Permissions)
		}
	}
	want := map[string]string{"contents": "read", "id-token": "write", "attestations": "write"}
	if got := wf.Jobs["publish"].Permissions; !maps.Equal(got, want) {
		t.Errorf("publish permissions = %v, want %v", got, want)
	}
}

// command prepares name to run in dir with an environment isolated from the
// caller's: no inherited git, dist, or build settings, and a gh stub that
// fails, so that a test can never reach GitHub.
func command(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	bin := t.TempDir()
	stub := "#!/bin/sh\necho 'gh is stubbed in tests' >&2\nexit 99\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(key, "GIT_"), slices.Contains([]string{"BUILD_DIR", "DIST_DIR", "GOARCH", "GOOS", "VERSION"}, key):
			continue
		case key == "PATH":
			kv = "PATH=" + bin + string(os.PathListSeparator) + value
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return cmd
}

func TestReleaseCheck(t *testing.T) {
	dir := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := command(t, dir, "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	check := func(dir, tag string) error {
		return command(t, dir, "bash", "scripts/release.sh", "--check", tag).Run()
	}
	data, err := os.ReadFile("scripts/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "release.sh"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	git(dir, "init", "-q")
	git(dir, "add", "scripts")
	git(dir, "commit", "-q", "-m", "init")

	if err := check(dir, "v3.1.4"); err != nil {
		t.Errorf("first release must accept any tag: %v", err)
	}
	git(dir, "tag", "v0.9.0")
	git(dir, "tag", "v0.10.0")
	git(dir, "tag", "v1.0.0-rc.1")
	for tag, ok := range map[string]bool{
		"v0.10.1": true, "v0.11.0": true, "v1.0.0": true,
		"v0.10.0": false, "v0.9.1": false, "v0.10.2": false, "v0.12.0": false, "v2.0.0": false,
	} {
		if err := check(dir, tag); (err == nil) != ok {
			t.Errorf("release.sh --check %s: err = %v, want ok = %v", tag, err, ok)
		}
	}

	// A shallow clone has no tags; the check must fail instead of treating
	// the tag as a first release.
	shallow := filepath.Join(t.TempDir(), "shallow")
	git("", "clone", "-q", "--depth", "1", "--no-tags", "file://"+dir, shallow)
	if err := check(shallow, "v3.1.4"); err == nil {
		t.Error("release.sh --check must fail in a shallow clone")
	}
}

// Each release script validates its tag before having any side effect.
func TestScriptsRejectInvalidTags(t *testing.T) {
	for _, args := range [][]string{{"scripts/dist.sh"}, {"scripts/publish.sh"}, {"scripts/release.sh"}, {"scripts/release.sh", "--check"}} {
		for _, tag := range []string{"", "main", "v1.2", "1.2.3", "v01.2.3", "v1.2.3-rc1", "refs/tags/v1.2.3"} {
			script := strings.Join(args, " ")
			out, err := command(t, "", "bash", append(args, tag)...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), "usage:") {
				t.Errorf("%s %q: want usage error, got %v: %s", script, tag, err, out)
			}
		}
	}
}

// TestDistArchives runs dist.sh against a stub build.sh and checks the
// published layout: one archive per release platform plus checksums.
func TestDistArchives(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"scripts/dist.sh", "README.md", "LICENSE", "LICENSE-3rdparty.csv"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := "#!/usr/bin/env bash\nset -eu\nmkdir -p \"$BUILD_DIR\"\nout=\"$BUILD_DIR/bits-$GOOS-$GOARCH\"\necho \"$GOOS/$GOARCH $VERSION\" >\"$out\"\necho \"$out\"\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "build.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := command(t, dir, "bash", "scripts/dist.sh", "v1.2.3")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dist.sh: %v\n%s", err, out)
	}

	dist := filepath.Join(dir, "dist")
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{
		"bits_1.2.3_checksums.txt",
		"bits_1.2.3_darwin_arm64.tar.gz",
		"bits_1.2.3_linux_amd64.tar.gz",
		"bits_1.2.3_linux_arm64.tar.gz",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("dist contents = %v, want %v", names, want)
	}

	check := exec.Command("sha256sum", "--check", "bits_1.2.3_checksums.txt")
	if _, err := exec.LookPath("sha256sum"); err != nil {
		check = exec.Command("shasum", "-a", "256", "--check", "bits_1.2.3_checksums.txt")
	}
	check.Dir = dist
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("checksums do not verify: %v\n%s", err, out)
	}

	for _, platform := range []string{"darwin/arm64", "linux/amd64", "linux/arm64"} {
		archive := filepath.Join(dist, "bits_1.2.3_"+strings.ReplaceAll(platform, "/", "_")+".tar.gz")
		files := readTarGz(t, archive)
		if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, []string{"LICENSE", "LICENSE-3rdparty.csv", "README.md", "bits"}) {
			t.Errorf("%s contains %v", archive, got)
		}
		bits := files["bits"]
		if bits.mode&0o111 == 0 || bits.body != platform+" 1.2.3\n" {
			t.Errorf("%s: bits mode %o body %q", archive, bits.mode, bits.body)
		}
	}
}

type tarFile struct {
	mode int64
	body string
}

func readTarGz(t *testing.T, path string) map[string]tarFile {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]tarFile{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[hdr.Name] = tarFile{mode: hdr.Mode, body: string(body)}
	}
}
