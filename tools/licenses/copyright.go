package main

import (
	"regexp"
	"slices"
	"strings"
)

// copyrightLineRe matches lines that open with a copyright statement. It is
// deliberately broad (any line starting with "copyright", an optional "(c)" or
// "©", optionally behind markdown decoration); copyrightIgnoreList filters out
// the boilerplate such a broad match drags in. go-license-detector exposes no
// copyright finder (its v4 fork dropped the finder package), so the pattern is
// this tool's own.
var copyrightLineRe = regexp.MustCompile(`(?i)^[ \t]*(?:[#>][ \t]+)*(?:\(c\)[ \t]*|©[ \t]*)?copyright\b`)

// copyrightIgnoreList holds phrases that indicate a line mentions copyright
// without asserting one (license template boilerplate, BSD clause bodies).
// A line containing any of them (case-insensitively) is not a copyright
// statement, even when it starts with "Copyright".
var copyrightIgnoreList = []string{
	"copyright [yyyy]",
	"[name of copyright owner]",
	"copyright notice",
	"copyright owner",
	"copyright holder",
	"copyright law",
	"copyright license",
	"copyright statement",
	// yaml.v3's LICENSE misspells the phrase as "staring"; both spellings
	// are covered so the upstream typo cannot resurface as a bogus statement.
	"copyright starting",
	"copyright staring",
}

// extractCopyright returns the distinct copyright statements of a license
// file, in order of appearance. Lines are quoted verbatim from the file except
// for markdown decoration ("> ", "# " and similar) which is file formatting,
// not part of the statement.
func extractCopyright(content []byte) []string {
	var statements []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !isCopyrightStatement(line) {
			continue
		}
		statement := strings.TrimSpace(strings.TrimLeft(line, " \t#>"))
		if statement == "" || slices.Contains(statements, statement) {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

// isCopyrightStatement reports whether a line asserts a copyright (as
// opposed to merely mentioning one).
func isCopyrightStatement(line string) bool {
	return copyrightLineRe.MatchString(line) && !matchesIgnoreList(line)
}

func matchesIgnoreList(line string) bool {
	lower := strings.ToLower(line)
	return slices.ContainsFunc(copyrightIgnoreList, func(phrase string) bool {
		return strings.Contains(lower, phrase)
	})
}
