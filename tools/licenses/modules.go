package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// mainModule is the path of the repository's own module; it never appears in
// its go.sum, but the inventory rules it out explicitly.
const mainModule = "github.com/DataDog/bits-cli"

// majorVersionRe matches the /v2-style major version suffix of a module path.
var majorVersionRe = regexp.MustCompile(`/v[0-9]+$`)

// gopkgVersionRe splits a gopkg.in element like "yaml.v3" into name and
// version.
var gopkgVersionRe = regexp.MustCompile(`^(.*?)\.v[0-9]+$`)

// moduleInfo is an external dependency with a local module cache directory.
type moduleInfo struct {
	Path    string
	Version string
	Dir     string
}

// goSumModulePaths returns the sorted distinct external module paths with an
// h1: zip hash in the repository root's go.sum — the modules whose source is
// part of the build/test closure of the shipped tree. go.sum also records
// /go.mod-only hashes for modules that mere graph resolution touched but
// never built; those lines are skipped so the inventory lists only modules
// whose sources can actually ship. The tools/licenses nested module keeps
// build tools like go-license-detector out of the root go.sum. The main
// module and Datadog-internal modules are excluded: they are covered by the
// repository's own LICENSE and NOTICE.
func goSumModulePaths(data []byte) []string {
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !strings.HasPrefix(fields[2], "h1:") || strings.HasSuffix(fields[1], "/go.mod") {
			continue
		}
		path := fields[0]
		if isInternal(path) || slices.Contains(paths, path) {
			continue
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func isInternal(path string) bool {
	return path == mainModule ||
		strings.HasPrefix(path, "github.com/DataDog/") ||
		strings.HasPrefix(path, "datadoghq.com/")
}

// downloadModules runs "go mod download -json" for the given module paths and
// returns their resolved versions and cache directories. The command runs
// with a throwaway -modfile (go.mod and go.sum copied to a temp directory) so
// recording download hashes never dirties the working tree, while the shared
// module cache is still populated for later runs.
func downloadModules(root string, paths []string) (map[string]moduleInfo, error) {
	tmpDir, err := os.MkdirTemp("", "bits-licenses-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	for _, name := range [...]string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(root, name), filepath.Join(tmpDir, name)); err != nil {
			return nil, err
		}
	}

	args := append([]string{
		"mod", "download", "-json",
		"-modfile=" + filepath.Join(tmpDir, "go.mod"),
	}, paths...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go mod download: %w\n%s%s", err, out.String(), stderr.String())
	}

	modules := make(map[string]moduleInfo, len(paths))
	decoder := json.NewDecoder(&out)
	var failed []string
	for decoder.More() {
		var result struct {
			Path    string
			Version string
			Dir     string
			Error   *struct {
				Err string
			}
		}
		if err := decoder.Decode(&result); err != nil {
			return nil, fmt.Errorf("parse go mod download output: %w", err)
		}
		switch {
		case result.Error != nil:
			failed = append(failed, fmt.Sprintf("%s: %s", result.Path, result.Error.Err))
		case result.Dir == "":
			failed = append(failed, fmt.Sprintf("%s: no cached source directory", result.Path))
		default:
			modules[result.Path] = moduleInfo{Path: result.Path, Version: result.Version, Dir: result.Dir}
		}
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("could not download %d module(s):\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return modules, nil
}

func copyFile(src, dst string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// deriveOrigin maps a module path to its upstream repository URL using the
// module path conventions (github.com/<owner>/<repo>[/<subdir>], the
// golang.org/x/<name> vanity redirect, gopkg.in's two forms). Vanity domains
// like charm.land cannot be resolved offline; such modules need an origin in
// overrides.json.
func deriveOrigin(path string) string {
	base := majorVersionRe.ReplaceAllString(path, "")
	switch {
	case strings.HasPrefix(base, "github.com/"):
		parts := strings.Split(base, "/")
		if len(parts) >= 3 {
			return "https://github.com/" + parts[1] + "/" + parts[2]
		}
	case strings.HasPrefix(base, "golang.org/x/"):
		parts := strings.Split(base, "/")
		if len(parts) >= 3 {
			return "https://github.com/golang/" + parts[2]
		}
	case strings.HasPrefix(base, "gopkg.in/"):
		parts := strings.Split(base, "/")
		if len(parts) == 2 {
			if name := gopkgVersionRe.FindStringSubmatch(parts[1]); name != nil {
				return "https://github.com/go-" + name[1] + "/" + name[1]
			}
		}
		if len(parts) == 3 {
			if name := gopkgVersionRe.FindStringSubmatch(parts[2]); name != nil {
				return "https://github.com/" + parts[1] + "/" + name[1]
			}
		}
	}
	return ""
}

// override is a manual correction for one module. Any empty field keeps the
// detected value.
type override struct {
	// License replaces the detected license. When detection found something
	// else — a different single license, or the joined set of a multi-license
	// detection — Detected must record that detection result: it proves a
	// human re-confirmed the disagreement, and a later change of the
	// detection result upstream fails the run instead of being silently
	// overridden.
	License   string `json:"license,omitempty"`
	Detected  string `json:"detected,omitempty"`
	Copyright string `json:"copyright,omitempty"`
	Origin    string `json:"origin,omitempty"`
	// Copyleft acknowledges a copyleft-licensed dependency. Without it the
	// run fails on any copyleft license, so shipping one is always a
	// deliberate, documented decision rather than an accident.
	Copyleft bool `json:"copyleft,omitempty"`
	// Reason documents why the override exists; loadOverrides rejects entries
	// without one so the file stays self-explanatory.
	Reason string `json:"reason"`
}

// loadOverrides reads the data file of manual corrections. Detection errors
// and vanity-domain origins are recorded here rather than in code, so new
// cases are a data change.
func loadOverrides(path string) (map[string]override, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var overrides map[string]override
	if err := json.Unmarshal(content, &overrides); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for key, ov := range overrides {
		if ov.Reason == "" {
			return nil, fmt.Errorf("%s: entry for %s has no reason", path, key)
		}
	}
	return overrides, nil
}

// copyleftPrefixes are the SPDX id families whose terms make the covered
// code unusable in an Apache-2.0 distribution; the "-only" and
// "-or-later" variants share the prefix, and an "AND"/"OR" combination
// counts when any term is copyleft.
var copyleftPrefixes = []string{"GPL-", "AGPL-", "LGPL-", "EPL-"}

// isCopyleft reports whether a license value contains a copyleft SPDX term.
func isCopyleft(license string) bool {
	for _, prefix := range copyleftPrefixes {
		if strings.Contains(license, prefix) {
			return true
		}
	}
	return false
}

// csvHeader is the fixed contract of LICENSE-3rdparty.csv.
const csvHeader = "Component,Origin,License,Copyright"

// row is one LICENSE-3rdparty.csv record.
type row struct {
	Component string
	Origin    string
	License   string
	Copyright string
}

// buildRows turns module detections into inventory rows, applying overrides.
// It refuses to guess: a module whose license, copyright, or origin stays
// unresolved after detection and overrides is reported and the whole call
// fails, so nothing unresolved can slip into the inventory. A
// multi-license detection (two license files or a dual-license statement)
// is never auto-joined: it stays unresolved until an overrides.json entry
// states how the licenses combine. A license override that contradicts what
// detection found also fails the call unless the entry records the detection
// result it re-confirmed, and an override key that matched no module fails
// too, so stale or mistyped keys cannot be silently ignored.
func buildRows(modules []moduleInfo, overrides map[string]override) ([]row, error) {
	var rows []row
	var unresolved []string
	var copyleft []string
	consumed := make(map[string]bool, len(overrides))
	for _, module := range modules {
		detection, err := detectModule(module.Dir)
		if err != nil {
			return nil, err
		}
		// An unresolved detection contributes no license: a multi-license
		// set ("MIT OR Apache-2.0") is a detection result, not an answer to
		// how the licenses combine, so the row's license must come from an
		// override.
		r := row{
			Component: module.Path,
			Copyright: detection.Copyright,
		}
		if detection.Known {
			r.License = detection.Licenses
		}
		ack := false
		if ov, ok := overrides[module.Path]; ok {
			consumed[module.Path] = true
			ack = ov.Copyleft
			if ov.License != "" {
				if detection.Licenses != "" && detection.Licenses != ov.License && ov.Detected != detection.Licenses {
					return nil, fmt.Errorf(
						"%s: override license %q disagrees with the detected license %q; re-confirm the upstream state before overriding (record the re-confirmed detection result in the \"detected\" field of its overrides.json entry)",
						module.Path, ov.License, detection.Licenses)
				}
				r.License = ov.License
			}
			if ov.Copyright != "" {
				r.Copyright = ov.Copyright
			}
			r.Origin = ov.Origin
		}
		if r.Origin == "" {
			r.Origin = deriveOrigin(module.Path)
		}
		switch {
		case !detection.Known && detection.Licenses != "" && r.License == "":
			unresolved = append(unresolved, fmt.Sprintf("%s@%s: multiple licenses detected (%s) in %s; per-file coverage or a dual-license statement must be resolved by hand", module.Path, module.Version, detection.Licenses, module.Dir))
		case !detection.Known && r.License == "":
			unresolved = append(unresolved, fmt.Sprintf("%s@%s: license could not be detected in %s", module.Path, module.Version, module.Dir))
		case r.Copyright == "":
			unresolved = append(unresolved, fmt.Sprintf("%s@%s: no copyright statement found in %s", module.Path, module.Version, module.Dir))
		case r.Origin == "":
			unresolved = append(unresolved, fmt.Sprintf("%s@%s: upstream origin is not derivable from the module path", module.Path, module.Version))
		case isCopyleft(r.License) && !ack:
			copyleft = append(copyleft, fmt.Sprintf("%s@%s: %s", module.Path, module.Version, r.License))
		default:
			rows = append(rows, r)
		}
	}
	if len(copyleft) > 0 {
		return nil, fmt.Errorf(
			"%d copyleft module(s) refused:\n  %s\n"+
				"If a copyleft dependency is deliberate, acknowledge it with \"copyleft\": true and a reason in tools/licenses/overrides.json",
			len(copyleft), strings.Join(copyleft, "\n  "))
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf(
			"%d of %d module(s) could not be resolved; refusing to guess:\n  %s\n"+
				"Add a tools/licenses/overrides.json entry for it",
			len(unresolved), len(modules), strings.Join(unresolved, "\n  "))
	}
	var stale []string
	for key := range overrides {
		if !consumed[key] {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		return nil, fmt.Errorf(
			"%d overrides.json entry/entries matched no module in the inventory; remove them or fix their keys:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
	slices.SortFunc(rows, func(a, b row) int {
		return strings.Compare(a.Component, b.Component)
	})
	return rows, nil
}
