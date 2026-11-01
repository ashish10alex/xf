package main

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// listProcs lists every running process. Only the user's own have a path
// and a command line.
func listProcs() ([]proc, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var procs []proc
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := proc{
			PID:  int(e.ProcessID),
			PPID: int(e.ParentProcessID),
			Name: trimExe(windows.UTF16ToString(e.ExeFile[:])),
		}
		readProc(&p, me.User.Sid)
		procs = append(procs, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return procs, nil
}

func trimExe(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".exe") {
		return name[:len(name)-4]
	}
	return name
}

// readProc fills in whether p is the user's, as me, and if so its path and
// command line.
func readProc(p *proc, me *windows.SID) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) != nil {
		return
	}
	tu, err := tok.GetTokenUser()
	_ = tok.Close()
	if err != nil || !tu.User.Sid.Equals(me) {
		return
	}
	p.Mine = true
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		p.Path = windows.UTF16ToString(buf[:n])
	}
	if line, err := commandLine(h); err == nil {
		p.Args, _ = windows.DecomposeCommandLine(line)
	}
}

// commandLine reads the command line of the process h.
func commandLine(h windows.Handle) (string, error) {
	var n uint32
	_ = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, nil, 0, &n)
	if n < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) {
		return "", errors.New("no command line")
	}
	buf := make([]byte, n)
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), n, &n); err != nil {
		return "", err
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil {
		return "", nil
	}
	return windows.UTF16ToString(unsafe.Slice(us.Buffer, us.Length/2)), nil
}

// stopProc ends process pid, with its Job Objects' kill-on-close taking
// the runs it started with it. A detached engine has no console to be sent
// Ctrl+C on, and nothing of it needs saving.
func stopProc(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil // gone already
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.TerminateProcess(h, 1); err != nil {
		return err
	}
	_, err = windows.WaitForSingleObject(h, 5000)
	return err
}
