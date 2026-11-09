package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/frontend"
)

// fakeHost stands in for GitLab: Find answers found, or err.
type fakeHost struct {
	mu     sync.Mutex
	calls  int
	repo   forge.Repo
	branch string
	base   string
	found  forge.Found
	err    error
	// commitMRs are CommitMR's answers, by SHA.
	commitMRs   map[string]*forge.MR
	commitCalls int
}

func (f *fakeHost) Find(_ context.Context, repo forge.Repo, branch, base string) (forge.Found, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.repo, f.branch, f.base = repo, branch, base
	return f.found, f.err
}

func (f *fakeHost) Whoami(context.Context, forge.Repo) (string, error) { return "ada", nil }

func (f *fakeHost) CommitMR(_ context.Context, _ forge.Repo, sha string) (*forge.MR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCalls++
	return f.commitMRs[sha], f.err
}

func (f *fakeHost) set(found forge.Found, err error) {
	f.mu.Lock()
	f.found, f.err = found, err
	f.mu.Unlock()
}

func (f *fakeHost) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// forgeTest serves a project on branch feat of a repository whose origin
// is on gitlab.example.com, which the store knows with a token.
func forgeTest(t *testing.T) (*Server, *httptest.Server, *http.Client, *fakeHost, *forge.Store, func(...string) string) {
	t.Helper()
	_, root, git := scmRepo(t)
	git("remote", "add", "origin", "git@gitlab.example.com:group/proj.git")
	git("switch", "-q", "-c", "feat")
	env := map[string]string{}
	store := &forge.Store{Path: filepath.Join(t.TempDir(), forge.FileName), Getenv: func(k string) string { return env[k] }}
	if err := store.Set(forge.Host{Name: "gitlab.example.com", Kind: forge.GitLab, Token: "glpat-abcdefgh1234"}, false); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	cfg := &Forge{Hosts: store, Poll: func() time.Duration { return 30 * time.Millisecond },
		Client: func(forge.Kind, string) forge.Client { return host }}
	s, err := New(Config{Project: &Project{Deps: frontend.Deps{Root: root, Engine: newFakeEngine(testGraph()), DefaultBranch: "main",
		Git: func(ctx context.Context) (dataformapi.GitState, error) { return dataformapi.ReadGit(ctx, root) }}}, Token: "secret", Forge: cfg})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		s.Close()
	})
	c := ts.Client()
	jar := &tokenJar{}
	c.Jar = jar
	jar.set(ts.URL)
	waitFor(t, func() bool { return s.only().graph() != nil })
	return s, ts, c, host, store, git
}

// forgeNow waits for a check to end and answers what it found.
func forgeNow(t *testing.T, c *http.Client, ts *httptest.Server) forgeView {
	t.Helper()
	var v forgeView
	waitFor(t, func() bool {
		v = forgeView{}
		getJSON(t, c, ts.URL+"/api/forge", &v)
		return !v.Checking && v.State != "" && (v.State != "off" || v.Branch == "")
	})
	return v
}

func TestForgeFindsTheBranchsMR(t *testing.T) {
	s, ts, c, host, _, _ := forgeTest(t)
	host.set(forge.Found{
		MR:       &forge.MR{Number: 42, Title: "Feat", State: "open", Source: "feat", Target: "develop", URL: "https://gitlab.example.com/group/proj/-/merge_requests/42", Blockers: []string{}},
		Pipeline: &forge.Pipeline{ID: 7, Status: forge.StatusRunning, Stages: []forge.Stage{}},
	}, nil)
	var v forgeView
	waitFor(t, func() bool {
		v = forgeNow(t, c, ts)
		return v.State == "ok"
	})
	if v.Kind != forge.GitLab || v.Host != "gitlab.example.com" || v.Path != "group/proj" || v.Branch != "feat" || v.Base != "main" || v.MR == nil || v.MR.Number != 42 {
		t.Fatalf("view %+v", v)
	}
	if host.repo.API != "https://gitlab.example.com/api/v4" || host.branch != "feat" || host.base != "main" {
		t.Errorf("asked %+v %s %s", host.repo, host.branch, host.base)
	}
	// The pipeline runs, but no page listens: the timed check is missed,
	// and the next page to ask asks the host.
	n := host.count()
	waitFor(t, func() bool {
		s.only().forge.mu.Lock()
		defer s.only().forge.mu.Unlock()
		return s.only().forge.missed
	})
	if host.count() != n {
		t.Errorf("a timed check with no page asked the host")
	}
	forgeNow(t, c, ts)
	waitFor(t, func() bool { return host.count() > n })

	// Create MR on an MR that is open opens it.
	var cv createView
	postJSON(t, c, ts.URL+"/api/forge/create", map[string]any{}, &cv)
	if !cv.Exists || cv.URL != v.MR.URL {
		t.Errorf("create with one open: %+v", cv)
	}
}

// The first switch after the project opens asks the host for the new
// branch's merge request.
func TestForgeAsksAgainOnTheFirstSwitch(t *testing.T) {
	s, ts, c, host, _, git := forgeTest(t)
	host.set(forge.Found{MR: &forge.MR{Number: 1, State: "open", Blockers: []string{}}}, nil)
	if v := forgeNow(t, c, ts); v.Branch != "feat" {
		t.Fatalf("before: %+v", v)
	}
	git("switch", "-q", "-c", "other")
	s.only().sendBranch()
	waitFor(t, func() bool { return forgeNow(t, c, ts).Branch == "other" })
}

