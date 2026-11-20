package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbeShellVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake shells are shell scripts")
	}
	for _, test := range []struct {
		name, shell, script string
		want                string
		wantErr             bool
	}{
		{name: "PowerShell 7", shell: "pwsh", script: "echo 7.4.6", want: "7.4"},
		{name: "Windows PowerShell", shell: "powershell", script: "echo 5.1.26100.2161", want: "5.1"},
		{name: "case-insensitive name", shell: "PowerShell.EXE", script: "echo 5.1.26100.2161", want: "5.1"},
		{name: "not PowerShell", shell: "bash", script: "exit 1"},
		{name: "probe fails", shell: "pwsh", script: "exit 1", wantErr: true},
		{name: "unexpected output", shell: "pwsh", script: "echo seven", wantErr: true},
		{name: "major only", shell: "pwsh", script: "echo 7", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := filepath.Join(t.TempDir(), test.shell)
			if err := os.WriteFile(shell, []byte("#!/bin/sh\n"+test.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			got, err := probeShellVersion(shell)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("probeShellVersion() = %q, %v; want %q, error %t", got, err, test.want, test.wantErr)
			}
		})
	}
}

// The real default shell reports its version only when it is PowerShell.
func TestDefaultShellVersion(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	got, err := ws.DefaultShellVersion()
	if err != nil {
		t.Fatal(err)
	}
	switch name := strings.ToLower(filepath.Base(ws.DefaultShellPath())); name {
	case "powershell.exe":
		if got != "5.1" {
			t.Fatalf("Windows PowerShell version = %q, want 5.1", got)
		}
	case "pwsh.exe":
		if !strings.HasPrefix(got, "7.") {
			t.Fatalf("PowerShell 7 version = %q, want 7.x", got)
		}
	default:
		if got != "" {
			t.Fatalf("%s version = %q, want none", name, got)
		}
	}
}
