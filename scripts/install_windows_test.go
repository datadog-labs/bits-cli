package scripts

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// isolateUserPath gives the installer a scratch HKCU key to add its PATH entry
// to, so the test never changes the real user PATH.
func isolateUserPath(t *testing.T) {
	t.Helper()
	path := `Software\bits-cli-test-` + rand.Text()
	t.Cleanup(func() {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && err != registry.ErrNotExist {
			t.Errorf("delete %s: %v", path, err)
		}
	})
	t.Setenv("BITS_TEST_USER_ENVIRONMENT", path)
}

func installerArch(t *testing.T) string {
	t.Helper()
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	arch, _, readErr := key.GetStringValue("PROCESSOR_ARCHITECTURE")
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return strings.ToLower(arch)
}

func resetInstallCommand(t *testing.T, _ string) string {
	t.Helper()
	isolateUserPath(t)
	return "Added "
}

func TestInstallScriptUserPath(t *testing.T) {
	url := installerServer(t)
	for _, tc := range []struct {
		name         string
		value        string
		kind         uint32
		wantExisting bool
		alias        bool
		separator    bool
	}{
		{name: "absent"},
		{name: "path separator", separator: true},
		{name: "root reached through a junction", alias: true},
		{name: "expanded registry value", value: `C:\Tools;%SystemRoot%`, kind: registry.EXPAND_SZ},
		{name: "plain registry value", value: `C:\Tools;%SystemRoot%\literal`, kind: registry.SZ},
		{name: "existing variable", value: `C:\Tools;%BITS_TEST_ROOT%\current\bin\`, kind: registry.EXPAND_SZ, wantExisting: true, alias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserPath(t)
			home := filepath.Join(t.TempDir(), "a&b (c) %PATH% [d]")
			if tc.separator {
				home += ";separator"
			}
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
			t.Setenv("BITS_TEST_ROOT", filepath.Join(home, "bits"))
			keyPath := os.Getenv("BITS_TEST_USER_ENVIRONMENT")
			if tc.kind != 0 {
				key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.ALL_ACCESS)
				if err != nil {
					t.Fatal(err)
				}
				if tc.kind == registry.EXPAND_SZ {
					err = key.SetExpandStringValue("Path", tc.value)
				} else {
					err = key.SetStringValue("Path", tc.value)
				}
				if err != nil {
					t.Error(err)
				}
				if err := key.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				out, err := runInstaller(t, home, url, "v0.1.0")
				if err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
				if tc.separator && !strings.Contains(out, "contains a PATH separator") {
					t.Fatalf("install did not explain why PATH was skipped:\n%s", out)
				}
			}
			key, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
			if tc.separator && err == registry.ErrNotExist {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := key.Close(); err != nil {
					t.Error(err)
				}
			}()
			if tc.separator {
				t.Fatal("install created a registry key for an unusable PATH entry")
			}
			got, kind, pathErr := key.GetStringValue("Path")
			physicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			short, err := windows.UTF16PtrFromString(physicalRoot)
			if err != nil {
				t.Fatal(err)
			}
			long := make([]uint16, windows.MAX_LONG_PATH)
			n, err := windows.GetLongPathName(short, &long[0], uint32(len(long)))
			if err != nil || n == 0 || int(n) >= len(long) {
				t.Fatalf("resolve long path for %s: length %d, %v", physicalRoot, n, err)
			}
			bin := filepath.Join(windows.UTF16ToString(long[:n]), "current", "bin")
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
			if pathErr != nil || got != want || kind != kindWant {
				t.Fatalf("PATH = %q (%d), %v; want %q type %d", got, kind, pathErr, want, kindWant)
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
