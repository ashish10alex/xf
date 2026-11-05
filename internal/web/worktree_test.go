package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
)

// gitRepo runs git in dir with a fixed identity and no global config.
func gitRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// worktreeServer serves a real checkout at <tmp>/shop whose project is
// in proj, on main, with a branch feature/x and the project's
// node_modules untracked. xf remembers what it opens.
func worktreeServer(t *testing.T) (s *Server, ts *httptest.Server, main string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	top := filepath.Join(t.TempDir(), "shop")
	main = filepath.Join(top, "proj")
	for path, body := range map[string]string{
		filepath.Join(main, "definitions", "a.sqlx"):                     "a",
		filepath.Join(top, ".gitignore"):                                 "node_modules/\n",
		filepath.Join(main, "node_modules", "@dataform", "core", "x.js"): "x",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRepo(t, top, "init", "-q", "-b", "main")
	gitRepo(t, top, "add", ".")
	gitRepo(t, top, "commit", "-q", "-m", "a")
	gitRepo(t, top, "branch", "feature/x")
	gitRepo(t, top, "branch", "other")

	var mu sync.Mutex
	remembered := []session.Project{{Root: main, Backend: "dataform"}}
	opened := func(root string) *Project {
		return &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: root, Backend: "dataform", Branches: gitbranch.Repo{Dir: root}}}
	}
	s, err := New(Config{Token: "secret", Project: opened(main),
		Projects: func() []session.Project {
			mu.Lock()
			defer mu.Unlock()
			return append([]session.Project{}, remembered...)
		},
		Open: func(p session.Project) (*Project, error) {
			mu.Lock()
			remembered = append(remembered, p)
			mu.Unlock()
			return opened(p.Root), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts, main
}

type worktreeAnswer struct {
	OK      bool
	Key     string
	Message string
	Kind    string
	Open    []string
}

func TestWorktreeOpenListRemove(t *testing.T) {
	s, ts, main := worktreeServer(t)
	c := ts.Client()
	if s.startKey != "proj" {
		t.Fatalf("start key %q", s.startKey)
	}

	// shift+enter on feature/x makes a worktree in the main checkout's
	// .xf/worktrees and opens its project, with node_modules linked.
	var a worktreeAnswer
	postJSON(t, c, ts.URL+"/api/worktree/open", map[string]string{"name": "feature/x"}, &a)
	if !a.OK || a.Key != "proj@xf_feature-x" || !strings.Contains(a.Message, "made a worktree") {
		t.Fatalf("open: %+v", a)
	}
	wt := filepath.Join(filepath.Dir(main), ".xf", "worktrees", "xf_feature-x", "proj")
	if _, err := os.Stat(filepath.Join(wt, "node_modules", "@dataform", "core", "x.js")); err != nil {
		t.Errorf("node_modules isn't linked: %v", err)
	}

	// Again, from the main checkout: the branch is checked out there, so
	// it opens that.
	var again worktreeAnswer
	req := map[string]string{"name": "feature/x"}
	postKeyed(t, c, ts.URL+"/api/worktree/open", "proj", req, &again)
	if !again.OK || again.Key != "proj@xf_feature-x" || !strings.Contains(again.Message, "checked out in") {
		t.Errorf("open again: %+v", again)
	}
	var cur worktreeAnswer
	postKeyed(t, c, ts.URL+"/api/worktree/open", "proj", map[string]string{"name": "main"}, &cur)
	if cur.OK {
		t.Errorf("opened the checkout's own branch: %+v", cur)
	}

	// The projects list names it under proj, whose key stays proj.
	var list bootView
	getJSON(t, c, ts.URL+"/api/boot", &list)
	var row *pickerProject
	for i, p := range list.Picker {
		if p.Key == "proj@xf_feature-x" {
			row = &list.Picker[i]
		} else if p.Key != "proj" {
			t.Errorf("unexpected row %+v", p)
		}
	}
	if row == nil || row.Name != "proj@xf_feature-x" || row.Worktree != "xf_feature-x" || !row.Own || !samePath(row.Main, main) {
		t.Fatalf("the worktree's row: %+v", row)
	}
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project=proj@xf_feature-x", &b)
	if b.Project == nil || b.Project.Name != "proj@xf_feature-x" {
		t.Fatalf("boot of the worktree: %+v", b)
	}

	// A worktree made elsewhere, not opened yet, is found under proj.
	other := filepath.Join(filepath.Dir(filepath.Dir(main)), "agent")
	gitRepo(t, main, "worktree", "add", "-q", other, "other")
	var found map[string][]foundWorktree
	getJSON(t, c, ts.URL+"/api/picker/worktrees", &found)
	if fs := found[main]; len(fs) != 1 || fs[0].Name != "proj@agent" || fs[0].Branch != "other" || fs[0].Own {
		t.Errorf("found worktrees: %+v", found)
	}
	// The projects tab lists, under the open proj, its worktrees that
	// aren't open: agent's, not xf_feature-x's.
	var unopened map[string][]foundWorktree
	getJSON(t, c, ts.URL+"/api/worktrees", &unopened)
	if fs := unopened[main]; len(unopened) != 1 || len(fs) != 1 || fs[0].Name != "proj@agent" || fs[0].Main != main {
		t.Errorf("the open projects' worktrees: %+v", unopened)
	}
	// One xf didn't make goes too, its branch kept; the main checkout
	// never does.
	var rm worktreeAnswer
	postJSON(t, c, ts.URL+"/api/worktree/remove", map[string]string{"root": filepath.Join(other, "proj")}, &rm)
	if !rm.OK {
		t.Errorf("removing a worktree xf didn't make: %+v", rm)
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is still there: %v", other, err)
	}
	postJSON(t, c, ts.URL+"/api/worktree/remove", map[string]string{"root": main}, &rm)
	if rm.OK || !strings.Contains(rm.Message, "not a worktree") {
		t.Errorf("removing the main checkout: %+v", rm)
	}

	// Work of its own keeps it, open, until the page says to lose it.
	if err := os.WriteFile(filepath.Join(wt, "mine.sqlx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	postJSON(t, c, ts.URL+"/api/worktree/remove", map[string]string{"root": wt}, &rm)
	if rm.OK || rm.Kind != "dirty" || !strings.Contains(rm.Message, "mine.sqlx") || s.openAt(wt) == nil {
		t.Errorf("removed a dirty worktree: %+v", rm)
	}
	if _, err := os.Lstat(filepath.Join(wt, "node_modules")); err != nil {
		t.Errorf("the question left node_modules unlinked: %v", err)
	}
	postJSON(t, c, ts.URL+"/api/worktree/remove", map[string]any{"root": wt, "force": true}, &rm)
	if !rm.OK || len(rm.Open) != 1 || rm.Open[0] != "proj" {
		t.Fatalf("remove: %+v", rm)
	}
	if _, err := os.Stat(filepath.Dir(wt)); err == nil {
		t.Error("the worktree is still there")
	}
	if s.openAt(wt) != nil {
		t.Error("its project is still open")
	}
	if _, err := os.Stat(filepath.Join(main, "node_modules", "@dataform", "core", "x.js")); err != nil {
		t.Errorf("removing took the main checkout's node_modules: %v", err)
	}
}

// The projects tab's "New worktree…" makes a branch no one has yet, from
// the checkout's HEAD, in a worktree; without new, a name no branch has
// is refused.
func TestWorktreeNewBranch(t *testing.T) {
	_, ts, main := worktreeServer(t)
	c := ts.Client()
	var a worktreeAnswer
	postJSON(t, c, ts.URL+"/api/worktree/open", map[string]any{"name": "ash/try"}, &a)
	if a.OK || !strings.Contains(a.Message, "no branch") {
		t.Errorf("without new: %+v", a)
	}
	postJSON(t, c, ts.URL+"/api/worktree/open", map[string]any{"name": "ash/try", "new": true}, &a)
	if !a.OK || a.Key != "proj@xf_ash-try" || !strings.Contains(a.Message, "made the branch ash/try") {
		t.Fatalf("new: %+v", a)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(main), ".xf", "worktrees", "xf_ash-try", "proj", "definitions", "a.sqlx")); err != nil {
		t.Errorf("the new worktree has no project: %v", err)
	}
	// A worktree the person named is in xf_ and that name.
	var named worktreeAnswer
	postKeyed(t, c, ts.URL+"/api/worktree/open", "proj", map[string]any{"name": "ash/two", "new": true, "folder": "review 2"}, &named)
	if !named.OK || named.Key != "proj@xf_review-2" || !strings.Contains(named.Message, "xf_review-2") {
		t.Errorf("named: %+v", named)
	}
	// An existing name with new opens that branch, as without.
	var again worktreeAnswer
	postKeyed(t, c, ts.URL+"/api/worktree/open", "proj", map[string]any{"name": "ash/try", "new": true}, &again)
	if !again.OK || again.Key != "proj@xf_ash-try" || !strings.Contains(again.Message, "checked out in") {
		t.Errorf("new on a branch that is there: %+v", again)
	}
}

// The page on the worktree's project leaves it at the "closed" event,
// which ends its request to remove it: the removal still happens.
func TestWorktreeRemoveOutlivesThePage(t *testing.T) {
	_, ts, main := worktreeServer(t)
	c := ts.Client()
	var a worktreeAnswer
	postJSON(t, c, ts.URL+"/api/worktree/open", map[string]string{"name": "feature/x"}, &a)
	if !a.OK {
		t.Fatalf("open: %+v", a)
	}
	wt := filepath.Join(filepath.Dir(main), ".xf", "worktrees", "xf_feature-x", "proj")
	page := openEvents(t, c, ts.URL, a.Key)
	ctx, leave := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b, _ := json.Marshal(map[string]string{"root": wt})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/worktree/remove", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if res, err := c.Do(req); err == nil {
			_ = res.Body.Close()
		}
	}()
	if ev := page.until(t, "closed", "worktree is being removed"); ev == "" {
		t.Fatal("no closed event")
	}
	leave()
	<-done
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Dir(wt)); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the worktree is still there after the page left")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// postKeyed posts as a page on the project key does.
func postKeyed(t *testing.T, c *http.Client, u, key string, body, v any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(projectHeader, key)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %s", u, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
