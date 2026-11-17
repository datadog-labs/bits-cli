package exec

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const parentDeathHelperEnv = "BITS_EXEC_TEST_PARENT_DEATH_PID_FILE"

// A command must not outlive a Bits killed outright, which cannot stop it.
func TestExecServiceLinuxCommandDiesWithBits(t *testing.T) {
	if pidFile := os.Getenv(parentDeathHelperEnv); pidFile != "" {
		// The helper plays Bits: it leaves a command running, then waits to
		// be killed.
		service := newUnixTestExecService(4096)
		service.Start(context.Background(), ExecRequest{Command: "echo $$ > " + pidFile + "; exec sleep 300", CWD: filepath.Dir(pidFile)}, 0)
		select {}
	}
	pidFile := filepath.Join(t.TempDir(), "command.pid")
	helper := osexec.Command(os.Args[0], "-test.run=^TestExecServiceLinuxCommandDiesWithBits$")
	helper.Env = append(os.Environ(), parentDeathHelperEnv+"="+pidFile)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	pid, err := waitForPIDFile(pidFile, 10*time.Second)
	if err != nil {
		_ = helper.Process.Kill()
		t.Fatal(err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := helper.Wait(); err == nil {
		t.Fatal("helper exited cleanly, want killed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("command %d outlived the killed Bits", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
