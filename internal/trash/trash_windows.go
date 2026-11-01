package trash

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32                = windows.NewLazySystemDLL("shell32.dll")
	procSHFileOperationW   = shell32.NewProc("SHFileOperationW")
	procSHQueryRecycleBinW = shell32.NewProc("SHQueryRecycleBinW")
)

// shFileOpStruct is SHFILEOPSTRUCTW, as 64-bit Windows lays it out.
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// shQueryRBInfo is SHQUERYRBINFO.
type shQueryRBInfo struct {
	cbSize      uint32
	i64Size     int64
	i64NumItems int64
}

const (
	foDelete          = 0x0003
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

// move sends path to the Recycle Bin, as Explorer's delete does, without
// any of the shell's dialogs.
func move(path string) error {
	if err := canRecycle(path); err != nil {
		return err
	}
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list of paths ends with a second NUL
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return errors.New("the shell refused (code " + strconv.FormatUint(uint64(r), 10) + ")")
	}
	if op.fAnyOperationsAborted != 0 {
		return errors.New("cancelled")
	}
	return nil
}

// canRecycle says why path can't go to the Recycle Bin, if it can't. Where
// there is no Recycle Bin, a network share or a removable disk, or where it
// is turned off, FOF_ALLOWUNDO deletes for good without a word, so the
// file is left alone instead.
func canRecycle(path string) error {
	dir := filepath.Dir(path)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	d, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	buf := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(d, &buf[0], uint32(len(buf))); err != nil {
		return err
	}
	root, vol := &buf[0], windows.UTF16ToString(buf)
	info := shQueryRBInfo{cbSize: uint32(unsafe.Sizeof(shQueryRBInfo{}))}
	if r, _, _ := procSHQueryRecycleBinW.Call(uintptr(unsafe.Pointer(root)), uintptr(unsafe.Pointer(&info))); r != 0 {
		return errors.New(vol + " has no Recycle Bin")
	}
	off := errors.New("the Recycle Bin is turned off for " + vol)
	for _, k := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if regInt(k, `Software\Microsoft\Windows\CurrentVersion\Policies\Explorer`, "NoRecycleFiles") == 1 {
			return off
		}
	}
	guid := make([]uint16, 64)
	if windows.GetVolumeNameForVolumeMountPoint(root, &guid[0], uint32(len(guid))) == nil {
		// \\?\Volume{…}\ names the volume's settings key {…}.
		id := strings.TrimSuffix(strings.TrimPrefix(windows.UTF16ToString(guid), `\\?\Volume`), `\`)
		if regInt(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume\`+id, "NukeOnDelete") == 1 {
			return off
		}
	}
	return nil
}

// regInt is the DWORD value name under key\path, or -1.
func regInt(key registry.Key, path, name string) int64 {
	k, err := registry.OpenKey(key, path, registry.QUERY_VALUE)
	if err != nil {
		return -1
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return -1
	}
	return int64(v)
}
