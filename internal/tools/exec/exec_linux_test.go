package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"syscall"
	"testing"
)

const parentDeathHelperEnv = "BITS_EXEC_TEST_PARENT_DEATH_DIR"

// A command must not outlive a Bits killed outright, which cannot stop it.
func TestExecServiceLinuxCommandDiesWithBits(t *testing.T) {
	if dir := os.Getenv(parentDeathHelperEnv); dir != "" {
		// The helper plays Bits: it leaves a command running, then waits to
		// be killed.
		service := newUnixTestExecService(4096)
		service.Start(context.Background(), ExecRequest{Command: "exec 3>watch; echo $$ >&3; exec sleep 300", CWD: dir}, 0)
		select {}
	}
	dir := t.TempDir()
	watch := watchProcesses(t, dir)
	helper := osexec.Command(os.Args[0], "-test.run=^TestExecServiceLinuxCommandDiesWithBits$")
	helper.Env = append(os.Environ(), parentDeathHelperEnv+"="+dir)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	pid := watch.pid(t)
	t.Cleanup(func() {
		select {
		case <-watch.closed: // exited: its pid may be reused
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := helper.Wait(); err == nil {
		t.Fatal("helper exited cleanly, want killed")
	}
	watch.exited(t)
}
