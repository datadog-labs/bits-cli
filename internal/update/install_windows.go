package update

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"
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
func activate(root, v string) error {
	current := filepath.Join(root, currentLink)
	if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return setJunction(current, filepath.Join(root, versionsDir, v))
}

// setJunction makes the directory link a junction to target. On a junction,
// it replaces the target in one step, so link never goes missing. The file
// system takes target as is, where cmd's mklink would expand %VARS% in it.
func setJunction(link, target string) error {
	substitute := utf16.Encode([]rune(`\??\` + target))
	display := utf16.Encode([]rune(target))
	// A mount point REPARSE_DATA_BUFFER: tag, data length, reserved, then the
	// offset and length in bytes of each name, which follow NUL-terminated.
	names := 2 * (len(substitute) + 1 + len(display) + 1)
	data := binary.LittleEndian.AppendUint32(nil, windows.IO_REPARSE_TAG_MOUNT_POINT)
	for _, n := range []int{8 + names, 0, 0, 2 * len(substitute), 2 * (len(substitute) + 1), 2 * len(display)} {
		data = binary.LittleEndian.AppendUint16(data, uint16(n))
	}
	for _, c := range slices.Concat(substitute, []uint16{0}, display, []uint16{0}) {
		data = binary.LittleEndian.AppendUint16(data, c)
	}
	if len(data) > windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE {
		return fmt.Errorf("%s is too long for a junction target", target)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("point %s at %s: %w", link, target, err)
	}
	return nil
}

// setUpCommand puts <root>\current\bin on the user PATH and says how to run
// bits.
func setUpCommand(root string, opts InstallOptions, out io.Writer) error {
	bin := filepath.Join(root, currentLink, "bin")
	added, err := addToUserPath(opts.UserEnvironment, bin)
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

// addToUserPath puts dir first on the Path of the HKCU environment key, so a
// bits from an earlier manual install does not shadow it. It keeps the value
// REG_EXPAND_SZ and unexpanded, as setx and
// [Environment]::SetEnvironmentVariable do not.
func addToUserPath(environment, dir string) (bool, error) {
	// An empty name opens HKCU itself, whose Path no one reads.
	if environment == "" {
		return false, errors.New("no user environment key")
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, environment, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer func() { _ = key.Close() }()
	path, _, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	// Entries name dir as written, as this function adds it, or through
	// variables, as %LOCALAPPDATA%\bits\current\bin.
	// NOTE: Windows also expands a %NAME% that dir itself holds, and the
	// value has no way to escape it, so new terminals would miss such a dir.
	for _, entry := range filepath.SplitList(path) {
		expanded, err := registry.ExpandString(entry)
		if err != nil {
			return false, err
		}
		for _, candidate := range []string{entry, expanded} {
			if strings.EqualFold(strings.TrimRight(candidate, `\`), dir) {
				return false, nil
			}
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
