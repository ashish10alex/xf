// Package watch reports changes to the files that affect a project's
// compilation. Each backend says which files those are with a Spec.
package watch

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Spec says which files under a project's root affect its compilation.
type Spec struct {
	// Dirs are the directories watched recursively, including directories
	// created later. Nil watches every directory at the root except Skip.
	Dirs []string
	// Skip are root directories left out when Dirs is nil, such as build
	// output.
	Skip []string
	// Exts are the file types under the directories that count.
	Exts map[string]bool
	// RootFiles are the files directly in the root that count.
	RootFiles []string
	// AnyFile counts every file, and every folder made or removed, under
	// the directories and in the root, whatever Exts and RootFiles say.
	AnyFile bool
}

// counts says whether a file called name, under the directories, counts.
func (s Spec) counts(name string) bool {
	return s.AnyFile || s.Exts[strings.ToLower(filepath.Ext(name))]
}

// Watch reports changes to the files spec names: anything with one of its
// extensions under its directories, plus its root files. Bursts of events,
// such as an editor's save dance of backup, write and rename, are debounced
// into one notification. The channel is closed when ctx is done.
func Watch(ctx context.Context, root string, spec Spec, debounce time.Duration) (<-chan struct{}, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(root); err != nil {
		_ = w.Close()
		return nil, err
	}
	for _, d := range spec.dirs(root) {
		if err := addTree(w, filepath.Join(root, d)); err != nil {
			_ = w.Close()
			return nil, err
		}
	}

	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		defer func() { _ = w.Close() }()
		timer := time.NewTimer(debounce)
		timer.Stop()
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Has(fsnotify.Create) && spec.isWatchedDir(root, ev.Name) {
					_ = addTree(w, ev.Name)
					// Files written before it was watched have no events.
					if spec.holdsWatchedFile(ev.Name) {
						timer.Reset(debounce)
					}
				}
				if spec.relevant(root, ev) {
					timer.Reset(debounce)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			case <-timer.C:
				select {
				case out <- struct{}{}:
				default: // a notification is already pending
				}
			}
		}
	}()
	return out, nil
}

// Walk visits every file under root that spec names, in a fixed order, with
// its path relative to root: the files a change to which Watch reports.
func (s Spec) Walk(root string, visit func(rel string, info fs.FileInfo)) error {
	for _, name := range s.RootFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			visit(name, info)
		}
	}
	dirs := slices.Clone(s.dirs(root))
	slices.Sort(dirs)
	for _, d := range dirs {
		top := filepath.Join(root, d)
		err := filepath.WalkDir(top, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			name := e.Name()
			if path != top && ignoredName(name) {
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if e.IsDir() || isEditorTempFile(name) || !s.counts(name) {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil // gone since it was listed
			}
			rel, _ := filepath.Rel(root, path)
			visit(rel, info)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// dirs are the root directories to watch.
func (s Spec) dirs(root string) []string {
	if s.Dirs != nil {
		return s.Dirs
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && s.watchesAll(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// watchesAll reports whether top, a root directory, is watched when Dirs
// is nil.
func (s Spec) watchesAll(top string) bool {
	return !ignoredName(top) && !slices.Contains(s.Skip, top)
}

// addTree watches dir and every directory below it. A missing dir is fine:
// not every project has includes/.
func addTree(w *fsnotify.Watcher, dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != dir && ignoredName(d.Name()) {
			return filepath.SkipDir
		}
		return w.Add(path)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// holdsWatchedFile reports whether dir, or a directory below it, holds a
// file with one of the spec's extensions.
func (s Spec) holdsWatchedFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case path != dir && ignoredName(d.Name()):
			if d.IsDir() {
				return filepath.SkipDir
			}
		case !d.IsDir() && !isEditorTempFile(d.Name()) && s.counts(d.Name()):
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// isWatchedDir reports whether path is a directory inside one of the
// watched directories.
func (s Spec) isWatchedDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	watched := slices.Contains(s.Dirs, top)
	if s.Dirs == nil {
		watched = s.watchesAll(top)
	}
	if !watched {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// relevant reports whether an event should trigger a recompile.
func (s Spec) relevant(root string, ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	base := filepath.Base(ev.Name)
	if samePath(filepath.Dir(ev.Name), filepath.Clean(root)) {
		if s.AnyFile {
			return base != ".git" && !isEditorTempFile(base) && !slices.Contains(s.Skip, base)
		}
		return slices.Contains(s.RootFiles, base)
	}
	if ignoredName(base) || isEditorTempFile(base) {
		return false
	}
	return s.counts(base)
}

func ignoredName(name string) bool {
	return name == "node_modules" || strings.HasPrefix(name, ".")
}

// isEditorTempFile matches files editors create while saving: vim's 4913
// write probe, backups ending in ~, and swap files.
func isEditorTempFile(name string) bool {
	switch {
	case name == "4913", strings.HasSuffix(name, "~"):
		return true
	case strings.HasSuffix(name, ".swp"), strings.HasSuffix(name, ".swx"), strings.HasSuffix(name, ".swo"):
		return true
	}
	return false
}

// samePath compares two cleaned paths as the filesystem does: ignoring
// case on Windows, where c:\proj and C:\Proj are one directory.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
