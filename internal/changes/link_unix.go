//go:build !windows

package changes

import (
	"io/fs"
	"os"
)

// mirrorLink recreates the link at path as to, in a mirrored tree:
// symlinks are kept as they are (pnpm's, node_modules/.bin's).
func mirrorLink(path, to string, d fs.DirEntry) (handled bool, err error) {
	if d.Type()&fs.ModeSymlink == 0 {
		return false, nil
	}
	link, err := os.Readlink(path)
	if err != nil {
		return true, nil // unreadable: can't be required either
	}
	return true, os.Symlink(link, to)
}
