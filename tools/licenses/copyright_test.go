package main

import (
	"slices"
	"strings"
	"testing"
)

func TestExtractCopyrightFindsStatements(t *testing.T) {
	mit := strings.Replace(mitLicenseText, "Copyright (c) 2020-2026 Charmbracelet, Inc.", "Copyright (c) 2023 Example, Inc.", 1)
	got := extractCopyright([]byte(mit))
	if want := []string{"Copyright (c) 2023 Example, Inc."}; !slices.Equal(got, want) {
		t.Fatalf("extractCopyright = %q, want %q", got, want)
	}
}

func TestExtractCopyrightIgnoresBoilerplate(t *testing.T) {
	content := `Copyright 2009 The Go Authors.

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.

      To apply the Apache License to your work, attach the following
      boilerplate notice:

   Copyright [yyyy] [name of copyright owner]

   2. Grant of Copyright License. Subject to the terms and conditions of
      the License, You hereby grant to Licensor a perpetual, worldwide,
`
	got := extractCopyright([]byte(content))
	if want := []string{"Copyright 2009 The Go Authors."}; !slices.Equal(got, want) {
		t.Fatalf("extractCopyright = %q, want %q", got, want)
	}
}

func TestExtractCopyrightHandlesMarkdownDecoration(t *testing.T) {
	content := "> Copyright © 2011 Russ Ross\n>     copyright notice, this list of conditions\n"
	got := extractCopyright([]byte(content))
	if want := []string{"Copyright © 2011 Russ Ross"}; !slices.Equal(got, want) {
		t.Fatalf("extractCopyright = %q, want %q", got, want)
	}
}

func TestExtractCopyrightKeepsDistinctStatements(t *testing.T) {
	content := "Copyright (c) 2014 First, Inc.\nCopyright (c) 2014 First, Inc.\nCopyright (c) 2017 Second\n"
	got := strings.Join(extractCopyright([]byte(content)), "; ")
	want := "Copyright (c) 2014 First, Inc.; Copyright (c) 2017 Second"
	if got != want {
		t.Fatalf("extractCopyright = %q, want %q", got, want)
	}
}
