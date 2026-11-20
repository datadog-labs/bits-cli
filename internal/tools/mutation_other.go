//go:build !windows

package tools

import (
	"io/fs"
	"os"
)

// keepAttributes has nothing to keep where permissions, which safeReplace
// restores, are a file's only attributes.
func keepAttributes(*os.File, fs.FileInfo) error { return nil }
