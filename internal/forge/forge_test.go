package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseRemote(t *testing.T) {
	for _, c := range []struct {
		in         string
		host, path string
		ssh        bool
	}{
		{"git@gitlab.example.com:group/sub/proj.git", "gitlab.example.com", "group/sub/proj", true},
		{"work-gitlab:group/proj", "work-gitlab", "group/proj", true},
		{"ssh://git@gitlab.example.com:2222/group/proj.git", "gitlab.example.com", "group/proj", true},
		{"https://github.com/owner/repo.git", "github.com", "owner/repo", false},
		{"https://user@GitLab.Example.com:8443/group/proj/", "gitlab.example.com:8443", "group/proj", false},
	} {
		r, ok := ParseRemote(c.in)
		if !ok || r.Host != c.host || r.Path != c.path || r.SSH != c.ssh {
			t.Errorf("ParseRemote(%q) = %+v, %v", c.in, r, ok)
		}
	}
	for _, bad := range []string{"", "/local/path/repo", "file:///tmp/repo", "https://github.com/"} {
		if r, ok := ParseRemote(bad); ok {
			t.Errorf("ParseRemote(%q) = %+v, want none", bad, r)
		}
	}
}

func TestRepoAt(t *testing.T) {
	if r := RepoAt(GitLab, "gitlab.example.com", "g/p"); r.API != "https://gitlab.example.com/api/v4" || r.Web != "https://gitlab.example.com/g/p" {
		t.Errorf("gitlab: %+v", r)
	}
	if r := RepoAt(GitHub, "github.com", "o/r"); r.API != "https://api.github.com" || r.GraphQL != "https://api.github.com/graphql" {
		t.Errorf("github.com: %+v", r)
	}
	if r := RepoAt(GitHub, "ghe.example.com", "o/r"); r.API != "https://ghe.example.com/api/v3" || r.GraphQL != "https://ghe.example.com/api/graphql" {
		t.Errorf("ghe: %+v", r)
	}
	if r := RepoAt(GitLab, "http://127.0.0.1:9", "g/p"); r.API != "http://127.0.0.1:9/api/v4" || r.Host != "127.0.0.1:9" {
		t.Errorf("http: %+v", r)
	}
}

func TestNewMRURL(t *testing.T) {
	gl := NewMRURL(RepoAt(GitLab, "gitlab.example.com", "g/p"), "feat/x", "main", "Add x")
	if !strings.HasPrefix(gl, "https://gitlab.example.com/g/p/-/merge_requests/new?") ||
		!strings.Contains(gl, "merge_request%5Bsource_branch%5D=feat%2Fx") || !strings.Contains(gl, "merge_request%5Btitle%5D=Add+x") {
		t.Errorf("gitlab: %s", gl)
	}
	gh := NewMRURL(RepoAt(GitHub, "github.com", "o/r"), "feat/x", "main", "Add x")
	if gh != "https://github.com/o/r/compare/main...feat/x?expand=1&title=Add+x" {
		t.Errorf("github: %s", gh)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	s := &Store{Path: filepath.Join(dir, "xf", FileName), Getenv: func(k string) string { return env[k] }}
	if hs, err := s.Hosts(); err != nil || len(hs) != 2 {
		t.Fatalf("no file: %+v, %v", hs, err)
	}
	if err := s.Set(Host{Name: "https://GitLab.Example.com/", Kind: GitLab, Token: " glpat-secret1234 "}, false); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.Path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, %v", fi.Mode(), err)
		}
	}
	a, ok := s.Lookup("gitlab.example.com")
	if !ok || a.Kind != GitLab || a.Token != "glpat-secret1234" || a.From != "file" {
		t.Errorf("lookup: %+v %v", a, ok)
	}
	// An empty token keeps the one there; clear takes it out.
	if err := s.Set(Host{Name: "gitlab.example.com", Kind: GitLab}, false); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup("gitlab.example.com"); a.Token != "glpat-secret1234" {
		t.Errorf("kept token: %+v", a)
	}
	// The environment stands in for a host with no token, and wins for
	// the built-in hosts.
	env["GITLAB_TOKEN"], env["GITHUB_TOKEN"] = "envlab", "envhub"
	if a, _ := s.Lookup("gitlab.example.com"); a.Token != "glpat-secret1234" {
		t.Errorf("file should win for a self-hosted host: %+v", a)
	}
	if err := s.Set(Host{Name: "gitlab.example.com", Kind: GitLab}, true); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup("gitlab.example.com"); a.Token != "envlab" || a.From != "GITLAB_TOKEN" {
		t.Errorf("env stand-in: %+v", a)
	}
	if err := s.Set(Host{Name: "github.com", Kind: GitHub, Token: "filehub"}, false); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup("github.com"); a.Token != "envhub" || !a.Builtin || !a.Entry {
		t.Errorf("env wins for github.com: %+v", a)
	}
	if err := s.Delete("GitLab.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("gitlab.example.com"); ok {
		t.Error("deleted host still known")
	}
	if err := s.Set(Host{Name: "x", Kind: "bitbucket"}, false); err == nil {
		t.Error("unknown kind taken")
	}
	if Mask("glpat-secret1234") != "••••1234" || Mask("short") != "••••" || Mask("") != "" {
		t.Error("mask")
	}
}

