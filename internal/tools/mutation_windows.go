package tools

import (
	"io/fs"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// keptAttributes are the Windows attributes a replacement takes over from the
// file it replaces; the read-only bit follows the permissions instead.
const keptAttributes = windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM |
	windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED

// fileBasicInfo is the Windows FILE_BASIC_INFO structure. Zero times leave
// the file's times unchanged.
type fileBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32
}

// keepAttributes gives f, the new file that replaces target, target's hidden
// and system attributes, which a newly created file lacks. It sets them
// through f's handle, so no path is looked up outside the workspace root.
func keepAttributes(f *os.File, target fs.FileInfo) error {
	data, ok := target.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data.FileAttributes&keptAttributes == 0 {
		return nil
	}
	info := fileBasicInfo{FileAttributes: data.FileAttributes & keptAttributes}
	if err := windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return &fs.PathError{Op: "setattributes", Path: f.Name(), Err: err}
	}
	return nil
}
