package web

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// A file's base (see sourceView.Base) is read on every load of the source
// pane: opening the file, and again after each compile and window focus.
// Each read is a git process, so the bases of tracked files are kept for
// as long as the index they came from is the one on disk.
//
// No call here writes the index: git show and git check-ignore only read
// it, and --no-optional-locks says so to any git that would refresh it.
// An index.lock taken while Dataform copies the project stops the compile
// (see dataform.gitRace).

const (
	// gitBaseTimeout bounds reading a file's index version.
	gitBaseTimeout = 3 * time.Second
	// baseSettle is how old an index must be for its files' bases to be
	// kept. A newer one could be rewritten within its file system's
	// timestamp resolution, with the same size, and look unchanged.
	baseSettle = 2 * time.Second
	// maxBaseCache bounds the bytes of the bases kept.
	maxBaseCache = 16 << 20
)

// indexStamp is what an index file looks like on disk: another means it
// was rewritten. git always writes a new index and renames it over the old.
type indexStamp struct {
	size int64
	mod  time.Time
}

// baseCache is the tracked files' bases by index file, then by path.
var baseCache = struct {
	sync.Mutex
	byIndex map[string]*indexBases
	bytes   int
}{byIndex: map[string]*indexBases{}}

type indexBases struct {
	stamp indexStamp
	texts map[string]string
}

// gitBase is the file at path as git's index has it, with "\n" line ends
// and no byte-order mark as the pane's text has: see sourceView.Base.
func gitBase(path string) *string {
	dir, name := filepath.Dir(path), filepath.Base(path)
	index := indexOf(dir)
	before, stable := stampOf(index)
	if stable {
		if text, ok := cachedBase(index, before, path); ok {
			return &text
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitBaseTimeout)
	defer cancel()
	if out, err := cmdrun.Git(ctx, dir, "--no-optional-locks", "show", ":./"+name).Output(); err == nil {
		if len(out) > maxSourceBytes {
			return nil
		}
		text := (&sourceFile{raw: out, bom: bytes.HasPrefix(out, utf8BOM), crlf: bytes.Contains(out, []byte("\r\n"))}).text()
		if after, _ := stampOf(index); stable && after == before {
			keepBase(index, before, path, text)
		}
		return &text
	}
	// Not in the index: a file git doesn't track yet, unless git ignores
	// it (exit 0) or it isn't in a repository (exit 128).
	var exit *exec.ExitError
	if err := cmdrun.Git(ctx, dir, "--no-optional-locks", "check-ignore", "-q", "--", name).Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return nil
	}
	untracked := ""
	return &untracked
}

// indexOf is the index file of the repository dir is in, as git finds it:
// the nearest .git folder, or the git dir a .git file names (a linked
// worktree's, a submodule's). It is "" when there is none, or when the
// environment points git elsewhere.
func indexOf(dir string) string {
	for _, v := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES"} {
		if os.Getenv(v) != "" {
			return ""
		}
	}
	for d := dir; ; {
		dot := filepath.Join(d, ".git")
		if fi, err := os.Stat(dot); err == nil {
			if fi.IsDir() {
				return filepath.Join(dot, "index")
			}
			data, err := os.ReadFile(dot)
			if err != nil {
				return ""
			}
			gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
			if !ok {
				return ""
			}
			gitDir = filepath.FromSlash(strings.TrimSpace(gitDir))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(d, gitDir)
			}
			return filepath.Join(gitDir, "index")
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// stampOf is index's stamp, and whether its files' bases may be kept: it
// exists and is older than baseSettle.
func stampOf(index string) (indexStamp, bool) {
	if index == "" {
		return indexStamp{}, false
	}
	fi, err := os.Stat(index)
	if err != nil {
		return indexStamp{}, false
	}
	s := indexStamp{fi.Size(), fi.ModTime()}
	return s, time.Since(s.mod) >= baseSettle
}

// cachedBase is path's base kept from index when it is still at stamp.
func cachedBase(index string, stamp indexStamp, path string) (string, bool) {
	baseCache.Lock()
	defer baseCache.Unlock()
	b := baseCache.byIndex[index]
	if b == nil || b.stamp != stamp {
		return "", false
	}
	text, ok := b.texts[path]
	return text, ok
}

// keepBase keeps path's base, read from index at stamp. The bases of an
// index rewritten since go; past maxBaseCache, all of them do.
func keepBase(index string, stamp indexStamp, path, text string) {
	baseCache.Lock()
	defer baseCache.Unlock()
	b := baseCache.byIndex[index]
	if b != nil && b.stamp != stamp {
		dropBases(index)
		b = nil
	}
	if b == nil {
		b = &indexBases{stamp: stamp, texts: map[string]string{}}
		baseCache.byIndex[index] = b
	}
	if old, ok := b.texts[path]; ok {
		baseCache.bytes -= len(old)
		delete(b.texts, path)
	}
	if baseCache.bytes+len(text) > maxBaseCache {
		for i := range baseCache.byIndex {
			dropBases(i)
		}
		b = &indexBases{stamp: stamp, texts: map[string]string{}}
		baseCache.byIndex[index] = b
	}
	b.texts[path] = text
	baseCache.bytes += len(text)
}

// dropBases forgets index's bases. baseCache is locked.
func dropBases(index string) {
	b := baseCache.byIndex[index]
	if b == nil {
		return
	}
	for _, t := range b.texts {
		baseCache.bytes -= len(t)
	}
	delete(baseCache.byIndex, index)
}