// fake serves canned JSON by path (with the query), checking the token.
func fake(t *testing.T, header, token string, routes map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(header)
		if got != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := r.Method + " " + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		v, ok := routes[key]
		if !ok {
			// Match without the query when the routes don't care.
			v, ok = routes[r.Method+" "+r.URL.EscapedPath()]
		}
		if !ok {
			t.Logf("no route: %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGitLabFind(t *testing.T) {
	p := "/api/v4/projects/g%2Fp"
	srv := fake(t, "PRIVATE-TOKEN", "tok", map[string]any{
		"GET " + p + "/merge_requests?per_page=20&source_branch=feat&state=opened": []map[string]any{
			{"iid": 7, "title": "Old", "state": "opened", "target_branch": "develop", "source_branch": "feat", "created_at": "2026-10-20T10:00:00Z", "project_id": 1, "source_project_id": 1},
			{"iid": 8, "title": "Into main", "state": "opened", "target_branch": "main", "source_branch": "feat", "created_at": "2026-10-19T10:00:00Z", "project_id": 1, "source_project_id": 1},
			{"iid": 9, "title": "From a fork", "state": "opened", "target_branch": "main", "source_branch": "feat", "created_at": "2026-10-21T10:00:00Z", "project_id": 1, "source_project_id": 2},
		},
		"GET " + p + "/merge_requests/8": map[string]any{
			"iid": 8, "title": "Into main", "state": "opened", "draft": true, "target_branch": "main", "source_branch": "feat",
			"web_url": "https://gl/g/p/-/merge_requests/8", "detailed_merge_status": "need_rebase",
			"head_pipeline": map[string]any{"id": 100, "status": "running", "web_url": "https://gl/g/p/-/pipelines/100", "sha": "abc"},
		},
		"GET " + p + "/merge_requests/8/approvals":   map[string]any{"approved_by": []any{map[string]any{}}, "approvals_required": 2},
		"GET " + p + "/merge_requests/8/discussions": []any{map[string]any{"notes": []any{map[string]any{"resolvable": true, "resolved": false}}}, map[string]any{"notes": []any{map[string]any{"resolvable": true, "resolved": true}}}, map[string]any{"notes": []any{map[string]any{"resolvable": false}}}},
		"GET " + p + "/pipelines/100/jobs": []map[string]any{
			{"id": 3, "name": "deploy", "stage": "deploy", "status": "created"},
			{"id": 1, "name": "lint", "stage": "test", "status": "success", "duration": 12.5, "started_at": "2026-10-20T10:00:00Z", "finished_at": "2026-10-20T10:00:12Z"},
			{"id": 2, "name": "unit", "stage": "test", "status": "running"},
			{"id": 4, "name": "flaky", "stage": "check", "status": "failed", "allow_failure": true},
		},
		"GET " + p + "/pipelines/100/bridges": []map[string]any{
			{"id": 5, "name": "child", "stage": "deploy", "status": "running", "web_url": "https://gl/job/5", "downstream_pipeline": map[string]any{"web_url": "https://gl/g/p/-/pipelines/101"}},
		},
	})
	repo := RepoAt(GitLab, srv.URL, "g/p")
	f, err := New(GitLab, "tok", nil).Find(context.Background(), repo, "feat", "main")
	if err != nil {
		t.Fatal(err)
	}
	m := f.MR
	if m == nil || m.Number != 8 || !m.Draft || m.State != "open" || m.Approvals != 1 || m.Required != 2 || m.Unresolved != 1 || len(m.Blockers) != 1 || m.Blockers[0] != "needs a rebase" {
		t.Fatalf("mr: %+v", m)
	}
	pl := f.Pipeline
	if pl == nil || pl.ID != 100 || pl.Status != StatusRunning || len(pl.Stages) != 3 {
		t.Fatalf("pipeline: %+v", pl)
	}
	if s := pl.Stages[0]; s.Name != "test" || s.Status != StatusRunning || len(s.Jobs) != 2 || s.Jobs[0].Seconds != 12.5 {
		t.Errorf("stage 0: %+v", s)
	}
	if s := pl.Stages[0]; s.Jobs[0].Started == nil || s.Jobs[0].Finished == nil || s.Started == nil || s.Finished != nil || pl.Started == nil || pl.Finished != nil {
		t.Errorf("a running stage and pipeline have started, not finished: %+v %+v", s, pl.Ran)
	}
	if s := pl.Stages[1]; s.Name != "deploy" || s.Status != StatusRunning || len(s.Jobs) != 2 || s.Jobs[1].URL != "https://gl/g/p/-/pipelines/101" {
		t.Errorf("stage 1, with a trigger job that opens its pipeline: %+v", s)
	}
	if s := pl.Stages[2]; s.Status != StatusSuccess || s.Jobs[0].Status != StatusFailed {
		t.Errorf("an allowed failure: %+v", s)
	}
}

// A pipeline whose one job is a trigger job: GitLab says the trigger
// job passed once the child pipeline was made, and the parent can pass
// while the child still runs. The job shows the child's status, and the
// child's jobs, read from its own project.
func TestGitLabDownstream(t *testing.T) {
	p := "/api/v4/projects/g%2Fp"
	child := "/api/v4/projects/9/pipelines/101"
	srv := fake(t, "PRIVATE-TOKEN", "tok", map[string]any{
		"GET " + p + "/merge_requests?per_page=20&source_branch=feat&state=opened":                           []any{},
		"GET " + p + "/merge_requests?order_by=updated_at&per_page=5&sort=desc&source_branch=feat&state=all": []any{},
		"GET " + p + "/pipelines?order_by=id&per_page=1&ref=feat&sort=desc":                                  []map[string]any{{"id": 100, "status": "success", "web_url": "u"}},
		"GET " + p + "/pipelines/100/jobs":                                                                   []any{},
		"GET " + p + "/pipelines/100/bridges": []map[string]any{
			{"id": 5, "name": "upload", "stage": "triggers", "status": "success", "duration": 2.1, "started_at": "2026-10-27T10:24:45Z", "finished_at": "2026-10-27T10:24:47Z",
				"downstream_pipeline": map[string]any{"id": 101, "project_id": 9, "status": "running", "web_url": "https://gl/g/p/-/pipelines/101"}},
		},
		"GET " + child + "/jobs": []map[string]any{
			{"id": 6, "name": "deploy_staging", "stage": "deploy", "status": "running", "started_at": "2026-10-27T10:24:57Z"},
		},
		"GET " + child + "/bridges": []any{},
	})
	f, err := New(GitLab, "tok", nil).Find(context.Background(), RepoAt(GitLab, srv.URL, "g/p"), "feat", "main")
	if err != nil {
		t.Fatal(err)
	}
	pl := f.Pipeline
	if pl == nil || pl.Status != StatusSuccess || len(pl.Stages) != 1 || len(pl.Stages[0].Jobs) != 1 {
		t.Fatalf("pipeline: %+v", pl)
	}
	s, j := pl.Stages[0], pl.Stages[0].Jobs[0]
	if j.Status != StatusRunning || j.Seconds != 0 || j.Finished != nil || j.URL != "https://gl/g/p/-/pipelines/101" || s.Status != StatusRunning {
		t.Errorf("the trigger job and its stage show the child running: %+v %+v", s, j)
	}
	d := j.Downstream
	if d == nil || d.ID != 101 || len(d.Stages) != 1 || d.Stages[0].Name != "deploy" || d.Stages[0].Jobs[0].Name != "deploy_staging" || d.Stages[0].Jobs[0].Status != StatusRunning {
		t.Fatalf("downstream: %+v", d)
	}
	if !pl.Unfinished() {
		t.Error("a passed pipeline with a running child is asked about again")
	}
	d.Status, d.Stages = StatusSuccess, nil
	if pl.Unfinished() {
		t.Error("done once the child is")
	}
}

func TestGitLabNoMR(t *testing.T) {
	p := "/api/v4/projects/g%2Fp"
	srv := fake(t, "PRIVATE-TOKEN", "tok", map[string]any{
		"GET " + p + "/merge_requests?per_page=20&source_branch=feat&state=opened":                           []any{},
		"GET " + p + "/merge_requests?order_by=updated_at&per_page=5&sort=desc&source_branch=feat&state=all": []any{},
		"GET " + p + "/pipelines?order_by=id&per_page=1&ref=feat&sort=desc":                                  []map[string]any{{"id": 5, "status": "success", "web_url": "u"}},
		"GET " + p + "/pipelines/5/jobs":                                                                     []any{},
		"GET " + p + "/pipelines/5/bridges":                                                                  []any{},
	})
	f, err := New(GitLab, "tok", nil).Find(context.Background(), RepoAt(GitLab, srv.URL, "g/p"), "feat", "main")
	if err != nil || f.MR != nil || f.Pipeline == nil || f.Pipeline.Status != StatusSuccess {
		t.Fatalf("%+v %v", f, err)
	}
	// A pipeline with no jobs has no stages: a list the page can walk,
	// never null.
	if b, _ := json.Marshal(f.Pipeline); !strings.Contains(string(b), `"stages":[]`) {
		t.Errorf("no jobs: %s", b)
	}
}

func TestErrors(t *testing.T) {
	srv := fake(t, "PRIVATE-TOKEN", "right", nil)
	repo := RepoAt(GitLab, srv.URL, "g/p")
	if _, err := New(GitLab, "wrong", nil).Find(context.Background(), repo, "feat", "main"); !errors.Is(err, ErrToken) {
		t.Errorf("401: %v", err)
	}
	if _, err := New(GitLab, "right", nil).Find(context.Background(), repo, "feat", "main"); err == nil || !strings.Contains(err.Error(), "can't see it") {
		t.Errorf("404: %v", err)
	}
	srv.Close()
	if _, err := New(GitLab, "right", nil).Find(context.Background(), repo, "feat", "main"); !Unreachable(err) {
		t.Errorf("down: %v", err)
	}
}

func TestGitHubFind(t *testing.T) {
	r := "/repos/o/r"
	srv := fake(t, "Authorization", "Bearer tok", map[string]any{
		"GET " + r + "/pulls?head=o%3Afeat&per_page=20&state=open": []map[string]any{
			{"number": 4, "title": "Feat", "state": "open", "created_at": "2026-10-20T10:00:00Z",
				"head": map[string]any{"ref": "feat", "sha": "s1", "repo": map[string]any{"full_name": "o/r"}}, "base": map[string]any{"ref": "main"}},
		},
		"GET " + r + "/pulls/4": map[string]any{"number": 4, "title": "Feat", "state": "open", "html_url": "https://gh/o/r/pull/4", "mergeable_state": "dirty",
			"head": map[string]any{"ref": "feat", "sha": "s1"}, "base": map[string]any{"ref": "main"}},
		"GET " + r + "/pulls/4/reviews": []map[string]any{
			{"user": map[string]any{"login": "a"}, "state": "CHANGES_REQUESTED"},
			{"user": map[string]any{"login": "a"}, "state": "APPROVED"},
			{"user": map[string]any{"login": "b"}, "state": "COMMENTED"},
		},
		"POST /graphql": map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": []any{map[string]any{"isResolved": false}, map[string]any{"isResolved": true}}}}}}},
		"GET " + r + "/actions/runs?head_sha=s1&per_page=50": map[string]any{"workflow_runs": []map[string]any{
			{"id": 10, "name": "CI", "status": "completed", "conclusion": "failure", "head_sha": "s1", "html_url": "u10"},
			{"id": 12, "name": "CI", "status": "in_progress", "head_sha": "s1", "html_url": "u12"},
			{"id": 11, "name": "Lint", "status": "completed", "conclusion": "success", "head_sha": "s1", "html_url": "u11"},
		}},
		"GET " + r + "/actions/runs/11/jobs": map[string]any{"jobs": []map[string]any{{"name": "lint", "status": "completed", "conclusion": "success", "started_at": "2026-10-20T10:00:00Z", "completed_at": "2026-10-20T10:01:00Z"}}},
		"GET " + r + "/actions/runs/12/jobs": map[string]any{"jobs": []map[string]any{{"name": "test", "status": "in_progress"}}},
	})
	repo := Repo{Kind: GitHub, Host: "gh", Path: "o/r", Web: "https://gh/o/r", API: srv.URL, GraphQL: srv.URL + "/graphql"}
	f, err := New(GitHub, "tok", nil).Find(context.Background(), repo, "feat", "main")
	if err != nil {
		t.Fatal(err)
	}
	m := f.MR
	if m == nil || m.Number != 4 || m.Approvals != 1 || m.Unresolved != 1 || len(m.Blockers) != 1 || m.Blockers[0] != "has conflicts" {
		t.Fatalf("pr: %+v", m)
	}
	pl := f.Pipeline
	if pl == nil || pl.Status != StatusRunning || len(pl.Stages) != 2 || pl.URL != "https://gh/o/r/pull/4/checks" {
		t.Fatalf("pipeline: %+v", pl)
	}
	if s := pl.Stages[0]; s.Name != "Lint" || s.Jobs[0].Seconds != 60 || s.Finished == nil || !s.Finished.Equal(*s.Jobs[0].Finished) {
		t.Errorf("lint: %+v", s)
	}
	if s := pl.Stages[1]; s.Name != "CI" || s.Status != StatusRunning {
		t.Errorf("a re-run replaces the run before: %+v", s)
	}
}

func TestSpan(t *testing.T) {
	at := func(m int) *time.Time { x := time.Date(2026, 10, 20, 10, m, 0, 0, time.UTC); return &x }
	jobs := []Ran{{at(5), at(9)}, {at(2), at(6)}, {}}
	if r := span(StatusSuccess, jobs); !r.Started.Equal(*at(2)) || !r.Finished.Equal(*at(9)) {
		t.Errorf("done: the first start and the last finish: %+v", r)
	}
	if r := span(StatusRunning, jobs); !r.Started.Equal(*at(2)) || r.Finished != nil {
		t.Errorf("going: started, not finished: %+v", r)
	}
	if r := span(StatusSkipped, []Ran{{}}); r.Started != nil || r.Finished != nil {
		t.Errorf("never ran: %+v", r)
	}
}

func TestFold(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{StatusSuccess, StatusPending}, StatusRunning},
		{[]string{StatusPending, StatusPending}, StatusPending},
		{[]string{StatusSuccess, StatusFailed}, StatusFailed},
		{[]string{StatusSuccess, StatusSkipped}, StatusSuccess},
		{[]string{StatusSuccess, StatusManual}, StatusManual},
	} {
		if got := fold(c.in); got != c.want {
			t.Errorf("fold(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
