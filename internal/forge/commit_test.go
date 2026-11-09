package forge

import (
	"context"
	"testing"
)

// CommitMR is the merged merge request a commit came in by, before an
// open one it is also in; one from a fork doesn't count, and a commit the
// host doesn't have has none.
func TestCommitMR(t *testing.T) {
	p := "/api/v4/projects/g%2Fp"
	gl := fake(t, "PRIVATE-TOKEN", "tok", map[string]any{
		"GET " + p + "/repository/commits/abc/merge_requests": []map[string]any{
			{"iid": 3, "title": "Open one", "state": "opened", "project_id": 1, "source_project_id": 1},
			{"iid": 2, "title": "From a fork", "state": "merged", "project_id": 1, "source_project_id": 9},
			{"iid": 1, "title": "Brought it in", "state": "merged", "web_url": "https://gl/g/p/-/merge_requests/1", "project_id": 1, "source_project_id": 1},
		},
	})
	repo := RepoAt(GitLab, gl.URL, "g/p")
	m, err := New(GitLab, "tok", nil).CommitMR(context.Background(), repo, "abc")
	if err != nil || m == nil || m.Number != 1 || m.State != "merged" || m.URL != "https://gl/g/p/-/merge_requests/1" {
		t.Fatalf("gitlab: %+v, %v", m, err)
	}
	if m, err := New(GitLab, "tok", nil).CommitMR(context.Background(), repo, "unpushed"); m != nil || err != nil {
		t.Errorf("a commit gitlab doesn't have: %+v, %v", m, err)
	}
	if u := CommitURL(repo, "abc"); u != gl.URL+"/g/p/-/commit/abc" {
		t.Errorf("gitlab commit url = %s", u)
	}

	r := "/repos/o/r"
	gh := fake(t, "Authorization", "Bearer tok", map[string]any{
		"GET " + r + "/commits/def/pulls": []map[string]any{
			{"number": 5, "title": "Feat", "state": "closed", "merged_at": "2026-10-20T10:00:00Z", "html_url": "https://gh/o/r/pull/5"},
		},
	})
	ghRepo := Repo{Kind: GitHub, Host: "gh", Path: "o/r", Web: "https://gh/o/r", API: gh.URL}
	m, err = New(GitHub, "tok", nil).CommitMR(context.Background(), ghRepo, "def")
	if err != nil || m == nil || m.Number != 5 || m.State != "merged" {
		t.Fatalf("github: %+v, %v", m, err)
	}
	if u := CommitURL(ghRepo, "def"); u != "https://gh/o/r/commit/def" {
		t.Errorf("github commit url = %s", u)
	}
}
