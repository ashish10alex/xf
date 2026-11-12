package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// commentHost is a host holding one merge request's comments, recording
// the writes.
type commentHost struct {
	comments []map[string]any
	writes   []string
}

func (h *commentHost) serve(t *testing.T, kind Kind, header, token string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(header) != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		list, one := "/repos/o/r/issues/5/comments", "/repos/o/r/issues/comments/"
		edit := http.MethodPatch
		if kind == GitLab {
			list, one, edit = "/api/v4/projects/g%2Fp/merge_requests/5/notes", "/api/v4/projects/g%2Fp/merge_requests/5/notes/", http.MethodPut
		}
		path := r.URL.EscapedPath()
		var body struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == http.MethodGet && path == list:
			page := r.URL.Query().Get("page")
			start := map[string]int{"1": 0, "2": 100}[page]
			end := min(start+100, len(h.comments))
			_ = json.NewEncoder(w).Encode(h.comments[min(start, end):end])
		case r.Method == http.MethodPost && path == list:
			h.writes = append(h.writes, "post "+body.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 999})
		case r.Method == http.MethodGet && (path == "/api/v4/projects/g%2Fp/merge_requests/5" || path == "/repos/o/r/pulls/5"):
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 5})
		case r.Method == http.MethodGet && path == "/api/v4/personal_access_tokens/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"scopes": []string{"api"}, "expires_at": "2026-12-31"})
		case r.Method == edit && strings.HasPrefix(path, one):
			h.writes = append(h.writes, "edit "+strings.TrimPrefix(path, one)+" "+body.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Logf("no route: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCommentsGitHub(t *testing.T) {
	h := &commentHost{}
	for i := range 120 {
		h.comments = append(h.comments, map[string]any{"id": i + 1, "body": fmt.Sprintf("comment %d", i+1)})
	}
	h.comments[110]["body"] = "<!-- ours -->\nreport"
	srv := h.serve(t, GitHub, "Authorization", "Bearer tok")
	repo := RepoAt(GitHub, srv.URL, "o/r")
	repo.API = srv.URL
	cs := NewComments(repo, 5, "tok", nil)
	ctx := context.Background()
	c, err := cs.Find(ctx, func(b string) bool { return strings.HasPrefix(b, "<!-- ours -->") })
	if err != nil || c == nil || c.ID != 111 {
		t.Fatalf("Find = %+v, %v; want comment 111 on the second page", c, err)
	}
	if err := cs.Edit(ctx, c.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Post(ctx, "fresh"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.writes, "|"); got != "edit 111 new|post fresh" {
		t.Errorf("writes = %q", got)
	}
	if c, err := cs.Find(ctx, func(string) bool { return false }); err != nil || c != nil {
		t.Errorf("Find none = %+v, %v", c, err)
	}
}

func TestCommentsGitLab(t *testing.T) {
	h := &commentHost{comments: []map[string]any{
		{"id": 1, "body": "<!-- ours -->\nby the system", "system": true},
		{"id": 2, "body": "<!-- ours -->\nreport"},
	}}
	srv := h.serve(t, GitLab, "PRIVATE-TOKEN", "tok")
	cs := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 5, "tok", nil)
	ctx := context.Background()
	c, err := cs.Find(ctx, func(b string) bool { return strings.HasPrefix(b, "<!-- ours -->") })
	if err != nil || c == nil || c.ID != 2 {
		t.Fatalf("Find = %+v, %v; want note 2, not the system note", c, err)
	}
	if err := cs.Edit(ctx, 2, "new"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.writes, "|"); got != "edit 2 new" {
		t.Errorf("writes = %q", got)
	}
	bad := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 5, "wrong", nil)
	if _, err := bad.Find(ctx, func(string) bool { return true }); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("wrong token: %v", err)
	}
	missing := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 6, "tok", nil)
	if _, err := missing.Find(ctx, func(string) bool { return true }); err == nil || !strings.Contains(err.Error(), "merge request !6 isn't in g/p") {
		t.Errorf("missing: %v", err)
	}
}

func TestCommentsTokenAndRead(t *testing.T) {
	h := &commentHost{}
	srv := h.serve(t, GitLab, "PRIVATE-TOKEN", "tok")
	ctx := context.Background()
	cs := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 5, "tok", nil)
	tok, err := cs.Token(ctx)
	if err != nil || tok.ExpiresAt != "2026-12-31" || len(tok.Scopes) != 1 || tok.Scopes[0] != "api" {
		t.Errorf("Token = %+v, %v", tok, err)
	}
	if err := cs.Read(ctx); err != nil {
		t.Errorf("Read: %v", err)
	}
	bad := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 5, "wrong", nil)
	if _, err := bad.Token(ctx); err != ErrToken {
		t.Errorf("wrong token: %v", err)
	}
	if err := NewComments(RepoAt(GitLab, srv.URL, "g/p"), 6, "tok", nil).Read(ctx); err == nil || !strings.Contains(err.Error(), "!6") {
		t.Errorf("missing MR: %v", err)
	}

	gh := h.serve(t, GitHub, "Authorization", "Bearer tok")
	repo := RepoAt(GitHub, gh.URL, "o/r")
	repo.API = gh.URL
	ghc := NewComments(repo, 5, "tok", nil)
	if _, err := ghc.Token(ctx); err != ErrNoTokenInfo {
		t.Errorf("GitHub Token: %v", err)
	}
	if err := ghc.Read(ctx); err != nil {
		t.Errorf("GitHub Read: %v", err)
	}
}
