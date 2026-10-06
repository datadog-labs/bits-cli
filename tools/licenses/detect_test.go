package main

import (
	"strings"
	"testing"
)

func TestDetectModuleMIT(t *testing.T) {
	dir := writeLicenseDir(t, map[string]string{"LICENSE": mitLicenseText})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Known || detection.Licenses != "MIT" {
		t.Errorf("MIT module detected as known=%v licenses=%q", detection.Known, detection.Licenses)
	}
	if detection.Copyright != "Copyright (c) 2020-2026 Charmbracelet, Inc." {
		t.Errorf("Copyright = %q", detection.Copyright)
	}
}

func TestDetectModuleGoStyleBSD3(t *testing.T) {
	// The reflowed clauses and "COPYRIGHT OWNER" wording must not pull the
	// match to a neighboring license (the detector also emits BSD-2-Clause
	// at ~0.84 for this text; only the per-file best match counts).
	dir := writeLicenseDir(t, map[string]string{"LICENSE": goStyleBSD3Text})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Known || detection.Licenses != "BSD-3-Clause" {
		t.Errorf("go-style BSD-3 detected as known=%v licenses=%q", detection.Known, detection.Licenses)
	}
}

func TestDetectModuleBSD2(t *testing.T) {
	dir := writeLicenseDir(t, map[string]string{"LICENSE": bsd2Text})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Known || detection.Licenses != "BSD-2-Clause" {
		t.Errorf("BSD-2 detected as known=%v licenses=%q", detection.Known, detection.Licenses)
	}
}

func TestDetectModuleDualStatementIsUnresolved(t *testing.T) {
	// go-yaml style: the dual statement names both licenses even though
	// the Apache part of the file is only notice text. Two distinct licenses
	// mean per-file coverage rather than a choice, so the module stays
	// unresolved; the detected set is recorded in canonical order for the
	// overrides.json entry that must resolve it.
	dir := writeLicenseDir(t, map[string]string{"LICENSE": dualStatementLicense})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known {
		t.Fatal("dual-statement module must stay unresolved")
	}
	if detection.Licenses != "MIT OR Apache-2.0" {
		t.Errorf("Licenses = %q, want MIT OR Apache-2.0", detection.Licenses)
	}
	if detection.Copyright != "Copyright (c) 2006-2011 A. Uthor; Copyright (c) 2011-2019 Other Corp" {
		t.Errorf("Copyright = %q", detection.Copyright)
	}
}

func TestDetectModuleTwoLicenseFilesIsUnresolved(t *testing.T) {
	// Two license files (the go-udiff shape) mean per-file coverage rather
	// than a choice of either side, so the module stays unresolved; the
	// detected set is recorded for the overrides.json entry that must
	// resolve it.
	dir := writeLicenseDir(t, map[string]string{
		"LICENSE-BSD": goStyleBSD3Text,
		"LICENSE-MIT": strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2023 A. Uthor", 1),
	})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known {
		t.Fatal("two-license-files module must stay unresolved")
	}
	if detection.Licenses != "MIT OR BSD-3-Clause" {
		t.Errorf("Licenses = %q, want MIT OR BSD-3-Clause", detection.Licenses)
	}
}

func TestDetectModuleTooManyLicenseFilesIsUnresolved(t *testing.T) {
	// Three distinct licenses is treated as detection noise: the module stays
	// unresolved and buildRows reports it instead of guessing. Noise is not
	// recorded as a detected set, unlike the two-license case above.
	dir := writeLicenseDir(t, map[string]string{
		"LICENSE-MIT":  strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2023 A. Uthor", 1),
		"LICENSE-BSD":  goStyleBSD3Text,
		"LICENSE-BSD2": bsd2Text,
	})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known || detection.Licenses != "" {
		t.Errorf("three-license-files module detected as known=%v licenses=%q", detection.Known, detection.Licenses)
	}
}

func TestDetectModuleAmbiguousDualStatementIsUnresolved(t *testing.T) {
	// The dual-license declaration is wrapped, so the second named license
	// never lands on the declaration's line; guessing from the rest of the
	// file could pick the wrong side, so the module must stay unresolved.
	wrapped := strings.Replace(dualStatementLicense,
		"This project is covered by two different licenses: MIT and Apache.",
		"This project is covered by two different licenses:\nMIT and Apache.", 1)
	dir := writeLicenseDir(t, map[string]string{"LICENSE": wrapped})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known {
		t.Errorf("ambiguous dual statement detected as %q", detection.Licenses)
	}
}

func TestDetectModuleIgnoresGoSourceNamedLicense(t *testing.T) {
	dir := writeLicenseDir(t, map[string]string{
		"LICENSE":    mitLicenseText,
		"license.go": "package x\n\n// Copyright (c) 2099 Bogus Holder\nfunc f() {}\n",
	})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Known || detection.Licenses != "MIT" {
		t.Errorf("MIT module detected as known=%v licenses=%q", detection.Known, detection.Licenses)
	}
	if strings.Contains(detection.Copyright, "Bogus Holder") {
		t.Errorf("license.go was read as a license file; copyright = %q", detection.Copyright)
	}
}

func TestDetectModuleIgnoresNonLicenseFiles(t *testing.T) {
	// The README names no SPDX license and the other files carry no license
	// text, so even the detector's README fallback finds nothing.
	dir := writeLicenseDir(t, map[string]string{
		"README.md":    "This project is proprietary. Do not redistribute.",
		"main.go":      "package main\nfunc main() {}\n",
		"LICENSE.html": "<html>license-ish words but no license body</html>",
	})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known {
		t.Errorf("module without license text detected as %q", detection.Licenses)
	}
}

func TestDetectModuleUnknownLicenseText(t *testing.T) {
	dir := writeLicenseDir(t, map[string]string{"LICENSE": "Some proprietary EULA. Do not redistribute.\n"})
	detection, err := detectModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Known {
		t.Errorf("unknown text detected as %q", detection.Licenses)
	}
}
