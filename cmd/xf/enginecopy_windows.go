package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// serveFromCopy keeps xf serve from holding the installed xf.exe open,
// which would make upgrades fail: unless this xf is already a copy in
// engineBinDir, it starts one there (see copyEngine) with args, detached
// like itself, and reports true without waiting for it. A copy cleans up
// older copies as it starts. When a copy can't be made, xf serve runs from
// here and logs why.
func serveFromCopy(args []string, logger *log.Logger) bool {
	exe, err := os.Executable()
	if err != nil {
		logger.Printf("serving from xf.exe, which can't be replaced while it runs: %v", err)
		return false
	}
	dir, err := engineBinDir()
	if err != nil {
		logger.Printf("serving from %s, which can't be replaced while it runs: %v", exe, err)
		return false
	}
	// Either test alone would do; both, so a copy never starts another.
	if strings.EqualFold(filepath.Dir(exe), dir) || strings.HasPrefix(filepath.Base(exe), engineCopyPrefix) {
		cleanEngines(dir, exe)
		return false
	}
	cp, err := copyEngine(exe, dir)
	if err != nil {
		logger.Printf("serving from %s, which can't be replaced while it runs: %v", exe, err)
		return false
	}
	cmd := exec.Command(cp, args...)
	// The log file this xf serve was given, if any.
	if os.Stdout != nil {
		cmd.Stdout = os.Stdout
	}
	if os.Stderr != nil {
		cmd.Stderr = os.Stderr
	}
	cmd, err = startDetached(cmd)
	if err != nil {
		logger.Printf("serving from %s, as starting %s failed: %v", exe, cp, err)
		return false
	}
	_ = cmd.Process.Release()
	return true
}
