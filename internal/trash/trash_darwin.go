package trash

import (
	"errors"
	"os"
	"path/filepath"
)

// move moves path into the user's Trash, as Finder would without "Put
// Back" (which needs Finder's own records, and asking Finder to delete
// needs the user's permission to control it). A file on another disk
// can't be moved there.
func move(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := freeName(filepath.Base(path), func(n string) bool {
		_, err := os.Lstat(filepath.Join(dir, n))
		return !errors.Is(err, os.ErrNotExist)
	})
	return os.Rename(path, filepath.Join(dir, name))
}
