package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/remote"
)

// thisBuild is the xf build this process runs (see remote.Build), read
// when it starts, before an upgrade can replace the executable; zero when
// it can't be read.
var thisBuild = readBuild()

// readBuild reads the build of the running executable.
func readBuild() remote.Build {
	exe, err := os.Executable()
	if err != nil {
		return remote.Build{}
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return remote.Build{}
	}
	return remote.Build{Time: fi.ModTime().UTC(), Size: fi.Size()}
}

// engineBinDir is where xf serve keeps the copies of xf it runs from on
// Windows: %LocalAppData%\xf\bin.
func engineBinDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "xf", "bin"), nil
}

// engineCopyPrefix starts the name of every copy of xf in engineBinDir.
const engineCopyPrefix = "xf-"

// copyEngine copies the executable exe into dir as xf-<hash>.exe, named by
// its content, and returns the copy's path. A copy that is already there
// is reused. The copy keeps exe's modification time, so it is the same
// build (see readBuild).
func copyEngine(exe, dir string) (string, error) {
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	fi, err := src.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, src); err != nil {
		return "", err
	}
	name := engineCopyPrefix + hex.EncodeToString(h.Sum(nil))[:16] + filepath.Ext(exe)
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	// Written under another name and renamed, so a copy under its final
	// name is always whole.
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime())
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
		if _, serr := os.Stat(dst); err != nil && serr == nil {
			err = nil // another xf serve made the same copy first
		}
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("copying %s to %s: %w", exe, dir, err)
	}
	return dst, nil
}

// cleanEngines removes every copy of xf in dir but keep. A copy that is
// still running can't be removed on Windows; it is left for a later
// clean-up.
func cleanEngines(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() || strings.EqualFold(p, keep) {
			continue
		}
		if strings.HasPrefix(e.Name(), engineCopyPrefix) {
			_ = os.Remove(p)
		}
	}
}
