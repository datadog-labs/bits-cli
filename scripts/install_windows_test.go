package scripts

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// isolateUserPath gives the installer a scratch HKCU key to add its PATH entry
// to, so the test never changes the real user PATH.
func isolateUserPath(t *testing.T) {
	t.Helper()
	path := `Software\bits-cli-test-` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
			t.Errorf("delete %s: %v", path, err)
		}
	})
	t.Setenv("BITS_TEST_USER_ENVIRONMENT", path)
}

func TestInstallScriptUserPath(t *testing.T) {
	url := installerServer(t)
	for _, tc := range []struct {
		name         string
		value        string
		kind         uint32
		wantExisting bool
		alias        bool
	}{
		{name: "absent"},
		{name: "root reached through a junction", alias: true},
		{name: "expanded registry value", value: `C:\Tools;%SystemRoot%`, kind: registry.EXPAND_SZ},
		{name: "plain registry value", value: `C:\Tools;%SystemRoot%\literal`, kind: registry.SZ},
		{name: "existing variable", value: `C:\Tools;%BITS_TEST_ROOT%\current\bin\`, kind: registry.EXPAND_SZ, wantExisting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserPath(t)
			home := filepath.Join(t.TempDir(), "a&b (c) %PATH% [d]")
			physical := home
			if tc.alias {
				physical = filepath.Join(t.TempDir(), "actual")
				if err := os.MkdirAll(physical, 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BITS_TEST_ALIAS", home)
				t.Setenv("BITS_TEST_TARGET", physical)
				cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:BITS_TEST_ALIAS -Target $env:BITS_TEST_TARGET | Out-Null")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("create root alias: %v\n%s", err, out)
				}
			}

			root := filepath.Join(physical, "bits")
			t.Setenv("BITS_TEST_ROOT", root)
			key, err := registry.OpenKey(registry.CURRENT_USER, os.Getenv("BITS_TEST_USER_ENVIRONMENT"), registry.ALL_ACCESS)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := key.Close(); err != nil {
					t.Error(err)
				}
			}()
			switch tc.kind {
			case registry.SZ:
				err = key.SetStringValue("Path", tc.value)
			case registry.EXPAND_SZ:
				err = key.SetExpandStringValue("Path", tc.value)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if out, err := runInstaller(t, home, url, "v0.1.0"); err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
			}
			got, kind, err := key.GetStringValue("Path")
			bin := filepath.Join(root, "current", "bin")
			want := bin
			kindWant := tc.kind
			if kindWant == 0 {
				kindWant = registry.SZ
			}
			if kindWant == registry.EXPAND_SZ && strings.Contains(bin, "%") {
				want = "%BITS_INSTALL_BIN%"
			}
			if tc.value != "" {
				want += ";" + tc.value
			}
			if tc.wantExisting {
				want = tc.value
			}
			if err != nil || got != want || kind != kindWant {
				t.Fatalf("PATH = %q (%d), %v; want %q type %d", got, kind, err, want, kindWant)
			}
			if variable, _, err := key.GetStringValue("BITS_INSTALL_BIN"); err == nil {
				t.Setenv("BITS_INSTALL_BIN", variable)
			} else if err != registry.ErrNotExist {
				t.Fatal(err)
			}
			freshPath := got
			if kind == registry.EXPAND_SZ {
				freshPath, err = registry.ExpandString(got)
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$bits=Get-Command bits.exe -CommandType Application -ErrorAction Stop; & $bits.Source --version; exit $LASTEXITCODE")
			cmd.Env = append(os.Environ(), "PATH="+freshPath)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "bits version v0.1.0" {
				t.Fatalf("fresh PATH lookup failed: %v\n%s", err, out)
			}
		})
	}
}

func TestInstallScriptFirstActivation(t *testing.T) {
	url := installerServer(t)
	for _, content := range []bool{false, true} {
		t.Run(fmt.Sprint(content), func(t *testing.T) {
			isolateUserPath(t)
			home := t.TempDir()
			current := filepath.Join(home, "bits", "current")
			if err := os.MkdirAll(current, 0o755); err != nil {
				t.Fatal(err)
			}
			if content {
				if err := os.WriteFile(filepath.Join(current, "notes"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runInstaller(t, home, url, "v0.1.0")
			if (err != nil) != content {
				t.Fatalf("install = %v\n%s", err, out)
			}
			if content {
				if _, err := os.Stat(filepath.Join(current, "notes")); err != nil {
					t.Fatal(err)
				}
			} else {
				out, err := exec.Command(filepath.Join(current, "bin", "bits.exe"), "--version").CombinedOutput()
				if err != nil || strings.TrimSpace(string(out)) != "bits version v0.1.0" {
					t.Fatalf("current: %q, %v", out, err)
				}
			}
		})
	}
}
