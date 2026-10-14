package imageinput

import (
	"io/fs"
	"path"
	"strings"
)

// DroppedPaths accepts escaped/quoted absolute paths and file:// URIs.
// Every token must name an existing image file; otherwise the paste is plain text.
//
// TODO: support Windows drive/UNC paths and WSL path translation.
func DroppedPaths(text string, stat func(string) (fs.FileInfo, error)) (paths []string, ok bool) {
	tokens, ok := splitShellWords(text)
	if !ok || len(tokens) == 0 {
		return nil, false
	}
	for _, token := range tokens {
		p, ok := droppedPath(token)
		if !ok || !hasImageExtension(p) {
			return nil, false
		}
		info, err := stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		paths = append(paths, p)
	}
	return paths, true
}

func hasImageExtension(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func droppedPath(token string) (string, bool) {
	if p, ok, err := FilePath(token); ok {
		return p, err == nil
	}
	return token, path.IsAbs(token)
}

// splitShellWords fails on unbalanced quotes or a trailing backslash.
func splitShellWords(text string) ([]string, bool) {
	var (
		words   []string
		word    strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range text {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				word.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, true
}
