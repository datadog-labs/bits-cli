//go:build !windows

package fake

// held reports whether err means the continue file is still open in the
// process that touched it. Only Windows refuses to remove an open file.
func held(error) bool { return false }
