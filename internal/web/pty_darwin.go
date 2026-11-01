package web

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a new pseudo-terminal: the master xf keeps and the name
// of the terminal the shell gets.
func openPTY() (*os.File, string, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	var name string
	var ierr error
	err = ptyControl(m, func(fd int) {
		if ierr = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); ierr != nil {
			return
		}
		if ierr = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); ierr != nil {
			return
		}
		buf := make([]byte, 128)
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
			ierr = e
			return
		}
		name = string(buf[:bytes.IndexByte(buf, 0)])
	})
	if err == nil {
		err = ierr
	}
	if err != nil {
		_ = m.Close()
		return nil, "", err
	}
	return m, name, nil
}

// procName is pid's command name, "" when it is gone.
func procName(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	b := make([]byte, 0, len(kp.Proc.P_comm))
	for _, c := range kp.Proc.P_comm {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