func TestForgeCreateAsksToPushFirst(t *testing.T) {
	_, ts, c, host, _, git := forgeTest(t)
	git("commit", "-q", "--allow-empty", "-m", "Add the orders model\n\nbody")
	host.set(forge.Found{}, nil)
	forgeNow(t, c, ts)
	var cv createView
	postJSON(t, c, ts.URL+"/api/forge/create", map[string]any{}, &cv)
	if !cv.Push || !cv.NoUpstream || cv.Remote != "origin" {
		t.Fatalf("no upstream: %+v", cv)
	}
	cv = createView{}
	postJSON(t, c, ts.URL+"/api/forge/create", map[string]any{"skip": true}, &cv)
	if !strings.HasPrefix(cv.URL, "https://gitlab.example.com/group/proj/-/merge_requests/new?") ||
		!strings.Contains(cv.URL, "source_branch%5D=feat") || !strings.Contains(cv.URL, "target_branch%5D=main") ||
		!strings.Contains(cv.URL, "title%5D=Add+the+orders+model&") && !strings.HasSuffix(cv.URL, "title%5D=Add+the+orders+model") {
		t.Errorf("url %s", cv.URL)
	}
	git("switch", "-q", "main")
	cv = createView{}
	postJSON(t, c, ts.URL+"/api/forge/create", map[string]any{"skip": true}, &cv)
	if cv.URL != "" || !strings.Contains(cv.Message, "is the base") {
		t.Errorf("on the base: %+v", cv)
	}
}

func TestForgeStates(t *testing.T) {
	_, ts, c, host, store, git := forgeTest(t)
	mr := &forge.MR{Number: 3, State: "open", Blockers: []string{}}
	host.set(forge.Found{MR: mr}, nil)
	if v := forgeNow(t, c, ts); v.State != "ok" || v.MR == nil {
		t.Fatalf("ok: %+v", v)
	}
	// Unreachable: the last answer stays, stale.
	host.set(forge.Found{}, &forge.UnreachableError{Host: "gitlab.example.com", Err: errors.New("no route")})
	postJSON(t, c, ts.URL+"/api/forge/check", map[string]any{}, nil)
	var v forgeView
	waitFor(t, func() bool { v = forgeNow(t, c, ts); return v.Stale })
	if v.MR == nil || v.MR.Number != 3 || v.CheckedAt == 0 || !strings.Contains(v.Error, "couldn't reach") {
		t.Errorf("stale: %+v", v)
	}
	// A refused token.
	host.set(forge.Found{}, forge.ErrToken)
	postJSON(t, c, ts.URL+"/api/forge/check", map[string]any{}, nil)
	waitFor(t, func() bool { v = forgeNow(t, c, ts); return v.State == "token" })
	// No token.
	if err := store.Set(forge.Host{Name: "gitlab.example.com", Kind: forge.GitLab}, true); err != nil {
		t.Fatal(err)
	}
	postJSON(t, c, ts.URL+"/api/forge/check", map[string]any{}, nil)
	waitFor(t, func() bool { v = forgeNow(t, c, ts); return v.State == "connect" })
	if !strings.Contains(v.TokenPage, "gitlab.example.com/-/user_settings/personal_access_tokens") {
		t.Errorf("token page %q", v.TokenPage)
	}
	// A host xf doesn't know shows nothing.
	git("remote", "set-url", "origin", "https://git.unknown.example/group/proj.git")
	postJSON(t, c, ts.URL+"/api/forge/check", map[string]any{}, nil)
	waitFor(t, func() bool { v = forgeNow(t, c, ts); return v.State == "off" })
}

func TestForgeHosts(t *testing.T) {
	_, ts, c, _, _, _ := forgeTest(t)
	var hv hostsView
	getJSON(t, c, ts.URL+"/api/forge/hosts", &hv)
	if len(hv.Hosts) != 3 || hv.Hosts[0].Host != "gitlab.example.com" || hv.Hosts[0].Token != "••••1234" || hv.Hosts[0].From != "file" {
		t.Fatalf("hosts %+v", hv)
	}
	postJSON(t, c, ts.URL+"/api/forge/hosts", map[string]any{"host": "https://GHE.example.com/", "kind": "github", "token": "ghp_0123456789"}, &hv)
	if len(hv.Hosts) != 4 || hv.Hosts[1].Host != "ghe.example.com" || hv.Hosts[1].Kind != forge.GitHub || !strings.Contains(hv.Hosts[1].TokenPage, "ghe.example.com/settings/tokens/new") {
		t.Fatalf("added %+v", hv)
	}
	var ok okView
	postJSON(t, c, ts.URL+"/api/forge/hosts/test", map[string]any{"host": "ghe.example.com"}, &ok)
	if !ok.OK || ok.Message != "signed in to ghe.example.com as ada" {
		t.Errorf("test %+v", ok)
	}
	postJSON(t, c, ts.URL+"/api/forge/hosts/delete", map[string]any{"host": "ghe.example.com"}, &hv)
	if len(hv.Hosts) != 3 {
		t.Errorf("deleted %+v", hv)
	}
	res, err := c.Post(ts.URL+"/api/forge/hosts", "application/json", strings.NewReader(`{"host":"x","kind":"bitbucket"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a bad kind: %s", res.Status)
	}
}

func TestBackoff(t *testing.T) {
	for fails, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 5: 5 * time.Minute, 9: 5 * time.Minute} {
		if got := backoff(fails); got != want {
			t.Errorf("backoff(%d) = %v, want %v", fails, got, want)
		}
	}
}
