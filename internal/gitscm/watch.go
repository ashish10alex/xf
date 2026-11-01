package gitscm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watched are the git directory's files whose change can change what
// Source Control shows: the index (a git add in a shell), HEAD and its log
// (a commit, reset or switch), the stash's log, the refs packed or
// fetched, and the files of an operation in progress.
var watched = []string{"index", "HEAD", "logs/HEAD", "logs/refs/stash", "refs/stash", "packed-refs", "FETCH_HEAD", "ORIG_HEAD",
	"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"}

// Watch reports changes to the watched files, debounced into one
// notification, until ctx is done, when the channel is closed. Status
// reads with --no-optional-locks, so reading it after a notification
// doesn't rewrite the index and notify again.
func (r *Repo) Watch(ctx context.Context, debounce time.Duration) (<-chan struct{}, error) {
	args := []string{"rev-parse"}
	for _, f := range watched {
		args = append(args, "--git-path", f)
	}
	out, err := r.read(ctx, args...)
	if err != nil {
		return nil, err
	}
	files, dirs := map[string]bool{}, map[string]bool{}
	for p := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		p = filepath.Clean(r.abs(p))
		files[p] = true
		dirs[filepath.Dir(p)] = true
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	added := 0
	for d := range dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue // logs/refs, before the first stash
		}
		if w.Add(d) == nil {
			added++
		}
	}
	if added == 0 {
		_ = w.Close()
		return nil, os.ErrNotExist
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
				// git writes index.lock and renames it to index.
				name := filepath.Clean(ev.Name)
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
