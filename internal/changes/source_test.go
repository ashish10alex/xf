package changes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/testutil"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// repo is a git repository whose Dataform project lives in a subfolder, as
// in a monorepo, so snapshots must hold only that folder.
type repo struct {
	t    *testing.T
	dir  string // repository root
	root string // project root
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	r := &repo{t: t, dir: dir, root: filepath.Join(dir, "dataform")}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.write("workflow_settings.yaml", "defaultProject: proj\n")
	r.write("definitions/orders.sql", "select 1")
	r.write("definitions/customers.sql", "select 2")
	r.write("../README.md", "outside the project")
	r.commit("base")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", r.dir}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
}

// fakeCompiler compiles a project by making a table of every
// definitions/*.sql file, whose query is the file's content. It refuses a
// snapshot holding files from outside the project.
type fakeCompiler struct {
	mu   sync.Mutex
	dirs []string
	fail error
}

func (f *fakeCompiler) compile(_ context.Context, dir string) (*project.Graph, error) {
	f.mu.Lock()
	f.dirs = append(f.dirs, dir)
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		return nil, errors.New("snapshot has files from outside the project")
	}
	if _, err := os.Stat(filepath.Join(dir, "workflow_settings.yaml")); err != nil {
		return nil, errors.New("snapshot has no workflow_settings.yaml")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "definitions", "*.sql"))
	var tables []map[string]any
	var compErrs []map[string]any
	for _, file := range files {
		data, _ := os.ReadFile(file)
		name := strings.TrimSuffix(filepath.Base(file), ".sql")
		if strings.Contains(string(data), "syntax error") {
			compErrs = append(compErrs, map[string]any{"fileName": "definitions/" + name + ".sql", "message": "Unexpected token"})
		}
		tables = append(tables, map[string]any{
			"type": "table", "fileName": "definitions/" + name + ".sql",
			"target": map[string]any{"database": "proj", "schema": "ds", "name": name},
			"query":  string(data),
		})
	}
	out := map[string]any{"tables": tables}
	if compErrs != nil {
		out["graphErrors"] = map[string]any{"compilationErrors": compErrs}
	}
	data, _ := json.Marshal(out)
	return dataform.ParseCompileJSON(data)
}

func (f *fakeCompiler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dirs)
}

func (r *repo) source(fc *fakeCompiler, cacheDir string) *Source {
	return &Source{Root: r.root, Key: "/bin/dataform", CacheDir: cacheDir, Compile: fc.compile,
		Now: func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }}
}

