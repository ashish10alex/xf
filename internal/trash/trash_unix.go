//go:build !darwin && !windows

package trash

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// move moves path into the home trash of the freedesktop.org trash
// specification: the file under files/ and, under info/, where it was and
// when it was deleted, so a file manager can restore it. A file on another
// disk than the home trash can't be moved there.
func move(path string) error {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		data = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(data, "Trash")
	files, info := filepath.Join(dir, "files"), filepath.Join(dir, "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// The info file is made first, and exclusively: it claims the name.
	base := filepath.Base(path)
	var name string
	var f *os.File
	for i := 0; ; i++ {
		name = freeName(base, func(n string) bool {
			_, err := os.Lstat(filepath.Join(files, n))
			_, err2 := os.Lstat(filepath.Join(info, n+".trashinfo"))
			return !errors.Is(err, os.ErrNotExist) || !errors.Is(err2, os.ErrNotExist)
		})
		var err error
		f, err = os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return err
		}
	}
	u := (&url.URL{Path: path}).EscapedPath()
	_, err := f.WriteString("[Trash Info]\nPath=" + u + "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(path, filepath.Join(files, name))
	}
	if err != nil {
		_ = os.Remove(filepath.Join(info, name+".trashinfo"))
	}
	return err
}
