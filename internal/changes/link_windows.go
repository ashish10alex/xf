//go:build windows

package changes

import (
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// mirrorLink recreates the link at path as to, in a mirrored tree. Windows
// only lets admins (or Developer Mode) create symlinks, so a link to a
// directory, a symlink or a junction (pnpm's, a workspace's), becomes a
// junction, and a link to a file becomes a hard link or a copy of it. A
// link within the tree points within the mirror, so a tool that resolves
// real paths still finds everything inside the project.
func mirrorLink(path, to string, d fs.DirEntry) (handled bool, err error) {
	// Go reports a junction as irregular, a symlink as a symlink.
	if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
		return false, nil
	}
	link, err := os.Readlink(path)
	if err != nil {
		return true, nil // not a link we can read: can't be required either
	}
	target := link
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return true, nil // dangling
	}
	if !info.IsDir() {
		if err := os.Link(target, to); err == nil {
			return true, nil
		}
		return true, copyFile(target, to)
	}
	// Point within the mirror when the link points within the tree.
	if !filepath.IsAbs(link) {
		target = filepath.Join(filepath.Dir(to), link)
	}
	return true, createJunction(to, target)
}

// createJunction makes link a junction (a directory mount point) to the
// directory target, which needs no privileges.
func createJunction(link, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		_ = os.Remove(link)
		return err
	}
	// A REPARSE_DATA_BUFFER for a mount point: the substitute name (the NT
	// path) and the print name, each NUL-terminated.
	sub := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))
	names := append(append(append(sub, 0), print...), 0)
	dataLen := 8 + 2*len(names)
	buf := make([]byte, 8+dataLen)
	binary.LittleEndian.PutUint32(buf[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buf[4:], uint16(dataLen))
	binary.LittleEndian.PutUint16(buf[8:], 0)                       // SubstituteNameOffset
	binary.LittleEndian.PutUint16(buf[10:], uint16(2*len(sub)))     // SubstituteNameLength
	binary.LittleEndian.PutUint16(buf[12:], uint16(2*(len(sub)+1))) // PrintNameOffset
	binary.LittleEndian.PutUint16(buf[14:], uint16(2*len(print)))   // PrintNameLength
	for i, c := range names {
		binary.LittleEndian.PutUint16(buf[16+2*i:], c)
	}
	var n uint32
	err = windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
	_ = windows.CloseHandle(h)
	if err != nil {
		_ = os.Remove(link)
	}
	return err
}