// head compiles the working tree as the engine would.
func (r *repo) head(fc *fakeCompiler) []byte {
	r.t.Helper()
	g, err := fc.compile(context.Background(), r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	return g.JSON
}

func ids(res *Result) []string {
	var out []string
	for _, c := range res.Changed {
		out = append(out, string(c.ID)+":"+c.ReasonsText())
	}
	return out
}

func TestChangesOnFeatureBranch(t *testing.T) {
	r := newRepo(t)
	r.git("checkout", "-q", "-b", "feat/orders")
	r.write("definitions/orders.sql", "select 10")
	r.write("definitions/returns.sql", "select 3")
	r.commit("change orders")
	r.write("definitions/customers.sql", "select 20") // uncommitted: in the working tree
	if err := os.Remove(filepath.Join(r.root, "definitions/returns.sql")); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCompiler{}
	s := r.source(fc, t.TempDir())
	ctx := context.Background()
	req := Request{DefaultBranch: "main", Head: HeadWorktree}

	// Nothing is compiled without asking.
	res, err := s.Changes(ctx, req, r.head(fc))
	if err != nil || res != nil {
		t.Fatalf("uncompiled base: got %v, %v; want nil, nil", res, err)
	}
	req.Compile = true
	res, err = s.Changes(ctx, req, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"proj.ds.customers:sql", "proj.ds.orders:sql"}
	if got := ids(res); !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
	if res.BaseRef != "main" || res.HeadRef != "feat/orders" || res.OnDefaultBranch || res.Head != HeadWorktree {
		t.Errorf("result = %+v", res)
	}
	if res.MergeBase != r.git("rev-parse", "main") {
		t.Errorf("merge-base = %s, want main's commit", res.MergeBase)
	}
	if got := res.Comparison(); got != "in feat/orders vs main" {
		t.Errorf("comparison = %q", got)
	}

	// The base is cached: later diffs compile nothing, even without Compile.
	compiles := fc.count()
	req.Compile = false
	if res, err = s.Changes(ctx, req, r.head(fc)); err != nil || res == nil {
		t.Fatalf("cached base: %v, %v", res, err)
	}
	// A new Source (a new xf serve) finds it on disk.
	s2 := r.source(fc, s.CacheDir)
	if res, err = s2.Changes(ctx, req, r.head(fc)); err != nil || res == nil {
		t.Fatalf("base on disk: %v, %v", res, err)
	}
	if n := fc.count() - compiles; n != 2 { // just the two head compiles above
		t.Errorf("compiled %d times after caching, want only the heads", n)
	}
}

func TestChangesPreferOriginAndPushedCommit(t *testing.T) {
	r := newRepo(t)
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	r.git("remote", "add", "origin", remote)
	r.git("push", "-q", "origin", "main")
	// Local main moves on without being pushed: origin/main is the base.
	r.write("definitions/customers.sql", "select 99")
	r.commit("unpushed main")
	r.git("checkout", "-q", "-b", "feat", "origin/main")
	r.write("definitions/orders.sql", "select 10")
	r.commit("pushed change")

	fc := &fakeCompiler{}
	s := r.source(fc, "")
	ctx := context.Background()
	_, err := s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadPushed, Compile: true}, nil)
	if !errors.Is(err, ErrNotPushed) {
		t.Fatalf("unpushed branch: err = %v, want ErrNotPushed", err)
	}
	r.git("push", "-q", "origin", "feat")
	r.write("definitions/orders.sql", "select 11") // not pushed: an API run doesn't see it
	r.write("definitions/customers.sql", "select 12")
	r.commit("local only")

	res, err := s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadPushed, Compile: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(res), []string{"proj.ds.orders:sql"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pushed changes = %v, want %v", got, want)
	}
	if res.BaseRef != "origin/main" || res.HeadRef != "origin/feat" || res.HeadSHA != r.git("rev-parse", "origin/feat") {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(res.Title(), "(pushed commit ") {
		t.Errorf("title = %q", res.Title())
	}

	res, err = s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(res), []string{"proj.ds.customers:sql", "proj.ds.orders:sql"}; !reflect.DeepEqual(got, want) {
		t.Errorf("working tree changes = %v, want %v", got, want)
	}
}

