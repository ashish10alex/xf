package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// listProcs lists every running process.
func listProcs() ([]proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	procs := make([]proc, 0, len(kps))
	for _, k := range kps {
		p := proc{
			PID:  int(k.Proc.P_pid),
			PPID: int(k.Eproc.Ppid),
			Name: unix.ByteSliceToString(k.Proc.P_comm[:]),
			Mine: k.Eproc.Ucred.Uid == uid,
		}
		// The name is cut to 16 bytes; the executable's path isn't.
		if path, args, err := procArgs(p.PID); err == nil {
			p.Path, p.Args = path, args
			if path != "" {
				p.Name = filepath.Base(path)
			}
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// procArgs reads the executable path and arguments of process pid, which
// macOS gives only for the user's own processes: kern.procargs2 is argc,
// the path, NULs to pad it, then the arguments, each NUL-terminated.
func procArgs(pid int) (string, []string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, err
	}
	if len(buf) < 4 {
		return "", nil, errors.New("short procargs2")
	}
	argc := int(binary.LittleEndian.Uint32(buf))
	buf = buf[4:]
	path, buf, _ := bytes.Cut(buf, []byte{0})
	buf = bytes.TrimLeft(buf, "\x00")
	var args []string
	for len(args) < argc && len(buf) > 0 {
		var arg []byte
		arg, buf, _ = bytes.Cut(buf, []byte{0})
		args = append(args, string(arg))
	}
	return string(path), args, nil
}
