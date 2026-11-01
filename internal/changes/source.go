package changes

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// Head is what a diff compares with the base.
type Head string

const (
	// HeadWorktree is the files on disk: what `dataform run` runs.
	HeadWorktree Head = "worktree"
	// HeadPushed is the branch's pushed commit: what an API run compiles.
	HeadPushed Head = "pushed"
)

// Request asks for the Changed Actions.
type Request struct {
	// DefaultBranch is the branch to compare against, e.g. main.
	DefaultBranch string `json:"defaultBranch"`
	Head          Head   `json:"head"`
	// Compile allows compiling a base (or pushed head) that isn't cached.
	// Without it the answer is nil until one has been compiled.
	Compile bool `json:"compile"`
}

// Result is the Changed Actions and what they were compared with.
type Result struct {
	BaseRef         string `json:"baseRef"`
	MergeBase       string `json:"mergeBase"`
	DefaultBranch   string `json:"defaultBranch"`
	OnDefaultBranch bool   `json:"onDefaultBranch,omitempty"`
	HeadRef         string `json:"headRef"`
	Head            Head   `json:"head"`
	HeadSHA         string `json:"headSha,omitempty"`
	Diff
	// HeadJSON is the compile JSON the base was compared with: the pushed
	// commit's for HeadPushed. It stays in the process that compiled it.
	HeadJSON []byte `json:"-"`
}

// Comparison is "in feat/orders vs origin/main", or "vs origin/main" when
// the compared branch is unknown.
func (r *Result) Comparison() string {
	if r.HeadRef == "" {
		return "vs " + r.BaseRef
	}
	return "in " + r.HeadRef + " vs " + r.BaseRef
}

// Title is the comparison with the merge-base and, for the pushed commit,
// which one: "in feat vs origin/main @ abc1234 (pushed commit def5678)".
func (r *Result) Title() string {
	t := r.Comparison() + " @ " + short(r.MergeBase)
	if r.Head == HeadPushed {
		t += " (pushed commit " + short(r.HeadSHA) + ")"
	} else {
		t += " (working tree)"
	}
	return t
}

// NoChangesMessage explains an empty result. On the default branch itself
// there is nothing to compare unless files were edited.
func (r *Result) NoChangesMessage() string {
	if r.OnDefaultBranch {
		return fmt.Sprintf("You're on %s, the branch Run Changed compares against, so there is nothing to compare. Switch to a feature branch, or edit files.", r.DefaultBranch)
	}
	return "No changed actions " + r.Comparison()
}

// Find returns the change to action id, if it changed.
func (r *Result) Find(id project.ActionID) (Changed, bool) {
	if r == nil {
		return Changed{}, false
	}
	for _, c := range r.Changed {
		if c.ID == id {
			return c, true
		}
	}
	return Changed{}, false
}

// maxCachedBases is how many base compiles are kept on disk per project.
const maxCachedBases = 5

// DefaultCompileTimeout bounds compiling one snapshot.
const DefaultCompileTimeout = 10 * time.Minute

// Source works out Changed Actions for one project, compiling bases from
// git snapshots and caching them in memory and on disk.
type Source struct {
	// Root is the project root: the folder with workflow_settings.yaml or
	// dbt_project.yml.
	Root string
	// Key is part of the cache key: the tool's binary, and its settings
	// that change a compile (dbt's target), since another CLI may compile
	// the same commit differently.
	Key string
	// CacheDir holds compiled bases; "" keeps them in memory only.
	CacheDir string
	// Compile compiles the project in dir; a base is its graph's JSON.
	Compile func(ctx context.Context, dir string) (*project.Graph, error)
	// Base, when set, builds a base from the snapshot in dir instead of
	// Compile: what is cached, and what Diff gets as base. An error says
	// what is wrong with the snapshot.
	Base func(ctx context.Context, dir string) ([]byte, error)
	// Diff compares a base with head, the working tree's graph JSON; nil
	// means Compare. A Diff that reads the working tree itself rather than
	// head (dbt's) can't compare the pushed commit, so with one set
	// HeadPushed is refused.
	Diff func(ctx context.Context, base, head []byte) (Diff, error)
	// Mirror are directories of Root mirrored into every snapshot when
	// Root has them: what the tool needs that git doesn't hold, such as
	// installed dbt packages.
	Mirror []string
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// Timeout bounds one snapshot compile; 0 means DefaultCompileTimeout.
	Timeout time.Duration

	mu       sync.Mutex
	mem      map[string][]byte
	failed   map[string]error
	inflight map[string]*flight
}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

