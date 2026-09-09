package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoSumModulePaths(t *testing.T) {
	// Modules with only a /go.mod hash are part of the module graph but
	// never built; only modules with a zip (h1:) hash reach the inventory.
	goSum := "github.com/external/one v1.0.0 h1:hash=\n" +
		"github.com/external/one v1.0.0/go.mod h1:hash=\n" +
		"github.com/external/two v2.1.0 h1:hash=\n" +
		"github.com/graph/only v1.2.3/go.mod h1:hash=\n" +
		"github.com/DataDog/internal v1.0.0 h1:hash=\n" +
		"datadoghq.com/private/thing v1.0.0 h1:hash=\n"
	want := []string{"github.com/external/one", "github.com/external/two"}
	if got := goSumModulePaths([]byte(goSum)); !slices.Equal(got, want) {
		t.Fatalf("goSumModulePaths = %v, want %v", got, want)
	}
}

func TestDeriveOrigin(t *testing.T) {
	for module, want := range map[string]string{
		"github.com/spf13/cobra":               "https://github.com/spf13/cobra",
		"github.com/alecthomas/chroma/v2":      "https://github.com/alecthomas/chroma",
		"github.com/charmbracelet/x/ansi":      "https://github.com/charmbracelet/x",
		"golang.org/x/text":                    "https://github.com/golang/text",
		"gopkg.in/check.v1":                    "https://github.com/go-check/check",
		"gopkg.in/user/pkg.v3":                 "https://github.com/user/pkg",
		"charm.land/bubbles/v2":                "", // vanity domain: needs an override
		"cloud.google.com/go/compute/metadata": "",
	} {
		if got := deriveOrigin(module); got != want {
			t.Errorf("deriveOrigin(%q) = %q, want %q", module, got, want)
		}
	}
}

