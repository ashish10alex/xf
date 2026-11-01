package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// listProcs lists every running process, from /proc.
func listProcs() ([]proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	var procs []proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue // gone already
		}
		// pid (comm) state ppid ...; comm may hold spaces and parentheses.
		open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
		if open < 0 || end < open {
			continue
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		p := proc{PID: pid, PPID: ppid, Name: string(stat[open+1 : end])}
		if fi, err := os.Stat(dir); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				p.Mine = st.Uid == uid
			}
		}
		if path, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			p.Path = strings.TrimSuffix(path, " (deleted)")
			p.Name = filepath.Base(p.Path)
		}
		if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(cmdline) > 0 {
			p.Args = strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
		}
		procs = append(procs, p)
	}
	return procs, nil
}
