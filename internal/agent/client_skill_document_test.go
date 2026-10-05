package agent

import "testing"

func TestSkillDocumentPreservesBody(t *testing.T) {
	for _, tc := range []struct{ name, input, body string }{
		{"lf", "---\nname: review\ndescription: Review\n---\n\n  body\n---\nend  ", "\n  body\n---\nend  "},
		{"bom and crlf", "\xef\xbb\xbf---\r\ndescription: Review\r\nmodel-invocable: false\r\n---\r\n\r\n\tBody\r\n ", "\r\n\tBody\r\n "},
		{"empty body", "---\ndescription: Review\n---", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document, ok := parseSkillDocument([]byte(tc.input), "review")
			if !ok || document.Name != "review" || document.body != tc.body {
				t.Fatalf("document = %+v, valid = %v, want body %q", document, ok, tc.body)
			}
		})
	}
}
