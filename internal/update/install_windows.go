package update

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const executableName = "bits.exe"

// resolveExecutable follows the current junction to the version that runs.
// filepath.EvalSymlinks fails on any path through a junction since Go 1.23.
func resolveExecutable(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	file, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(file) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	// Flags 0 ask for a normalized path with a drive letter.
	n, err := windows.GetFinalPathNameByHandle(file, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
}

// activate points the current junction at versions\<v>. Junctions need no
// privilege, unlike directory symlinks, and a running bits.exe under the old
// target does not stop the switch.
// NOTE: a junction cannot be replaced atomically. An install interrupted
// between the two steps leaves no current; running the installer again
// restores it.
func activate(root, v string) error {
	current := filepath.Join(root, currentLink)
	if err := os.Remove(current); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Quoted for cmd, which reads & ^ ( ) in an unquoted path as syntax.
	// Windows paths cannot hold the quote itself.
	cmd := exec.Command("cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: fmt.Sprintf(`cmd /c mklink /J "%s" "%s"`, current, filepath.Join(root, versionsDir, v)),
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		// A concurrent install of the same version may have created it first.
		if active, readErr := activeVersion(root); readErr == nil && active == v {
			return nil
		}
		return fmt.Errorf("mklink /J: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

// setUpCommand puts <root>\current\bin on the user PATH and says how to run
// bits.
func setUpCommand(root string, opts InstallOptions, out io.Writer) error {
	bin := filepath.Join(root, currentLink, "bin")
	added, err := addToUserPath(bin)
	if err != nil {
		return fmt.Errorf("add %s to your PATH: %w", bin, err)
	}
	var advice []string
	if added {
		advice = append(advice, fmt.Sprintf("Added %s to your PATH. Restart open terminals and editors to use bits there.", bin))
		if err := broadcastEnvironmentChange(); err != nil {
			advice = append(advice, fmt.Sprintf("Warning: %v", err))
		}
	}
	command := filepath.Join(bin, executableName)
	switch first := firstOnPath(opts.Path, executableName); {
	case strings.EqualFold(first, command):
		if added {
			advice = append(advice, "Run bits to get started.")
		}
	case first != "":
		advice = append(advice, fmt.Sprintf("%s comes first on your PATH, so run %s to use this one.", first, command))
	case !added:
		advice = append(advice, "Open a new terminal to use bits.")
	}
	if len(advice) == 0 {
		return nil
	}
	_, err = io.WriteString(out, strings.Join(advice, "\n")+"\n")
	return err
}

// addToUserPath puts dir first on the user PATH, so a bits from an earlier
// manual install does not shadow it. It keeps the value REG_EXPAND_SZ and
// unexpanded, as setx and [Environment]::SetEnvironmentVariable do not.
func addToUserPath(dir string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer func() { _ = key.Close() }()
	path, _, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	// Entries can name dir through variables, as %LOCALAPPDATA%\bits\current\bin.
	for _, entry := range filepath.SplitList(path) {
		expanded, err := registry.ExpandString(entry)
		if err != nil {
			return false, err
		}
		if strings.EqualFold(strings.TrimRight(expanded, `\`), dir) {
			return false, nil
		}
	}
	if path != "" {
		dir += ";" + path
	}
	return true, key.SetExpandStringValue("Path", dir)
}

var sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvironmentChange tells Explorer to reload the environment, so
// terminals started from it afterwards see the new PATH.
func broadcastEnvironmentChange() error {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
		timeoutMillis   = 5000
	)
	environment, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	ok, _, err := sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(environment)), smtoAbortIfHung, timeoutMillis, 0)
	if ok == 0 {
		return fmt.Errorf("announce the new PATH: %w", err)
	}
	return nil
}
