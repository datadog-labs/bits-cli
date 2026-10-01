//go:build !linux && !darwin

package workspace

// resolveDefaultShellPath is empty on platforms without exec_command support.
func resolveDefaultShellPath() string { return "" }
