package imageinput

import (
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

func TestRecognizingFilesDroppedOnTheTerminal(t *testing.T) {
	fsys := fstest.MapFS{
		"Users/me/shot.png":    {},
		"Users/me/My Shot.png": {},
		"Users/me/b.JPEG":      {},
		"Users/me/it's.gif":    {},
		"Users/me/notes.txt":   {},
		"Users/me/dir.png":     {Mode: fs.ModeDir},
	}
	stat := func(name string) (fs.FileInfo, error) {
		return fs.Stat(fsys, name[1:])
	}
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "a plain path", text: "/Users/me/shot.png", want: []string{"/Users/me/shot.png"}},
		{name: "a path with escaped spaces", text: `/Users/me/My\ Shot.png`, want: []string{"/Users/me/My Shot.png"}},
		{name: "several paths and a trailing newline", text: "  /Users/me/shot.png /Users/me/b.JPEG\n", want: []string{"/Users/me/shot.png", "/Users/me/b.JPEG"}},
		{name: "a single-quoted path", text: `'/Users/me/My Shot.png'`, want: []string{"/Users/me/My Shot.png"}},
		{name: "double-quoted paths", text: `"/Users/me/My Shot.png" "/Users/me/it's.gif"`, want: []string{"/Users/me/My Shot.png", "/Users/me/it's.gif"}},
		{name: "file URIs", text: "file:///Users/me/My%20Shot.png\r\nfile://localhost/Users/me/shot.png", want: []string{"/Users/me/My Shot.png", "/Users/me/shot.png"}},
		{name: "nothing but whitespace", text: " \n"},
		{name: "a relative path", text: "me/shot.png"},
		{name: "a sentence mentioning a path", text: "look at /Users/me/shot.png"},
		{name: "a missing file", text: "/Users/me/gone.png"},
		{name: "a directory", text: "/Users/me/dir.png"},
		{name: "a file that is not an image", text: "/Users/me/notes.txt"},
		{name: "one non-image among images", text: "/Users/me/shot.png /Users/me/notes.txt"},
		{name: "a file URI on another host", text: "file://host/Users/me/shot.png"},
		{name: "an unterminated quote", text: `'/Users/me/shot.png`},
		{name: "a Windows path", text: `C:\Users\me\shot.png`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DroppedPaths(tt.text, stat)
			if ok != (tt.want != nil) || !slices.Equal(got, tt.want) {
				t.Fatalf("DroppedPaths(%q) = %q, %v; want %q", tt.text, got, ok, tt.want)
			}
		})
	}
}
