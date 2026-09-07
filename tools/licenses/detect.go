package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-enry/go-license-detector/v4/licensedb"
	"github.com/go-enry/go-license-detector/v4/licensedb/filer"
)

// minConfidence is the per-file similarity a detector match must reach to
// count as that file's license. The detector's nearest-neighbor search also
// emits partial lookalikes (a "MIT-feh" at ~0.84 next to a "MIT" at ~0.98),
// so only each file's best match is considered, and only above this bar.
const minConfidence = 0.8

// canonicalOrder ranks common SPDX identifiers so dual-license results are
// joined deterministically ("MIT OR Apache-2.0", not the reverse).
var canonicalOrder = []string{"MIT", "Apache-2.0", "BSD-3-Clause", "BSD-2-Clause", "ISC"}

// licenseFileRe matches the top-level file names that may carry license or
// notice text: LICENSE, LICENSE.md, LICENSE-MIT, COPYING, NOTICE, ... The
// extensions are restricted to documentation formats (or none) so Go source
// files named license.go or copyright.go are not read as license text.
var licenseFileRe = regexp.MustCompile(`(?i)^(licen[cs]e|copying|copyright|notice)[-_.0-9a-z]*(?:\.(?:txt|md|rst|html))?$`)

// dualLicenseRe matches statements by which a file declares itself dual
// licensed (e.g. the go-yaml LICENSE: "This project is covered by two
// different licenses: MIT and Apache.").
var dualLicenseRe = regexp.MustCompile(`(?i)covered by two different licenses|dual[- ]licen[cs]ed`)

// licenseAliases maps names used in dual-license statements to SPDX ids.
var licenseAliases = []struct {
	re *regexp.Regexp
	id string
}{
	{regexp.MustCompile(`(?i)\bmit\b`), "MIT"},
	{regexp.MustCompile(`(?i)\bapache\b`), "Apache-2.0"},
	{regexp.MustCompile(`(?i)bsd[- ]?2\b`), "BSD-2-Clause"},
	{regexp.MustCompile(`(?i)bsd[- ]?3\b`), "BSD-3-Clause"},
	{regexp.MustCompile(`(?i)\bisc\b`), "ISC"},
}

// moduleDetection is the outcome of inspecting one module's directory.
type moduleDetection struct {
	// Licenses holds the detected licenses: the single SPDX identifier of a
	// normally licensed module, or the detected set joined in canonical order
	// ("MIT OR Apache-2.0") when more than one distinct license was found.
	Licenses string
	// Copyright joins the distinct copyright statements of the module's
	// license files with "; ".
	Copyright string
	// Known is true only when exactly one license was detected: the module
	// is then resolved without human judgment. When more than one distinct
	// license is detected, Known is false (with Licenses recording the set)
	// and the caller must require an overrides.json entry instead of guessing
	// how the licenses combine.
	Known bool
}

