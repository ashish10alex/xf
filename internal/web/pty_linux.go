package web

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// openPTY opens a new pseudo-terminal: the master xf keeps and the name
// of the terminal the shell gets.
func openPTY() (*os.File, string, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	var n uint32
	var ierr error
	err = ptyControl(m, func(fd int) {
		if ierr = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); ierr != nil {
			return
		}
		n, ierr = unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	})
	if err == nil {
		err = ierr
	}
	if err != nil {
		_ = m.Close()
		return nil, "", err
	}
	return m, "/dev/pts/" + strconv.FormatUint(uint64(n), 10), nil
}

// procName is pid's command name, "" when it is gone.
func procName(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
