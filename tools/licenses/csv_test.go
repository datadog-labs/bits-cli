package main

import (
	"bytes"
	"encoding/csv"
	"slices"
	"testing"
)

func TestRenderCSVQuotesFieldsNeedingIt(t *testing.T) {
	rows := []row{{
		Component: "example.com/mod",
		Origin:    "https://github.com/org/repo",
		License:   "MIT OR Apache-2.0",
		Copyright: `Copyright (c) 2024 "Example, Inc."`,
	}}
	want := "Component,Origin,License,Copyright\n" +
		"example.com/mod,https://github.com/org/repo,MIT OR Apache-2.0," +
		`"Copyright (c) 2024 ""Example, Inc."""` + "\n"
	got, err := renderCSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("renderCSV:\n got %q\nwant %q", got, want)
	}
}

func TestRenderCSVRoundTripsThroughEncodingCSV(t *testing.T) {
	rows := []row{
		{Component: "b.example.com/mod/v2", Origin: "https://example.com/b", License: "BSD-2-Clause", Copyright: "Copyright 2011 A. Uthor"},
		{Component: "a.example.com/mod", Origin: "https://example.com/a", License: "MIT", Copyright: "Copyright (c) 2024 Example, Inc.\nSecond line"},
	}
	rendered, err := renderCSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := csv.NewReader(bytes.NewReader(rendered)).ReadAll()
	if err != nil {
		t.Fatalf("parse rendered CSV: %v", err)
	}
	want := [][]string{
		{"Component", "Origin", "License", "Copyright"},
		{"b.example.com/mod/v2", "https://example.com/b", "BSD-2-Clause", "Copyright 2011 A. Uthor"},
		{"a.example.com/mod", "https://example.com/a", "MIT", "Copyright (c) 2024 Example, Inc.\nSecond line"},
	}
	if !slices.EqualFunc(parsed, want, slices.Equal[[]string, string]) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", parsed, want)
	}
}

// TestRenderCSVWritesRowsInGivenOrder pins that renderCSV does not sort: the
// canonical row order comes from buildRows, so the byte-for-byte drift check
// in main.go also catches a reordered committed file.
func TestRenderCSVWritesRowsInGivenOrder(t *testing.T) {
	rows := []row{
		{Component: "b.example.com/mod", Origin: "https://example.com/b", License: "MIT", Copyright: "Copyright 2011 A. Uthor"},
		{Component: "a.example.com/mod", Origin: "https://example.com/a", License: "MIT", Copyright: "Copyright 2012 B. Uthor"},
	}
	rendered, err := renderCSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := "Component,Origin,License,Copyright\n" +
		"b.example.com/mod,https://example.com/b,MIT,Copyright 2011 A. Uthor\n" +
		"a.example.com/mod,https://example.com/a,MIT,Copyright 2012 B. Uthor\n"
	if string(rendered) != want {
		t.Fatalf("renderCSV:\n got %q\nwant %q", rendered, want)
	}
}
