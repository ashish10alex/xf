package changes

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// rename is a file git calls renamed: its path at the merge-base, and how
// alike the two are, in percent.
type rename struct {
	from  string
	score int
}

// renamedFiles maps each file of the project in dir that git calls renamed
// since mergeBase to its old path, both relative to dir with forward
// slashes. It compares with headSHA, or with the working tree when that is
// "". The working tree is compared through a copy of the index: git diff
// rewrites the index it reads when files were saved since (whatever
// --no-optional-locks says), and the index.lock that takes can stop a
// Dataform compile copying the project. The files of added that git
// doesn't track yet count too: they are added with intent-to-add to that
// copy.
func renamedFiles(ctx context.Context, dir, mergeBase, headSHA string, added []string) (map[string]rename, error) {
	args := []string{"diff", "-M", "--diff-filter=R", "--name-status", "-z", "--relative", mergeBase}
	var env []string
	if headSHA != "" {
		args = append(args, headSHA)
	} else {
		index, cleanup, err := indexWith(ctx, dir, added)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		env = []string{"GIT_INDEX_FILE=" + index}
	}
	cmd := cmdrun.Git(ctx, dir, append(args, "--", ".")...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseRenames(string(out)), nil
}

// parseRenames reads git diff --name-status -z output: "R095", the old
// path and the new one, each ended by a NUL. Other entries are skipped.
func parseRenames(out string) map[string]rename {
	renames := map[string]rename{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		status := fields[i]
		if !strings.HasPrefix(status, "R") || i+2 >= len(fields) {
			continue
		}
		score, _ := strconv.Atoi(status[1:])
		renames[fields[i+2]] = rename{from: fields[i+1], score: score}
		i += 2
	}
	return renames
}

// indexWith is a copy of dir's index with the files of added that git
// doesn't track marked intent-to-add, and a func that removes it.
func indexWith(ctx context.Context, dir string, added []string) (string, func(), error) {
	none := func() {}
	var untracked []string
	if len(added) > 0 {
		out, err := git(ctx, dir, append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, added...)...)
		if err != nil {
			return "", none, err
		}
		for _, f := range strings.Split(out, "\x00") {
			if f != "" {
				untracked = append(untracked, f)
			}
		}
	}
	index, err := git(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", none, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	data, err := os.ReadFile(index)
	missing := errors.Is(err, fs.ErrNotExist) // git reads no index as an empty one
	if err != nil && !missing {
		return "", none, err
	}
	tmp, err := os.MkdirTemp("", "xf-index-")
	if err != nil {
		return "", none, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	copied := filepath.Join(tmp, "index")
	if !missing {
		if err := os.WriteFile(copied, data, 0o600); err != nil {
			cleanup()
			return "", none, err
		}
	}
	if len(untracked) == 0 {
		return copied, cleanup, nil
	}
	cmd := cmdrun.Git(ctx, dir, append([]string{"add", "--intent-to-add", "--"}, untracked...)...)
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+copied)
	if _, err := cmd.Output(); err != nil {
		cleanup()
		return "", none, err
	}
	return copied, cleanup, nil
}

// pairRenames marks the actions of d that git calls renamed: when a file
// renamed since the base has exactly one new action and its old path
// exactly one deleted action, neither generated from another action, the
// new one was the old one, under the name of its new file.
func pairRenames(d *Diff, renames map[string]rename) {
	if len(renames) == 0 {
		return
	}
	newIn := map[string][]int{}
	for i, c := range d.Changed {
		if c.Parent == "" && c.IsNew() {
			newIn[c.FileName] = append(newIn[c.FileName], i)
		}
	}
	deletedIn := map[string][]int{}
	for i, x := range d.Deleted {
		if x.Parent == "" {
			deletedIn[x.FileName] = append(deletedIn[x.FileName], i)
		}
	}
	for file, r := range renames {
		added, removed := newIn[file], deletedIn[r.from]
		if len(added) != 1 || len(removed) != 1 {
			continue
		}
		c, x := &d.Changed[added[0]], &d.Deleted[removed[0]]
		c.RenamedFrom, c.RenamedFromFile, c.Similarity = x.ID, x.FileName, r.score
		x.RenamedTo = c.ID
	}
}

// hasRenameCandidates says whether d has a new action and a deleted one,
// so that some may be a rename.
func hasRenameCandidates(d Diff) bool {
	if len(d.Deleted) == 0 {
		return false
	}
	for _, c := range d.Changed {
		if c.Parent == "" && c.IsNew() {
			return true
		}
	}
	return false
}

// newFiles are the files of d's new actions.
func newFiles(d Diff) []string {
	seen := map[string]bool{}
	var files []string
	for _, c := range d.Changed {
		if c.IsNew() && !seen[c.FileName] && c.FileName != "" {
			seen[c.FileName] = true
			files = append(files, c.FileName)
		}
	}
	return files
}
