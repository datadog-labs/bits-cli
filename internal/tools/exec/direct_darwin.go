package exec

import "syscall"

// stopWithParent does nothing: macOS cannot signal a child when its parent
// dies, so a command outlives a Bits that is killed outright.
func stopWithParent(*syscall.SysProcAttr) {}
