package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// Comment is a comment on a merge request (a pull request's on GitHub).
type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// Comments finds, posts and edits comments on one merge request. They are
// the only writes xf makes to a host, for xf check --comment in CI (ADR
// 0040); xf web still never writes.
type Comments struct {
	a api
	// Number is the merge request's: its IID on GitLab.
	Number int
}

// NewComments is the comments of merge request number in repo, written
// with token; hc nil uses a client of its own.
func NewComments(repo Repo, number int, token string, hc *http.Client) *Comments {
	return &Comments{a: api{repo: repo, token: token, hc: hc}, Number: number}
}

// commentPages bounds how many pages Find reads: 3,000 comments.
const commentPages = 30

func (c *Comments) list() string {
	if c.a.repo.Kind == GitHub {
		return repoURL(c.a.repo) + "/issues/" + strconv.Itoa(c.Number) + "/comments"
	}
	return project(c.a.repo) + "/merge_requests/" + strconv.Itoa(c.Number) + "/notes"
}

func (c *Comments) one(id int64) string {
	if c.a.repo.Kind == GitHub {
		return repoURL(c.a.repo) + "/issues/comments/" + strconv.FormatInt(id, 10)
	}
	return c.list() + "/" + strconv.FormatInt(id, 10)
}

// Find is the oldest comment ours says is ours, nil for none.
func (c *Comments) Find(ctx context.Context, ours func(body string) bool) (*Comment, error) {
	for page := 1; page <= commentPages; page++ {
		u := c.list() + "?per_page=100&page=" + strconv.Itoa(page)
		if c.a.repo.Kind == GitLab {
			u += "&sort=asc&order_by=created_at"
		}
		var got []struct {
			Comment
			System bool `json:"system"`
		}
		if err := c.a.get(ctx, u, &got); err != nil {
			return nil, c.explain(err)
		}
		for _, g := range got {
			if !g.System && ours(g.Body) {
				return &g.Comment, nil
			}
		}
		if len(got) < 100 {
			break
		}
	}
	return nil, nil
}

// Post adds a comment with body.
func (c *Comments) Post(ctx context.Context, body string) error {
	return c.explain(c.a.do(ctx, http.MethodPost, c.list(), map[string]string{"body": body}, nil))
}

// Edit changes comment id's body.
func (c *Comments) Edit(ctx context.Context, id int64, body string) error {
	method := http.MethodPut
	if c.a.repo.Kind == GitHub {
		method = http.MethodPatch
	}
	return c.explain(c.a.do(ctx, method, c.one(id), map[string]string{"body": body}, nil))
}

// Token is what a GitLab access token says of itself.
type Token struct {
	Scopes []string `json:"scopes"`
	// ExpiresAt is its last day, "2026-12-31", "" for none.
	ExpiresAt string `json:"expires_at"`
}

// ErrNoTokenInfo is a host that doesn't describe the token: GitHub, a
// GitLab too old to, or a token of a kind that can't.
var ErrNoTokenInfo = errors.New("the host doesn't describe this token")

// Token asks GitLab what the token is (GET /personal_access_tokens/self,
// which project and group access tokens answer too). ErrToken is a token
// GitLab refused; ErrNoTokenInfo is any other answer but the token's.
func (c *Comments) Token(ctx context.Context) (*Token, error) {
	if c.a.repo.Kind != GitLab {
		return nil, ErrNoTokenInfo
	}
	var t Token
	err := c.a.get(ctx, c.a.repo.API+"/personal_access_tokens/self", &t)
	switch {
	case err == nil:
		return &t, nil
	case errors.Is(err, ErrToken):
		return nil, ErrToken
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	return nil, ErrNoTokenInfo
}

// Read reads the merge request, which says the token can see it.
func (c *Comments) Read(ctx context.Context) error {
	u := project(c.a.repo) + "/merge_requests/" + strconv.Itoa(c.Number)
	if c.a.repo.Kind == GitHub {
		u = repoURL(c.a.repo) + "/pulls/" + strconv.Itoa(c.Number)
	}
	var out struct{}
	return c.explain(c.a.get(ctx, u, &out))
}

// explain says a refusal in words about comments.
func (c *Comments) explain(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		what := "merge request !"
		if c.a.repo.Kind == GitHub {
			what = "pull request #"
		}
		return fmt.Errorf("%s%d isn't in %s on %s, or the token can't see it", what, c.Number, c.a.repo.Path, c.a.repo.Host)
	case errors.Is(err, ErrToken):
		return fmt.Errorf("%s refused the token", c.a.repo.Host)
	}
	return err
}