// detectModule runs go-license-detector over a module's cached source
// directory. licensedb.Detect scans every license-looking file (plus
// READMEs of modules that only name their license there) and returns
// SPDX-style matches with per-file confidences. Two rules govern the
// module-level result:
//
//   - the best match of each file counts, so a module shipping two license
//     files (LICENSE-MIT + LICENSE-BSD) yields the set "MIT OR
//     BSD-3-Clause";
//   - a file explicitly declaring dual licensing contributes the licenses
//     it names, so the go-yaml "covered by two different licenses: MIT and
//     Apache" statement yields the set "MIT OR Apache-2.0" even where the
//     second license appears only in notice form; a declaration that names
//     fewer than two licenses (e.g. wrapped across lines) is ambiguous and
//     leaves the module unresolved rather than guessed from.
//
// Only a single detected license resolves the module. Two distinct
// licenses means per-file coverage (e.g. go-yaml: libyaml-derived files
// MIT, the rest Apache-2.0) or separate license files (e.g. go-udiff:
// ported Go code BSD-3, additions MIT) — coverage, not a choice of either
// side — so the module is left unresolved (Known false, Licenses recording
// the set) and its License value must come from an overrides.json entry.
// More than two distinct licenses is treated as detection noise: the module
// is left unresolved so buildRows reports it instead of guessing.
func detectModule(dir string) (moduleDetection, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return moduleDetection{}, fmt.Errorf("list %s: %w", dir, err)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	var licenses []string
	var statements []string
	for _, entry := range entries {
		if entry.IsDir() || !licenseFileRe.MatchString(entry.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return moduleDetection{}, fmt.Errorf("read %s: %w", filepath.Join(dir, entry.Name()), err)
		}
		statementIDs, dualDeclared := detectDualStatement(content)
		if dualDeclared && len(statementIDs) < 2 {
			// The file declares dual licensing but the statement does not
			// resolve both licenses (e.g. wrapped over two physical lines).
			// Guessing from the rest of the file could pick the wrong side, so
			// the module stays unresolved and needs an overrides.json entry.
			return moduleDetection{}, nil
		}
		for _, id := range statementIDs {
			if !slices.Contains(licenses, id) {
				licenses = append(licenses, id)
			}
		}
		for _, statement := range extractCopyright(content) {
			if !slices.Contains(statements, statement) {
				statements = append(statements, statement)
			}
		}
	}

	fileLicenses, err := detectLicenseFiles(dir)
	if err != nil {
		return moduleDetection{}, err
	}
	for _, id := range fileLicenses {
		if !slices.Contains(licenses, id) {
			licenses = append(licenses, id)
		}
	}

	detection := moduleDetection{Copyright: strings.Join(statements, "; ")}
	switch len(licenses) {
	case 1:
		detection.Licenses = licenses[0]
		detection.Known = true
	case 2:
		slices.SortFunc(licenses, func(a, b string) int {
			return rankLicense(a) - rankLicense(b)
		})
		detection.Licenses = strings.Join(licenses, " OR ")
	}
	return detection, nil
}

// detectLicenseFiles returns the distinct SPDX ids of the licenses the
// detector accepted, taking only the best match of each file into account.
// License names are iterated in sorted order, and an exact confidence tie
// keeps the lexicographically lesser id: map iteration order is random, so
// without a fixed tie-break the same tree could generate different CSVs.
func detectLicenseFiles(dir string) ([]string, error) {
	f, err := filer.FromDirectory(dir)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	matches, err := licensedb.Detect(f)
	if err != nil {
		if errors.Is(err, licensedb.ErrNoLicenseFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("detect licenses in %s: %w", dir, err)
	}
	best := make(map[string]float32) // file -> best confidence so far
	id := make(map[string]string)    // file -> SPDX id of the best match
	for _, name := range slices.Sorted(maps.Keys(matches)) {
		for file, confidence := range matches[name].Files {
			if confidence < minConfidence || confidence <= best[file] {
				continue
			}
			best[file] = confidence
			id[file] = name
		}
	}
	var ids []string
	for _, file := range slices.Sorted(maps.Keys(id)) {
		if !slices.Contains(ids, id[file]) {
			ids = append(ids, id[file])
		}
	}
	return ids, nil
}

// detectDualStatement returns the SPDX ids named by an explicit dual-license
// declaration in the text, mirroring the go-yaml LICENSE wording, and whether
// such a declaration is present at all. The declaration is searched over the
// whole file, but aliases are only collected from the physical line carrying
// the declaration: a declaration that resolves fewer than both licenses must
// be treated as ambiguous by the caller, not guessed from.
func detectDualStatement(content []byte) (ids []string, declared bool) {
	declared = dualLicenseRe.Match(content)
	for _, line := range strings.Split(string(content), "\n") {
		if !dualLicenseRe.MatchString(line) {
			continue
		}
		for _, alias := range licenseAliases {
			if alias.re.MatchString(line) && !slices.Contains(ids, alias.id) {
				ids = append(ids, alias.id)
			}
		}
	}
	return ids, declared
}

// rankLicense positions an SPDX identifier within canonicalOrder; unknown ids
// sort last.
func rankLicense(id string) int {
	if i := slices.Index(canonicalOrder, id); i >= 0 {
		return i
	}
	return len(canonicalOrder)
}
