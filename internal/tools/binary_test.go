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
		{"long text no null", []byte(strings.Repeat("a", 10000)), false},
		{"null beyond probe limit", beyond, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.data); got != tt.want {
				t.Errorf("isBinary = %v, want %v", got, tt.want)
			}
		})
	}
}
