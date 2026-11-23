package tools

import (
	"strings"
	"testing"
)

func TestIsBinary(t *testing.T) {
	beyond := make([]byte, binaryProbeSize+1)
	for i := range beyond[:binaryProbeSize] {
		beyond[i] = 'a'
	}
	beyond[binaryProbeSize] = 0

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain text", []byte("hello world\n"), false},
		{"null byte mid", []byte("hello\x00world"), true},
		{"null byte first", append([]byte{0}, []byte("text")...), true},
		{"invalid UTF-8", []byte("hello\xffworld"), true},
		{"incomplete UTF-8 at end", append([]byte(strings.Repeat("a", binaryProbeSize-1)), 0xc3), true},
		{"long text no null", []byte(strings.Repeat("a", 10000)), false},
		{"null beyond probe limit", beyond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.data); got != tt.want {
				t.Errorf("isBinary = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeText(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		text string
		ok   bool
	}{
		{"UTF-8", "h\u00e9\n", "h\u00e9\n", true},
		{"UTF-8 BOM", bom + "x", bom + "x", true},
		{"UTF-16LE BOM", "\xff\xfeh\x00\xe9\x00", bom + "h\u00e9", true},
		{"UTF-16BE BOM", "\xfe\xff\x00h\x00\xe9", bom + "h\u00e9", true},
		{"UTF-16 surrogate pair", "\xff\xfe\x3d\xd8\x00\xde", bom + "\U0001F600", true},
		{"UTF-16 without BOM", "h\x00i\x00", "", false},
		{"UTF-16 odd length", "\xff\xfeh\x00i", "", false},
		{"UTF-16 lone surrogate", "\xff\xfeh\x00\x00\xd8", "", false},
		{"UTF-16 NUL", "\xff\xfeh\x00\x00\x00", "", false},
		{"legacy code page", "caf\xe9", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, enc, ok := decodeText([]byte(test.data))
			if ok != test.ok || text != test.text {
				t.Fatalf("decodeText = %q, %v; want %q, %v", text, ok, test.text, test.ok)
			}
			if !ok {
				return
			}
			back, err := encodeText(text, enc)
			if err != nil || string(back) != test.data {
				t.Fatalf("encodeText = %q, %v; want %q", back, err, test.data)
			}
		})
	}
}
