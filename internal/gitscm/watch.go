package gitscm

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watched are the git directory's files whose change can change what
// Source Control shows: the index (a git add in a shell), HEAD and its log
// (a commit, reset or switch), the stash's log, the refs packed or
// fetched, config (upstream set on push -u), reftable's list, and the files
// of an operation in progress.
var watched = []string{"index", "HEAD", "logs/HEAD", "logs/refs/stash", "refs/stash", "packed-refs", "FETCH_HEAD", "ORIG_HEAD",
	"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "config", "reftable/tables.list"}

// watchedRefTrees are the subdirectories of .git/refs whose changes (branch
// moves, git push updating refs/remotes/*, tags) change the commit graph and
// upstream status. Other ref namespaces (such as tool checkpoints) are ignored.
var watchedRefTrees = []string{"refs/heads", "refs/remotes", "refs/tags"}

// maxWatchedRefDirs bounds how many ref subdirectories are added to fsnotify
// so a repository with thousands of nested loose branches cannot exhaust file
// descriptors.
const maxWatchedRefDirs = 256

// Watch reports changes to the watched files and ref trees, debounced into one
// notification, until ctx is done, when the channel is closed. Status
// reads with --no-optional-locks, so reading it after a notification
// doesn't rewrite the index and notify again.
func (r *Repo) Watch(ctx context.Context, debounce time.Duration) (<-chan struct{}, error) {
	args := []string{"rev-parse"}
	for _, f := range watched {
		args = append(args, "--git-path", f)
	}
	args = append(args, "--git-path", "refs")
	for _, d := range watchedRefTrees {
		args = append(args, "--git-path", d)
	}
	out, err := r.read(ctx, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < len(watched)+1+len(watchedRefTrees) {
		return nil, os.ErrNotExist
	}
	files, dirs := map[string]bool{}, map[string]bool{}
	for _, p := range lines[:len(watched)] {
		p = filepath.Clean(r.abs(p))
		files[p] = true
		dirs[filepath.Dir(p)] = true
	}
	// Watching refs itself lets us detect refs/remotes or refs/tags being
	// created later in a fresh repository.
	refsDir := filepath.Clean(r.abs(lines[len(watched)]))
	dirs[refsDir] = true
	refTrees := make([]string, 0, len(watchedRefTrees))
	for _, p := range lines[len(watched)+1 : len(watched)+1+len(watchedRefTrees)] {
		refTrees = append(refTrees, filepath.Clean(r.abs(p)))
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	addedDirs := map[string]bool{}
	addDir := func(d string) bool {
		if addedDirs[d] {
			return false
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return false
		}
		if w.Add(d) == nil {
			addedDirs[d] = true
			return true
		}
		return false
	}
	refDirCount := 0
	addRefTree := func(root string) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if refDirCount >= maxWatchedRefDirs {
				return filepath.SkipAll
			}
			if addDir(filepath.Clean(p)) {
				refDirCount++
			}
			return nil
		})
	}
	for d := range dirs {
		addDir(d)
	}
	for _, tree := range refTrees {
		addRefTree(tree)
	}
	if len(addedDirs) == 0 {
		_ = w.Close()
		return nil, os.ErrNotExist
	}
	inRefTrees := func(p string) bool {
		for _, tree := range refTrees {
			if p == tree || strings.HasPrefix(p, tree+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
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
				if ev.Op == fsnotify.Chmod {
					continue
				}
				name := filepath.Clean(ev.Name)
				if inRefTrees(name) {
					if ev.Has(fsnotify.Create) {
						if fi, err := os.Stat(name); err == nil && fi.IsDir() {
							addRefTree(name)
						}
					}
					timer.Reset(debounce)
					continue
				}
				// git writes index.lock and renames it to index.
				if files[name] || files[strings.TrimSuffix(name, ".lock")] {
					timer.Reset(debounce)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			case <-timer.C:
				select {
				case ch <- struct{}{}:
				default: // one is pending already
				}
			}
		}
	}()
	return ch, nil
}
