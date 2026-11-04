package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// github reads GitHub's REST API, and its GraphQL API for review
// threads, on github.com or GitHub Enterprise.
type github struct {
	token string
	hc    *http.Client
}

func (g *github) api(repo Repo) api { return api{repo: repo, token: g.token, hc: g.hc} }

func repoURL(repo Repo) string { return repo.API + "/repos/" + repo.Path }

type ghPR struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	State   string    `json:"state"` // open, closed
	Draft   bool      `json:"draft"`
	HTMLURL string    `json:"html_url"`
	Created time.Time `json:"created_at"`
	Merged  *string   `json:"merged_at"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	// Of one pull request only.
	MergeableState string `json:"mergeable_state"`
}

func (p ghPR) mr() MR {
	st := p.State
	if p.Merged != nil {
		st = "merged"
	} else if st != "open" {
		st = "closed"
	}
	return MR{Number: p.Number, Title: p.Title, State: st, Draft: st == "open" && p.Draft,
		Source: p.Head.Ref, Target: p.Base.Ref, URL: p.HTMLURL, HeadSHA: p.Head.SHA, Unresolved: -1, Blockers: []string{}}
}

func (g *github) Whoami(ctx context.Context, repo Repo) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.api(repo).get(ctx, repo.API+"/user", &u); err != nil {
		if errors.Is(err, errNotFound) {
			return "", errors.New(repo.Host + " has no GitHub API at " + repo.API)
		}
		return "", err
	}
	return u.Login, nil
}

func (g *github) Find(ctx context.Context, repo Repo, branch, base string) (Found, error) {
	a := g.api(repo)
	pr, err := g.findPR(ctx, a, repo, branch, base)
	if err != nil {
		return Found{}, err
	}
	var f Found
	sha := ""
	if pr != nil {
		var d ghPR
		if err := a.get(ctx, repoURL(repo)+"/pulls/"+strconv.Itoa(pr.Number), &d); err != nil {
			return Found{}, wrap404(err, repo)
		}
		m := d.mr()
		if m.State == "open" {
			g.prDetails(ctx, a, repo, d, &m)
		}
		f.MR = &m
		sha = d.Head.SHA
	}
	p, err := g.runs(ctx, a, repo, branch, sha)
	if err != nil {
		return Found{}, err
	}
	if p != nil && f.MR != nil {
		p.URL = f.MR.URL + "/checks"
	}
	f.Pipeline = p
	return f, nil
}

// findPR is the open pull request from branch to show, else the one last
// updated; nil for none. One from a fork doesn't count.
func (g *github) findPR(ctx context.Context, a api, repo Repo, branch, base string) (*ghPR, error) {
	owner, _, _ := strings.Cut(repo.Path, "/")
	same := func(list []ghPR) []ghPR {
		var out []ghPR
		for _, p := range list {
			if p.Head.Repo == nil || strings.EqualFold(p.Head.Repo.FullName, repo.Path) {
				out = append(out, p)
			}
		}
		return out
	}
	var open []ghPR
	q := url.Values{"head": {owner + ":" + branch}, "state": {"open"}, "per_page": {"20"}}
	if err := a.get(ctx, repoURL(repo)+"/pulls?"+q.Encode(), &open); err != nil {
		return nil, wrap404(err, repo)
	}
	if open = same(open); len(open) > 0 {
		mrs, created := make([]MR, len(open)), make([]time.Time, len(open))
		for i, p := range open {
			mrs[i], created[i] = p.mr(), p.Created
		}
		return &open[pick(mrs, created, base)], nil
	}
	var all []ghPR
	q = url.Values{"head": {owner + ":" + branch}, "state": {"all"}, "per_page": {"5"}, "sort": {"updated"}, "direction": {"desc"}}
	if err := a.get(ctx, repoURL(repo)+"/pulls?"+q.Encode(), &all); err != nil {
		return nil, wrap404(err, repo)
	}
	if all = same(all); len(all) > 0 {
		return &all[0], nil
	}
	return nil, nil
}

// prDetails adds an open pull request's approvals, unresolved threads and
// what blocks it; what the host won't say is left out.
func (g *github) prDetails(ctx context.Context, a api, repo Repo, d ghPR, m *MR) {
	var reviews []struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	changes := false
	if a.get(ctx, repoURL(repo)+"/pulls/"+strconv.Itoa(d.Number)+"/reviews?per_page=100", &reviews) == nil {
		last := map[string]string{}
		for _, r := range reviews { // oldest first
			if r.User == nil || r.State == "COMMENTED" || r.State == "PENDING" {
				continue
			}
			last[r.User.Login] = r.State
		}
		for _, s := range last {
			switch s {
			case "APPROVED":
				m.Approvals++
			case "CHANGES_REQUESTED":
				changes = true
			}
		}
	}
	if repo.GraphQL != "" {
		owner, name, _ := strings.Cut(repo.Path, "/")
		var res struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							Nodes []struct {
								IsResolved bool `json:"isResolved"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct{} `json:"errors"`
		}
		body := map[string]any{
			"query":     `query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){reviewThreads(first:100){nodes{isResolved}}}}}`,
			"variables": map[string]any{"o": owner, "n": name, "p": d.Number},
		}
		if a.do(ctx, http.MethodPost, repo.GraphQL, body, &res) == nil && len(res.Errors) == 0 {
			m.Unresolved = 0
			for _, t := range res.Data.Repository.PullRequest.ReviewThreads.Nodes {
				if !t.IsResolved {
					m.Unresolved++
				}
			}
		}
	}
	if w := ghBlocker(d.MergeableState); w != "" {
		m.Blockers = append(m.Blockers, w)
	}
	if changes {
		m.Blockers = append(m.Blockers, "changes were requested")
	}
}