func TestBuildRowsAppliesOverrides(t *testing.T) {
	overridden := writeModule(t, "github.com/example/cobra-like", map[string]string{
		"LICENSE.txt": mitLicenseText,
	})
	vanity := writeModule(t, "charm.land/example/v2", map[string]string{
		"LICENSE": strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2024 Example, Inc.", 1),
	})
	rows, err := buildRows([]moduleInfo{overridden, vanity}, map[string]override{
		"github.com/example/cobra-like": {
			License:   "Apache-2.0",
			Detected:  "MIT",
			Copyright: "Copyright The Example Authors",
			Reason:    "fixture",
		},
		"charm.land/example/v2": {Origin: "https://github.com/example/example"},
	})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	// Rows are sorted by component path.
	if len(rows) != 2 ||
		rows[0] != (row{"charm.land/example/v2", "https://github.com/example/example", "MIT", "Copyright (c) 2024 Example, Inc."}) ||
		rows[1] != (row{"github.com/example/cobra-like", "https://github.com/example/cobra-like", "Apache-2.0", "Copyright The Example Authors"}) {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestBuildRowsFailsLoudlyInsteadOfGuessing(t *testing.T) {
	unknownLicense := writeModule(t, "github.com/example/eula", map[string]string{
		"LICENSE": "Proprietary. All rights reserved.\n",
	})
	unknownOrigin := writeModule(t, "charm.land/example/v2", map[string]string{
		"LICENSE": mitLicenseText,
	})
	noCopyright := writeModule(t, "github.com/example/anonymous", map[string]string{
		// The BSD clauses only mention copyright, never assert it.
		"LICENSE": strings.Join(strings.Split(goStyleBSD3Text, "\n")[1:], "\n"),
	})
	_, err := buildRows([]moduleInfo{unknownLicense, unknownOrigin, noCopyright},
		map[string]override{})
	if err == nil {
		t.Fatal("buildRows should have failed")
	}
	for _, fragment := range []string{
		"github.com/example/eula@v1.0.0: license could not be detected",
		"github.com/example/anonymous@v1.0.0: no copyright statement",
		"charm.land/example/v2@v1.0.0: upstream origin is not derivable",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

// TestMembershipExcludesToolOnlyDeps pins the shipped-tree membership rule:
// the CSV enumerates the repository root's go.sum, and tools/licenses lives
// in a nested module precisely so that its dependencies (the license
// detector and its tree) never appear there. The detector module is
// required by tools/licenses/go.mod, so if this test fails the tool's
// dependencies have leaked into the shipped dependency tree.
func TestMembershipExcludesToolOnlyDeps(t *testing.T) {
	root, err := findRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	members := goSumModulePaths(data)
	if slices.Contains(members, "github.com/go-enry/go-license-detector/v4") {
		t.Error("go-license-detector leaked into the shipped dependency tree; " +
			"tools/licenses dependencies must stay in the nested module")
	}
	// The exact count is pinned: a dependency change updates it together
	// with the regenerated LICENSE-3rdparty.csv, so silent go.sum growth
	// fails here rather than slipping into the inventory unnoticed.
	if len(members) != 53 {
		t.Errorf("membership = %d modules, want 53", len(members))
	}
	// The testify assertion is a canary: root go.sum must still contain
	// real (test-only) dependencies, otherwise the exclusion above would
	// pass vacuously on an empty enumeration.
	if !slices.Contains(members, "github.com/stretchr/testify") {
		t.Error("expected test-only dependency github.com/stretchr/testify in membership")
	}
	toolSum, err := os.ReadFile(filepath.Join(root, "tools", "licenses", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(goSumModulePaths(toolSum), "github.com/go-enry/go-license-detector/v4") {
		t.Error("expected go-license-detector in tools/licenses/go.sum")
	}
}

func TestBuildRowsFailsOnUnresolvedMultiLicenseDetection(t *testing.T) {
	// A multi-license detection is never auto-joined: without an override
	// stating how the licenses combine, the module is reported as unresolved.
	dualStatement := writeModule(t, "github.com/example/dual", map[string]string{
		"LICENSE": dualStatementLicense,
	})
	twoFiles := writeModule(t, "github.com/example/twofiles", map[string]string{
		"LICENSE-BSD": goStyleBSD3Text,
		"LICENSE-MIT": strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2023 A. Uthor", 1),
	})
	_, err := buildRows([]moduleInfo{dualStatement, twoFiles}, map[string]override{})
	if err == nil {
		t.Fatal("buildRows should have failed on multi-license modules without overrides")
	}
	for module, detected := range map[string]string{
		"github.com/example/dual":     "multiple licenses detected (MIT OR Apache-2.0)",
		"github.com/example/twofiles": "multiple licenses detected (MIT OR BSD-3-Clause)",
	} {
		fragment := module + "@v1.0.0: " + detected
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestBuildRowsResolvesMultiLicenseDetectionViaOverride(t *testing.T) {
	// The same shapes as the go-yaml and go-udiff overrides.json entries:
	// per-file coverage is combined with AND, and "detected" records the
	// detection set the entry re-confirmed.
	dualStatement := writeModule(t, "github.com/example/dual", map[string]string{
		"LICENSE": dualStatementLicense,
	})
	twoFiles := writeModule(t, "github.com/example/twofiles", map[string]string{
		"LICENSE-BSD": goStyleBSD3Text,
		"LICENSE-MIT": strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2023 A. Uthor", 1),
	})
	rows, err := buildRows([]moduleInfo{dualStatement, twoFiles}, map[string]override{
		"github.com/example/dual": {
			License:  "MIT AND Apache-2.0",
			Detected: "MIT OR Apache-2.0",
			Reason:   "fixture: per-file coverage",
		},
		"github.com/example/twofiles": {
			License:  "MIT AND BSD-3-Clause",
			Detected: "MIT OR BSD-3-Clause",
			Reason:   "fixture: per-file coverage",
		},
	})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	if len(rows) != 2 ||
		rows[0].License != "MIT AND Apache-2.0" || rows[0].Component != "github.com/example/dual" ||
		rows[1].License != "MIT AND BSD-3-Clause" || rows[1].Component != "github.com/example/twofiles" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestBuildRowsMultiLicenseOverrideNeedsConfirmedDetection(t *testing.T) {
	// An override on a multi-license module without the "detected"
	// confirmation would silently survive upstream relicensing, so it must
	// fail the run instead.
	dual := writeModule(t, "github.com/example/dual", map[string]string{
		"LICENSE": dualStatementLicense,
	})
	_, err := buildRows([]moduleInfo{dual}, map[string]override{
		"github.com/example/dual": {License: "MIT AND Apache-2.0", Reason: "fixture: unconfirmed"},
	})
	if err == nil {
		t.Fatal("buildRows should have failed on a multi-license override without a confirmed detection")
	}
	for _, fragment := range []string{
		"override license \"MIT AND Apache-2.0\"",
		"detected license \"MIT OR Apache-2.0\"",
		"re-confirm",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestBuildRowsFailsOnStaleOverrideKeys(t *testing.T) {
	mit := writeModule(t, "github.com/example/mod", map[string]string{"LICENSE": mitLicenseText})
	_, err := buildRows([]moduleInfo{mit}, map[string]override{
		"github.com/example/renamed-or-typo": {Origin: "https://github.com/example/mod", Reason: "stale fixture"},
	})
	if err == nil {
		t.Fatal("buildRows should have failed on an override that matched no module")
	}
	for _, fragment := range []string{
		"overrides.json entry",
		"github.com/example/renamed-or-typo",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestBuildRowsFailsOnLicenseOverrideDisagreement(t *testing.T) {
	mit := writeModule(t, "github.com/example/mod", map[string]string{"LICENSE": mitLicenseText})
	_, err := buildRows([]moduleInfo{mit}, map[string]override{
		"github.com/example/mod": {License: "Apache-2.0", Reason: "unconfirmed fixture"},
	})
	if err == nil {
		t.Fatal("buildRows should have failed on an override contradicting a confident detection")
	}
	for _, fragment := range []string{
		"override license \"Apache-2.0\"",
		"detected license \"MIT\"",
		"re-confirm",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestBuildRowsAcceptsConfirmedLicenseOverride(t *testing.T) {
	mit := writeModule(t, "github.com/example/mod", map[string]string{"LICENSE": mitLicenseText})
	// Same shape as the go-spew entry: the override pins the license the
	// file titles itself as, and "detected" records the detection result a
	// human re-confirmed the disagreement against.
	rows, err := buildRows([]moduleInfo{mit}, map[string]override{
		"github.com/example/mod": {License: "ISC", Detected: "MIT", Reason: "fixture: file titles itself ISC"},
	})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	if len(rows) != 1 || rows[0].License != "ISC" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestBuildRowsFailsWhenDetectionMovedOnFromConfirmedOverride(t *testing.T) {
	mit := writeModule(t, "github.com/example/mod", map[string]string{"LICENSE": mitLicenseText})
	_, err := buildRows([]moduleInfo{mit}, map[string]override{
		"github.com/example/mod": {License: "ISC", Detected: "0BSD", Reason: "fixture: stale confirmation"},
	})
	if err == nil {
		t.Fatal("buildRows should have failed when the detection result no longer matches the recorded confirmation")
	}
}

func TestLoadOverridesRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadOverrides(path)
	if err == nil {
		t.Fatal("loadOverrides should have failed on invalid JSON")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not mention the file path %q", err, path)
	}
}

func TestLoadOverridesRejectsEntriesWithoutReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.json")
	content := `{"github.com/example/mod": {"license": "MIT"}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadOverrides(path)
	if err == nil {
		t.Fatal("loadOverrides should have failed on an entry without a reason")
	}
	for _, fragment := range []string{"github.com/example/mod", "no reason"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestIsCopyleft(t *testing.T) {
	for _, tc := range []struct {
		license string
		want    bool
	}{
		{"MIT", false},
		{"Apache-2.0", false},
		{"BSD-3-Clause", false},
		{"BSD-2-Clause", false},
		{"ISC", false},
		{"MPL-2.0", false},
		{"MIT OR Apache-2.0", false},
		{"GPL-2.0", true},
		{"GPL-3.0-only", true},
		{"GPL-2.0-or-later", true},
		{"AGPL-3.0-only", true},
		{"LGPL-2.1", true},
		{"EPL-2.0", true},
		{"MIT AND GPL-2.0", true},
	} {
		if got := isCopyleft(tc.license); got != tc.want {
			t.Errorf("isCopyleft(%q) = %v, want %v", tc.license, got, tc.want)
		}
	}
}

func TestBuildRowsRefusesCopyleft(t *testing.T) {
	gpl := writeModule(t, "github.com/example/gpl", map[string]string{
		"LICENSE": "Some proprietary EULA. Do not redistribute.\n\nCopyright (c) 2026 A. Uthor\n",
	})
	_, err := buildRows([]moduleInfo{gpl}, map[string]override{
		"github.com/example/gpl": {License: "GPL-3.0", Reason: "fixture"},
	})
	if err == nil {
		t.Fatal("buildRows should have refused the copyleft license")
	}
	for _, fragment := range []string{
		"1 copyleft module(s) refused",
		"github.com/example/gpl@v1.0.0: GPL-3.0",
		"\"copyleft\": true",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not mention %q", err, fragment)
		}
	}
}

func TestBuildRowsAcceptsAcknowledgedCopyleft(t *testing.T) {
	gpl := writeModule(t, "github.com/example/gpl", map[string]string{
		"LICENSE": "Some proprietary EULA. Do not redistribute.\n\nCopyright (c) 2026 A. Uthor\n",
	})
	rows, err := buildRows([]moduleInfo{gpl}, map[string]override{
		"github.com/example/gpl": {License: "GPL-3.0", Copyleft: true, Reason: "fixture: deliberate copyleft"},
	})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	if len(rows) != 1 || rows[0].License != "GPL-3.0" || rows[0].Component != "github.com/example/gpl" {
		t.Fatalf("rows = %#v", rows)
	}
}
