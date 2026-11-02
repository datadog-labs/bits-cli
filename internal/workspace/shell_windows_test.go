//go:build windows

package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolvePowerShell(t *testing.T) {
	pwsh := filepath.Join(`C:\Program Files`, "PowerShell", "7", "pwsh.exe")
	windowsPowerShell := filepath.Join(`C:\Windows`, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	for _, test := range []struct {
		name      string
		available map[string]string
		want      string
	}{
		{name: "PowerShell 7 preferred", available: map[string]string{"pwsh.exe": pwsh, "powershell.exe": windowsPowerShell, "bash.exe": `C:\Program Files\Git\bin\bash.exe`}, want: pwsh},
		{name: "Windows PowerShell fallback", available: map[string]string{"powershell.exe": windowsPowerShell}, want: windowsPowerShell},
		{name: "Bash does not replace PowerShell", available: map[string]string{"bash.exe": `C:\Program Files\Git\bin\bash.exe`}},
		{name: "nothing available"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookPath := func(file string) (string, error) {
				if resolved, ok := test.available[file]; ok {
					return resolved, nil
				}
				return "", errors.New("not found")
			}
			if got := resolvePowerShell(lookPath); got != test.want {
				t.Fatalf("resolvePowerShell() = %q, want %q", got, test.want)
			}
		})
	}
}