// ghBlocker is GitHub's mergeable_state in words, "" when nothing blocks
// the merge.
func ghBlocker(s string) string {
	switch s {
	case "dirty":
		return "has conflicts"
	case "behind":
		return "is behind its base: update the branch"
	case "blocked":
		return "blocked by the branch's rules"
	case "unstable":
		return "checks aren't passing"
	}
	return "" // clean, has_hooks, unknown; draft, which MR.Draft says
}

type ghRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	HeadSHA    string    `json:"head_sha"`
	Created    time.Time `json:"created_at"`
}

// runs are the workflow runs of sha, or with sha "" of the branch's
// newest commit that has any, as one pipeline; nil for none.
func (g *github) runs(ctx context.Context, a api, repo Repo, branch, sha string) (*Pipeline, error) {
	q := url.Values{"per_page": {"50"}}
	if sha != "" {
		q.Set("head_sha", sha)
	} else {
		q.Set("branch", branch)
	}
	var res struct {
		Runs []ghRun `json:"workflow_runs"`
	}
	if err := a.get(ctx, repoURL(repo)+"/actions/runs?"+q.Encode(), &res); err != nil {
		if errors.Is(err, errNotFound) { // Actions off, or no access to them
			return nil, nil
		}
		return nil, err
	}
	runs := res.Runs
	if len(runs) == 0 {
		return nil, nil
	}
	if sha == "" {
		sort.Slice(runs, func(i, j int) bool { return runs[i].Created.After(runs[j].Created) })
		sha = runs[0].HeadSHA
	}
	// The newest run of each workflow at sha: a re-run replaces the run
	// before it.
	newest := map[string]ghRun{}
	for _, r := range runs {
		if r.HeadSHA != sha {
			continue
		}
		if o, ok := newest[r.Name]; !ok || r.ID > o.ID {
			newest[r.Name] = r
		}
	}
	keep := make([]ghRun, 0, len(newest))
	for _, r := range newest {
		keep = append(keep, r)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].ID < keep[j].ID })
	p := &Pipeline{SHA: sha, Stages: []Stage{}}
	var statuses []string
	for _, r := range keep {
		p.ID = max(p.ID, r.ID)
		st := Stage{Name: r.Name, Status: ghStatus(r.Status, r.Conclusion), Jobs: []Job{}}
		var jobs struct {
			Jobs []struct {
				Name       string     `json:"name"`
				Status     string     `json:"status"`
				Conclusion string     `json:"conclusion"`
				HTMLURL    string     `json:"html_url"`
				Started    *time.Time `json:"started_at"`
				Completed  *time.Time `json:"completed_at"`
			} `json:"jobs"`
		}
		if err := a.get(ctx, repoURL(repo)+"/actions/runs/"+strconv.FormatInt(r.ID, 10)+"/jobs?per_page=100", &jobs); err == nil {
			for _, j := range jobs.Jobs {
				st.Jobs = append(st.Jobs, Job{Name: j.Name, Status: ghStatus(j.Status, j.Conclusion), URL: j.HTMLURL, Seconds: seconds(j.Started, j.Completed), Ran: Ran{j.Started, j.Completed}})
			}
		} else if errors.Is(err, ErrToken) || Unreachable(err) {
			return nil, err
		}
		p.Stages = append(p.Stages, st)
		statuses = append(statuses, st.Status)
	}
	p.Status = fold(statuses)
	spans(p)
	if len(keep) == 1 {
		p.URL = keep[0].HTMLURL
	} else {
		p.URL = repo.Web + "/commit/" + sha + "/checks"
	}
	return p, nil
}

// ghStatus is a GitHub run's or job's status and conclusion, folded.
func ghStatus(status, conclusion string) string {
	switch status {
	case "in_progress":
		return StatusRunning
	case "completed":
	default: // queued, waiting, requested, pending
		return StatusPending
	}
	switch conclusion {
	case "success", "neutral":
		return StatusSuccess
	case "failure", "timed_out", "startup_failure":
		return StatusFailed
	case "cancelled":
		return StatusCanceled
	case "skipped", "stale":
		return StatusSkipped
	case "action_required":
		return StatusManual
	}
	return StatusSuccess
}