func TestChangesOnDefaultBranch(t *testing.T) {
	r := newRepo(t)
	fc := &fakeCompiler{}
	res, err := r.source(fc, "").Changes(context.Background(), Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OnDefaultBranch || len(res.Changed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if msg := res.NoChangesMessage(); !strings.HasPrefix(msg, "You're on main, the branch Run Changed compares against") {
		t.Errorf("message = %q", msg)
	}
	res.OnDefaultBranch, res.HeadRef, res.BaseRef = false, "feat/orders", "origin/main"
	if msg := res.NoChangesMessage(); msg != "No changed actions in feat/orders vs origin/main" {
		t.Errorf("message = %q", msg)
	}
}

func TestChangesErrors(t *testing.T) {
	ctx := context.Background()
	fc := &fakeCompiler{}
	notGit := &Source{Root: t.TempDir(), Compile: fc.compile}
	if _, err := notGit.Changes(ctx, Request{DefaultBranch: "main", Head: HeadWorktree}, nil); !IsUnavailable(err) {
		t.Errorf("not a repo: err = %v", err)
	}

	r := newRepo(t)
	_, err := r.source(fc, "").Changes(ctx, Request{DefaultBranch: "develop", Head: HeadWorktree, Compile: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "neither origin/develop nor develop exists") {
		t.Errorf("missing branch: err = %v", err)
	}
}

func TestChangesBaseFailsToCompile(t *testing.T) {
	r := newRepo(t)
	r.write("definitions/orders.sql", "syntax error")
	r.commit("broken main")
	r.git("checkout", "-q", "-b", "feat")
	r.write("definitions/orders.sql", "select 1")

	fc := &fakeCompiler{}
	s := r.source(fc, t.TempDir())
	req := Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}
	_, err := s.Changes(context.Background(), req, r.head(fc))
	if err == nil || !strings.Contains(err.Error(), "could not compile") || !strings.Contains(err.Error(), "definitions/orders.sql: Unexpected token") {
		t.Fatalf("err = %v", err)
	}
	// The failure is kept: asking again doesn't compile again.
	n := fc.count()
	if _, err := s.Changes(context.Background(), req, nil); err == nil {
		t.Fatal("want the kept failure")
	}
	if fc.count() != n {
		t.Errorf("compiled again after a failure")
	}
	// Retry (c) tries again.
	s.Retry()
	if _, err := s.Changes(context.Background(), req, nil); err == nil {
		t.Fatal("still broken: want an error")
	}
	if fc.count() != n+1 {
		t.Errorf("Retry didn't compile again")
	}
	if entries, _ := os.ReadDir(s.projectDir()); len(entries) != 0 {
		t.Errorf("a failed base was cached on disk: %v", entries)
	}
}

func TestChangesCompileOutlivesCaller(t *testing.T) {
	r := newRepo(t)
	r.git("checkout", "-q", "-b", "feat")
	started, release := make(chan struct{}), make(chan struct{})
	fc := &fakeCompiler{}
	s := r.source(fc, "")
	s.Compile = func(ctx context.Context, dir string) (*project.Graph, error) {
		close(started)
		<-release
		return fc.compile(ctx, dir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}
	errc := make(chan error, 1)
	go func() {
		_, err := s.Changes(ctx, req, nil)
		errc <- err
	}()
	<-started
	cancel() // esc: the overlay stops waiting
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	close(release)
	head, _ := fc.compile(context.Background(), r.root)
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := s.Changes(context.Background(), Request{DefaultBranch: "main", Head: HeadWorktree}, head.JSON)
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned compile never landed in the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSnapshotMirrorsNodeModules(t *testing.T) {
	r := newRepo(t)
	r.write("package.json", `{"dependencies": {"@dataform/core": "3.0.0"}}`)
	r.commit("package.json")
	r.write("node_modules/@dataform/core/index.js", "module.exports = {}")
	testutil.Symlink(t, filepath.Join("@dataform", "core", "index.js"), filepath.Join(r.root, "node_modules", "core.js"))
	dir, err := snapshot(context.Background(), r.root, r.git("rev-parse", "HEAD"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"workflow_settings.yaml", "definitions/orders.sql", "package.json", "node_modules/@dataform/core/index.js"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("snapshot is missing %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Error("snapshot has files from outside the project")
	}
	core := filepath.Join(dir, "node_modules", "core.js")
	if runtime.GOOS == "windows" {
		// Only admins can make symlinks there: a link to a file is copied.
		if data, err := os.ReadFile(core); err != nil || string(data) != "module.exports = {}" {
			t.Errorf("core.js = %q, %v; want the linked file's content", data, err)
		}
		return
	}
	if link, err := os.Readlink(core); err != nil || link != "@dataform/core/index.js" {
		t.Errorf("symlink = %q, %v", link, err)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	s := &Source{Root: t.TempDir(), CacheDir: t.TempDir()}
	dir := s.projectDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := range 7 {
		p := filepath.Join(dir, string(rune('a'+i))+".json")
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	s.prune(dir)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{"c.json", "d.json", "e.json", "f.json", "g.json"}; !reflect.DeepEqual(names, want) {
		t.Errorf("kept %v, want %v", names, want)
	}
}

func TestCacheDay(t *testing.T) {
	if got := CacheDay(time.Date(2026, 9, 28, 23, 59, 59, 0, time.UTC)); got != "2026-09-28" {
		t.Errorf("got %s", got)
	}
	// UTC, whatever the local zone.
	if got := CacheDay(time.Date(2026, 9, 29, 0, 30, 0, 0, time.FixedZone("BST", 3600))); got != "2026-09-28" {
		t.Errorf("got %s", got)
	}
}

// A tool whose base isn't a graph (dbt's manifest) builds it with Base, from
// a snapshot holding the mirrored directories git doesn't, and compares
// with Diff, which reads the working tree: so the pushed commit is refused.
func TestChangesWithBaseAndDiff(t *testing.T) {
	r := newRepo(t)
	r.git("checkout", "-q", "-b", "feat")
	r.write("definitions/orders.sql", "select 10")
	r.write("dbt_packages/utils/macros.sql", "installed, never committed")

	var builds int
	s := &Source{
		Root: r.root, Key: "dbt-1234", CacheDir: t.TempDir(), Mirror: []string{"dbt_packages", "missing"},
		Base: func(_ context.Context, dir string) ([]byte, error) {
			builds++
			pkg, err := os.ReadFile(filepath.Join(dir, "dbt_packages/utils/macros.sql"))
			if err != nil {
				return nil, err
			}
			orders, err := os.ReadFile(filepath.Join(dir, "definitions/orders.sql"))
			if err != nil {
				return nil, err
			}
			return []byte(string(orders) + " | " + string(pkg)), nil
		},
		Diff: func(_ context.Context, base, head []byte) (Diff, error) {
			if string(base) != "select 1 | installed, never committed" || string(head) != "head" {
				t.Errorf("Diff(%q, %q)", base, head)
			}
			return Diff{Changed: []Changed{{ID: "proj.ds.orders", Reasons: []Reason{ReasonUpstream}}}}, nil
		},
	}
	ctx := context.Background()
	res, err := s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}, []byte("head"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res); !slices.Equal(got, []string{"proj.ds.orders:upstream"}) {
		t.Errorf("changed = %v", got)
	}
	if _, err := s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadWorktree}, []byte("head")); err != nil || builds != 1 {
		t.Errorf("second ask: err %v, %d builds; want the cached base", err, builds)
	}
	if _, err := s.Changes(ctx, Request{DefaultBranch: "main", Head: HeadPushed, Compile: true}, nil); !errors.Is(err, ErrWorktreeOnly) {
		t.Errorf("pushed head: err = %v, want ErrWorktreeOnly", err)
	}
}

// A Base that fails reads as a compile failure, which the overlay offers to
// retry.
func TestChangesBaseFailure(t *testing.T) {
	r := newRepo(t)
	s := &Source{Root: r.root, Key: "dbt", Base: func(context.Context, string) ([]byte, error) {
		return nil, errors.New("models/a.sql: syntax error")
	}, Diff: func(context.Context, []byte, []byte) (Diff, error) { return Diff{}, nil }}
	r.write("definitions/x.sql", "select 1")
	_, err := s.Changes(context.Background(), Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "could not compile") || !strings.Contains(err.Error(), "models/a.sql: syntax error") {
		t.Errorf("err = %v", err)
	}
}

func TestChangedFiles(t *testing.T) {
	r := newRepo(t)
	r.git("checkout", "-q", "-b", "feature")
	r.write("definitions/orders.sql", "select 10")
	r.commit("orders")
	r.write("definitions/customers.sql", "select 20") // uncommitted
	r.write("definitions/new.sql", "select 30")       // untracked
	r.write("../README.md", "changed outside the project")
	got, err := ChangedFiles(context.Background(), r.root, "main")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"definitions/customers.sql", "definitions/new.sql", "definitions/orders.sql"}; !slices.Equal(got, want) {
		t.Errorf("ChangedFiles = %v, want %v", got, want)
	}
	// With no such branch the working tree is compared with HEAD.
	got, err = ChangedFiles(context.Background(), r.root, "nope")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"definitions/customers.sql", "definitions/new.sql"}; !slices.Equal(got, want) {
		t.Errorf("against HEAD: %v, want %v", got, want)
	}
	if _, err := ChangedFiles(context.Background(), t.TempDir(), "main"); !errors.Is(err, ErrNotGit) {
		t.Errorf("outside git: %v", err)
	}
}

// renameOf is the rename a Changed Action and the deletion paired with it
// say happened: "proj.ds.old -> proj.ds.new (old file -> new file)".
func renameOf(res *Result) []string {
	var out []string
	for _, c := range res.Changed {
		if c.RenamedFrom == "" {
			continue
		}
		var to project.ActionID
		for _, d := range res.Deleted {
			if d.ID == c.RenamedFrom {
				to = d.RenamedTo
			}
		}
		if to != c.ID {
			out = append(out, "unpaired "+string(c.ID))
			continue
		}
		out = append(out, string(c.RenamedFrom)+" -> "+string(c.ID)+" ("+c.RenamedFromFile+" -> "+c.FileName+")")
	}
	return out
}

func TestChangesPairRenamedFiles(t *testing.T) {
	long := strings.Repeat("select a, b, c from x\n", 20)
	r := newRepo(t)
	r.write("definitions/stock.sql", long)
	r.write("definitions/gone.sql", "select 'nothing like the others'")
	r.commit("add stock")
	r.git("checkout", "-q", "-b", "feat")
	r.git("mv", "dataform/definitions/stock.sql", "dataform/definitions/stock_cover.sql")
	r.write("definitions/stock_cover.sql", long+"where a > 1\n")
	r.commit("rename stock")

	fc := &fakeCompiler{}
	s := r.source(fc, "")
	ctx := context.Background()
	req := Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}
	res, err := s.Changes(ctx, req, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"proj.ds.stock -> proj.ds.stock_cover (definitions/stock.sql -> definitions/stock_cover.sql)"}
	if got := renameOf(res); !reflect.DeepEqual(got, want) {
		t.Errorf("committed rename = %v, want %v", got, want)
	}
	if c, _ := res.Find("proj.ds.stock_cover"); c.Similarity < 50 || !c.IsNew() {
		t.Errorf("renamed action = %+v, want new with a similarity", c)
	}

	// A rename in the working tree that isn't staged: the new file is
	// untracked, and the index is left as it was.
	if err := os.Rename(filepath.Join(r.root, "definitions/gone.sql"), filepath.Join(r.root, "definitions/still_here.sql")); err != nil {
		t.Fatal(err)
	}
	status := r.git("status", "--porcelain")
	res, err = s.Changes(ctx, req, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "proj.ds.gone -> proj.ds.still_here (definitions/gone.sql -> definitions/still_here.sql)")
	slices.Sort(want)
	got := renameOf(res)
	slices.Sort(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("untracked rename = %v, want %v", got, want)
	}
	if after := r.git("status", "--porcelain"); after != status {
		t.Errorf("git status changed:\n%s\nwas:\n%s", after, status)
	}

	// A new file unlike the deleted one is no rename.
	if err := os.Remove(filepath.Join(r.root, "definitions/still_here.sql")); err != nil {
		t.Fatal(err)
	}
	r.write("definitions/other.sql", "select 42 as answer, 'unrelated' as why")
	res, err = s.Changes(ctx, req, r.head(fc))
	if err != nil {
		t.Fatal(err)
	}
	if got := renameOf(res); len(got) != 1 {
		t.Errorf("renames = %v, want only stock's", got)
	}
	for _, d := range res.Deleted {
		if d.ID == "proj.ds.gone" && d.RenamedTo != "" {
			t.Errorf("gone renamed to %s", d.RenamedTo)
		}
	}
}

func TestPairRenamesNeedsOneActionEachSide(t *testing.T) {
	d := Diff{
		Changed: []Changed{
			{ID: "ds.a1", FileName: "a_new.js", Reasons: []Reason{ReasonNew}},
			{ID: "ds.a2", FileName: "a_new.js", Reasons: []Reason{ReasonNew}},
			{ID: "ds.b_new", FileName: "b_new.sqlx", Reasons: []Reason{ReasonNew}},
			{ID: "ds.b_new_assertions_uniqueKey_0", FileName: "b_new.sqlx", Reasons: []Reason{ReasonNew}, Parent: "ds.b_new"},
		},
		Deleted: []Deleted{
			{ID: "ds.a", FileName: "a.js"},
			{ID: "ds.b", FileName: "b.sqlx"},
			{ID: "ds.b_assertions_uniqueKey_0", FileName: "b.sqlx", Parent: "ds.b"},
		},
	}
	pairRenames(&d, parseRenames("R100\x00a.js\x00a_new.js\x00M\x00c.sqlx\x00R087\x00b.sqlx\x00b_new.sqlx\x00"))
	for _, c := range d.Changed {
		want := project.ActionID("")
		if c.ID == "ds.b_new" {
			want = "ds.b"
		}
		if c.RenamedFrom != want {
			t.Errorf("%s renamed from %q, want %q", c.ID, c.RenamedFrom, want)
		}
	}
	if d.Changed[2].Similarity != 87 || d.Deleted[1].RenamedTo != "ds.b_new" || d.Deleted[0].RenamedTo != "" || d.Deleted[2].RenamedTo != "" {
		t.Errorf("diff = %+v", d)
	}
}

// Looking for renames in the working tree leaves .git/index alone, even
// when a saved file would have git diff refresh it: the index.lock that
// takes can stop a Dataform compile copying the project.
func TestRenamedFilesLeavesIndexAlone(t *testing.T) {
	r := newRepo(t)
	r.git("checkout", "-q", "-b", "feat")
	r.git("mv", "dataform/definitions/orders.sql", "dataform/definitions/orders_v2.sql")
	index := filepath.Join(r.dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	// Saved after the index was written, so its stat data is stale.
	later := before.ModTime().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(r.root, "definitions/customers.sql"), later, later); err != nil {
		t.Fatal(err)
	}
	renames, err := renamedFiles(context.Background(), r.root, r.git("rev-parse", "main"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := renames["definitions/orders_v2.sql"].from; got != "definitions/orders.sql" {
		t.Errorf("renames = %v, want orders.sql -> orders_v2.sql", renames)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf(".git/index was rewritten: %v -> %v", before.ModTime(), after.ModTime())
	}
}
