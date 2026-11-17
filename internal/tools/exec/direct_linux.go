package exec

import "syscall"

// stopWithParent asks Linux to send the shell SIGTERM when Bits dies without
// stopping it, such as on SIGKILL, so the command does not outlive Bits.
//
// NOTE: only the shell gets the signal, which reaches the command itself when
// the shell execs it, but not other descendants. Linux sends it when the
// thread that started the shell exits; Go ends a thread only when a goroutine
// locked to it exits, which the launcher's goroutine never is.
func stopWithParent(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