// CacheDay keys bases by UTC day. Projects often compute values from the
// current date in includes/, so a commit compiled yesterday can differ from
// the same commit compiled today; keying by day keeps the base comparable
// with today's compile of the head.
func CacheDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Source) key(sha string) string {
	return sha + "__" + hash(s.Key) + "__" + CacheDay(s.now())
}

// projectDir is where this project's bases are cached.
func (s *Source) projectDir() string {
	return filepath.Join(s.CacheDir, hash(project.RealPath(s.Root)))
}

// Retry forgets failed compiles, so the next request tries them again. A
// failure is otherwise kept for the life of the Source.
func (s *Source) Retry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = nil
}

// Changes compares head, the compile JSON of the working tree, with the
// base req names. For HeadPushed, head is ignored and the pushed commit is
// compiled instead. Without req.Compile the result is nil (and no error)
// until what it needs has been compiled.
func (s *Source) Changes(ctx context.Context, req Request, head []byte) (*Result, error) {
	if req.Head == HeadPushed && s.Diff != nil {
		return nil, ErrWorktreeOnly
	}
	b, err := ResolveBase(ctx, s.Root, req.DefaultBranch, req.Head)
	if err != nil {
		return nil, err
	}
	if req.Head == HeadPushed {
		if head, err = s.graph(ctx, b.HeadSHA, req.Compile); err != nil || head == nil {
			return nil, err
		}
	}
	base, err := s.graph(ctx, b.MergeBase, req.Compile)
	if err != nil || base == nil {
		return nil, err
	}
	diff := s.Diff
	if diff == nil {
		diff = func(_ context.Context, base, head []byte) (Diff, error) { return Compare(base, head) }
	}
	d, err := diff(ctx, base, head)
	if err != nil {
		return nil, err
	}
	if hasRenameCandidates(d) {
		// Best effort: without it the renamed stay new and deleted.
		if renames, err := renamedFiles(ctx, s.Root, b.MergeBase, b.HeadSHA, newFiles(d)); err == nil {
			pairRenames(&d, renames)
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return &Result{
		BaseRef: b.BaseRef, MergeBase: b.MergeBase, DefaultBranch: b.DefaultBranch,
		OnDefaultBranch: b.OnDefaultBranch, HeadRef: b.HeadRef, Head: req.Head, HeadSHA: b.HeadSHA,
		Diff: d, HeadJSON: head,
	}, nil
}

// Commit is the compile JSON of the project at commit sha, compiled now
// unless it is cached.
func (s *Source) Commit(ctx context.Context, sha string) ([]byte, error) {
	return s.graph(ctx, sha, true)
}

// graph is the compile JSON of the project at commit sha: from memory, from
// disk, from a compile already under way, or, with compile set, compiled
// now. It is nil when it isn't cached and compile isn't set.
func (s *Source) graph(ctx context.Context, sha string, compile bool) ([]byte, error) {
	key := s.key(sha)
	s.mu.Lock()
	if data := s.mem[key]; data != nil {
		s.mu.Unlock()
		return data, nil
	}
	if err := s.failed[key]; err != nil {
		s.mu.Unlock()
		return nil, err
	}
	f := s.inflight[key]
	s.mu.Unlock()

	if f == nil {
		if data := s.readDisk(key); data != nil {
			s.remember(key, data)
			return data, nil
		}
		if !compile {
			return nil, nil
		}
		f = s.start(ctx, key, sha)
	}
	select {
	case <-f.done:
		return f.data, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// start compiles sha in the background, or joins the compile already
// started for key. The compile outlives ctx, so a caller that stops waiting
// still gets it cached.
func (s *Source) start(ctx context.Context, key, sha string) *flight {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.inflight[key]; f != nil {
		return f
	}
	if s.inflight == nil {
		s.inflight = map[string]*flight{}
	}
	f := &flight{done: make(chan struct{})}
	s.inflight[key] = f
	timeout := cmp.Or(s.Timeout, DefaultCompileTimeout)
	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		f.data, f.err = s.compileCommit(cctx, sha)
		s.mu.Lock()
		delete(s.inflight, key)
		if f.err != nil {
			if s.failed == nil {
				s.failed = map[string]error{}
			}
			s.failed[key] = f.err
		}
		s.mu.Unlock()
		if f.err == nil {
			s.remember(key, f.data)
			s.writeDisk(key, f.data)
		}
		close(f.done)
	}()
	return f
}

// compileCommit compiles the project as of sha in a snapshot, which it
// removes afterwards. A compile with errors is a failure: diffing a partial
// graph would call most actions new.
func (s *Source) compileCommit(ctx context.Context, sha string) ([]byte, error) {
	dir, err := snapshot(ctx, s.Root, sha, os.TempDir(), s.Mirror)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }() // links are removed, not what they point to
	if s.Base != nil {
		data, err := s.Base(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("could not compile %s:\n%w", short(sha), err)
		}
		return data, nil
	}
	g, err := s.Compile(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("could not compile %s: %w", short(sha), err)
	}
	if g.HasErrors() {
		var lines []string
		for _, e := range g.Errors[:min(len(g.Errors), 3)] {
			msg := firstLine(e.Message)
			if e.FileName != "" {
				msg = e.FileName + ": " + msg
			}
			lines = append(lines, msg)
		}
		if n := len(g.Errors) - 3; n > 0 {
			lines = append(lines, fmt.Sprintf("… and %d more", n))
		}
		return nil, fmt.Errorf("could not compile %s:\n%s", short(sha), strings.Join(lines, "\n"))
	}
	return g.JSON, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// remember keeps data in memory, with the few most recent others.
func (s *Source) remember(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mem == nil {
		s.mem = map[string][]byte{}
	}
	if len(s.mem) >= 4 {
		for k := range s.mem {
			delete(s.mem, k)
			break
		}
	}
	s.mem[key] = data
}

func (s *Source) diskPath(key string) string {
	return filepath.Join(s.projectDir(), key+".json")
}

func (s *Source) readDisk(key string) []byte {
	if s.CacheDir == "" {
		return nil
	}
	data, err := os.ReadFile(s.diskPath(key))
	if err != nil {
		return nil
	}
	return data
}

// writeDisk saves a compiled base and keeps only the newest few.
func (s *Source) writeDisk(key string, data []byte) {
	if s.CacheDir == "" {
		return
	}
	dir := s.projectDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp.Name(), s.diskPath(key)) != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	s.prune(dir)
}

func (s *Source) prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return b.mod.Compare(a.mod) })
	for _, f := range files[min(len(files), maxCachedBases):] {
		_ = os.Remove(f.path)
	}
}

// ErrWorktreeOnly is returned for HeadPushed by a Source whose Diff reads
// the working tree.
var ErrWorktreeOnly = errors.New("only the working tree can be compared")

// IsUnavailable reports errors that mean the project can't have Changed
// Actions at all (not a git repository), as opposed to a failure worth
// showing.
func IsUnavailable(err error) bool { return errors.Is(err, ErrNotGit) }
