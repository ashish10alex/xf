package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gitscm"
)

// The inline blame's hover card (blame.js) shows the commit's page on the
// host and the merge request that brought it in. The host is asked when a
// card first shows a commit, and its answer kept a while.

// forgeRepo is the project's repository on its host, through the
// branch's push remote ("" for a detached HEAD, which uses origin), and
// the host as xf knows it; ok is false with no repository, no remote, or
// a host xf doesn't know.
func (p *projectState) forgeRepo(ctx context.Context) (branch string, repo forge.Repo, a forge.Access, ok bool) {
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		return "", repo, a, false
	}
	st, err := g.Status(ctx)
	if err != nil {
		return "", repo, a, false
	}
	remote := "origin"
	if st.Branch != "" {
		remote = forge.PushRemote(ctx, g.Top, st.Branch)
	}
	raw, err := forge.RemoteURL(ctx, g.Top, remote)
	if err != nil {
		return "", repo, a, false
	}
	r, ok := forge.ParseRemote(raw)
	if !ok {
		return "", repo, a, false
	}
	a, ok = forge.Match(ctx, p.forgeCfg.Hosts, r)
	if !ok {
		return "", repo, a, false
	}
	return st.Branch, forge.RepoAt(a.Kind, a.Name, r.Path), a, true
}

// commitView is GET /api/forge/commit's answer.
type commitView struct {
	// State is as forgeView's: "off", "connect", "token", "ok" or "error".
	State string     `json:"state"`
	Kind  forge.Kind `json:"kind,omitempty"`
	// URL is the commit's page on the host.
	URL   string    `json:"url,omitempty"`
	MR    *forge.MR `json:"mr"`
	Error string    `json:"error,omitempty"`
}

// commitKeep is how long a commit's answer is kept: a merged merge
// request stays as it is, an open one may merge.
const commitKeep = 10 * time.Minute

type keptCommit struct {
	view commitView
	at   time.Time
}

// forgeCommit answers with ?sha's page on the host and its merge request.
func (p *projectState) forgeCommit(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha")
	if len(sha) != 40 || !isHex(sha) {
		http.Error(w, "bad request: not a commit", http.StatusBadRequest)
		return
	}
	if p.forgeCfg == nil {
		writeJSON(w, commitView{State: "off"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), forgeTimeout)
	defer cancel()
	_, repo, a, ok := p.forgeRepo(ctx)
	if !ok {
		writeJSON(w, commitView{State: "off"})
		return
	}
	v := commitView{Kind: a.Kind, URL: forge.CommitURL(repo, sha)}
	if a.Token == "" {
		v.State = "connect"
		writeJSON(w, v)
		return
	}
	key := repo.Web + "@" + sha
	f := &p.forge
	f.mu.Lock()
	k, kept := f.commits[key]
	f.mu.Unlock()
	if kept && time.Since(k.at) < commitKeep {
		writeJSON(w, k.view)
		return
	}
	mr, err := p.forgeCfg.client(a.Kind, a.Token).CommitMR(ctx, repo, sha)
	switch {
	case err == nil:
		v.State, v.MR = "ok", mr
		f.mu.Lock()
		if f.commits == nil || len(f.commits) >= maxKeptCommits {
			f.commits = map[string]keptCommit{}
		}
		f.commits[key] = keptCommit{v, time.Now()}
		f.mu.Unlock()
	case errors.Is(err, forge.ErrToken):
		v.State = "token"
	default:
		v.State, v.Error = "error", err.Error()
	}
	writeJSON(w, v)
}

// maxKeptCommits bounds the commits' answers kept, all dropped past it.
const maxKeptCommits = 500

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
