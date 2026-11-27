package spec

import (
	"slices"
	"testing"
)

func TestParseFollowUps(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{raw: `{"prompts":["Summarize this dashboard","Check my monitors"]}`, want: []string{"Summarize this dashboard", "Check my monitors"}},
		{raw: `{"prompts":["  A  ","", " \n", "B"]}`, want: []string{"A", "B"}},
		{raw: `{"prompts":[]}`},
		{raw: `{}`},
		{raw: `{"prompts":"A"}`},
		{raw: `{"prompts":["A",1]}`},
		{raw: `[]`},
		{raw: `not json`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := ParseFollowUps(tc.raw); !slices.Equal(got, tc.want) {
				t.Fatalf("ParseFollowUps(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
