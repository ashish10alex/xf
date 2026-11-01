// Package trash moves files and folders to the system's trash, where they
// can be got back the usual way: the Trash on macOS, the Recycle Bin on
// Windows, and the freedesktop.org trash on Linux and the other Unixes.
package trash

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// Move moves the file or folder at path to the trash.
func Move(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return err
	}
	if err := move(abs); err != nil {
		return errors.New("can't move " + filepath.Base(abs) + " to the trash: " + err.Error())
	}
	return nil
}

// freeName is name, or name with " 2", " 3" … before its extension, that
// taken doesn't report as in use.
func freeName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	ext := filepath.Ext(name)
	stem := name[:len(name)-len(ext)]
	if stem == "" { // a dot file: .env
		stem, ext = name, ""
	}
	for i := 2; ; i++ {
		n := stem + " " + strconv.Itoa(i) + ext
		if !taken(n) {
			return n
		}
	}
}
