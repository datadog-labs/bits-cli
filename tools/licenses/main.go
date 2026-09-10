// Command licenses keeps LICENSE-3rdparty.csv in sync with the real
// dependency tree:
//
//	go -C tools/licenses run . generate   # rewrite LICENSE-3rdparty.csv
//	go -C tools/licenses run . check      # fail if the committed CSV drifted
//
// The tool lives in its own nested module (see go.mod there) so the
// go-license-detector dependency and its tree stay out of the shipped module,
// whose go.sum defines the CSV membership; the same reasoning lives in
// scripts/lint.sh, scripts/test.sh, and scripts/generate-licenses.sh.
//
// The committed file is generated output: never hand-edit it, run
// scripts/generate-licenses.sh instead. Detection failures are loud (the
// run fails listing the modules) and corrections live in overrides.json.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// csvName and overridesPath are relative to the repository root.
const (
	csvName       = "LICENSE-3rdparty.csv"
	overridesPath = "tools/licenses/overrides.json"
)

// usage is the single-line interface contract, shared by the argument and
// command validation paths.
const usage = "usage: go -C tools/licenses run . <generate|check>"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "licenses: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("%s", usage)
	}
	root, err := findRepoRoot(".")
	if err != nil {
		return err
	}
	rows, err := buildInventory(root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "generate":
		generated, err := renderCSV(rows)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, csvName), generated, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", csvName, err)
		}
		fmt.Printf("generated %s (%d components)\n", csvName, len(rows))
		return nil
	case "check":
		return check(root, rows)
	default:
		return fmt.Errorf("unknown command %q: %s", args[0], usage)
	}
}

// buildInventory assembles the expected CSV rows from the shipped
// dependency tree: enumerate the modules recorded in the repository root's
// go.sum, download their sources, detect licenses, extract copyright
// holders, and apply manual overrides.
func buildInventory(root string) ([]row, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return nil, fmt.Errorf("read go.sum: %w", err)
	}
	paths := goSumModulePaths(data)
	modules, err := downloadModules(root, paths)
	if err != nil {
		return nil, err
	}
	infos := make([]moduleInfo, 0, len(paths))
	for _, path := range paths {
		info, ok := modules[path]
		if !ok {
			return nil, fmt.Errorf("module %s has no cached source directory", path)
		}
		infos = append(infos, info)
	}
	overrides, err := loadOverrides(filepath.Join(root, overridesPath))
	if err != nil {
		return nil, err
	}
	return buildRows(infos, overrides)
}

// check compares the committed CSV byte for byte with the canonical
// rendering of freshly generated rows: the file is generated output, so any
// difference — content, quoting, or row order — is drift and fails the run.
func check(root string, rows []row) error {
	committed, err := os.ReadFile(filepath.Join(root, csvName))
	if err != nil {
		return fmt.Errorf("read %s (run scripts/generate-licenses.sh to create it): %w", csvName, err)
	}
	expected, err := renderCSV(rows)
	if err != nil {
		return err
	}
	if bytes.Equal(committed, expected) {
		fmt.Printf("%s is up to date (%d components)\n", csvName, len(rows))
		return nil
	}

	// The git diff is a convenience; the byte comparison above is the verdict.
	tmp, err := os.CreateTemp("", "license-3rdparty-*.csv")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(expected); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	cmd := exec.Command("git", "diff", "--no-index", "--", filepath.Join(root, csvName), tmp.Name())
	if out, _ := cmd.CombinedOutput(); len(out) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", out)
	}
	return fmt.Errorf("%s is out of date with the current dependency tree; run scripts/generate-licenses.sh to regenerate it", csvName)
}

// findRepoRoot walks up from start until it reaches a go.mod declaring the
// bits-cli module, so the command works from anywhere inside the repository.
func findRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if isRepoRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the bits-cli repository root above %s", start)
		}
		dir = parent
	}
}

func isRepoRoot(dir string) bool {
	content, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	// The tools/licenses go.mod declares a module path derived from the
	// repository's own, so the match must be exact.
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "module "+mainModule {
			return true
		}
	}
	return false
}
