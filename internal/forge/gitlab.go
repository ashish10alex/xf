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

// gitlab reads GitLab's REST API (v4).
type gitlab struct {
	token string
	hc    *http.Client
}

// New is the client for kind, calling with token; hc nil uses a client
// of its own.
func New(kind Kind, token string, hc *http.Client) Client {
	if kind == GitHub {
		return &github{token: token, hc: hc}
	}
	return &gitlab{token: token, hc: hc}
}

func (g *gitlab) api(repo Repo) api { return api{repo: repo, token: g.token, hc: g.hc} }

// project is the project's part of an API URL: its path, escaped.
func project(repo Repo) string { return repo.API + "/projects/" + url.PathEscape(repo.Path) }

type glMR struct {
	IID             int       `json:"iid"`
	Title           string    `json:"title"`
	State           string    `json:"state"` // opened, closed, locked, merged
	Draft           bool      `json:"draft"`
	WIP             bool      `json:"work_in_progress"`
	Source          string    `json:"source_branch"`
	Target          string    `json:"target_branch"`
	WebURL          string    `json:"web_url"`
	SHA             string    `json:"sha"`
	Created         time.Time `json:"created_at"`
	ProjectID       int       `json:"project_id"`
	SourceProjectID int       `json:"source_project_id"`
	// Of one merge request only.
	DetailedStatus string `json:"detailed_merge_status"`
	HasConflicts   bool   `json:"has_conflicts"`
	HeadPipeline   *struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
		SHA    string `json:"sha"`
	} `json:"head_pipeline"`
}

func (m glMR) mr() MR {
	st := "open"
	switch m.State {
	case "merged":
		st = "merged"
	case "closed", "locked":
		st = "closed"
	}
	return MR{Number: m.IID, Title: m.Title, State: st, Draft: st == "open" && (m.Draft || m.WIP),
		Source: m.Source, Target: m.Target, URL: m.WebURL, HeadSHA: m.SHA, Unresolved: -1, Blockers: []string{}}
}

func (g *gitlab) Whoami(ctx context.Context, repo Repo) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := g.api(repo).get(ctx, repo.API+"/user", &u); err != nil {
		if errors.Is(err, errNotFound) {
			return "", errors.New(repo.Host + " has no GitLab API at " + repo.API)
		}
		return "", err
	}
	return u.Username, nil
}

func (g *gitlab) Find(ctx context.Context, repo Repo, branch, base string) (Found, error) {
	a := g.api(repo)
	mr, err := g.findMR(ctx, a, repo, branch, base)
	if err != nil {
		return Found{}, err
	}
	var f Found
	var pid int64
	var pStatus, pURL, pSHA string
	if mr != nil {
		var d glMR
		if err := a.get(ctx, project(repo)+"/merge_requests/"+strconv.Itoa(mr.IID), &d); err != nil {
			return Found{}, wrap404(err, repo)
		}
		m := d.mr()
		if m.State == "open" {
			g.mrDetails(ctx, a, repo, d, &m)
		}
		f.MR = &m
		if hp := d.HeadPipeline; hp != nil {
			pid, pStatus, pURL, pSHA = hp.ID, hp.Status, hp.WebURL, hp.SHA
		}
	}
	if pid == 0 {
		var ps []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
			SHA    string `json:"sha"`
		}
		q := url.Values{"ref": {branch}, "per_page": {"1"}, "order_by": {"id"}, "sort": {"desc"}}
		if err := a.get(ctx, project(repo)+"/pipelines?"+q.Encode(), &ps); err != nil {
			return Found{}, wrap404(err, repo)
		}
		if len(ps) > 0 {
			pid, pStatus, pURL, pSHA = ps[0].ID, ps[0].Status, ps[0].WebURL, ps[0].SHA
		}
	}
	if pid != 0 {
		p := &Pipeline{ID: pid, Status: glStatus(pStatus), URL: pURL, SHA: pSHA, Stages: []Stage{}}
		left := maxDownstream
		if stages, err := g.stages(ctx, a, project(repo), pid, &left, 0); err == nil {
			p.Stages = stages
			spans(p)
		} else if errors.Is(err, ErrToken) || Unreachable(err) {
			return Found{}, err
		}
		f.Pipeline = p
	}
	return f, nil
}

// findMR is the open merge request from branch to show, else the one
// that was last updated; nil for none. Merge requests from forks, whose
// source is another project, don't count.
func (g *gitlab) findMR(ctx context.Context, a api, repo Repo, branch, base string) (*glMR, error) {
	same := func(list []glMR) []glMR {
		var out []glMR
		for _, m := range list {
			if m.SourceProjectID == 0 || m.SourceProjectID == m.ProjectID {
				out = append(out, m)
			}
		}
		return out
	}
	var open []glMR
	q := url.Values{"source_branch": {branch}, "state": {"opened"}, "per_page": {"20"}}
	if err := a.get(ctx, project(repo)+"/merge_requests?"+q.Encode(), &open); err != nil {
		return nil, wrap404(err, repo)
	}
	if open = same(open); len(open) > 0 {
		mrs, created := make([]MR, len(open)), make([]time.Time, len(open))
		for i, m := range open {
			mrs[i], created[i] = m.mr(), m.Created
		}
		return &open[pick(mrs, created, base)], nil
	}
	var all []glMR
	q = url.Values{"source_branch": {branch}, "state": {"all"}, "per_page": {"5"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	if err := a.get(ctx, project(repo)+"/merge_requests?"+q.Encode(), &all); err != nil {
		return nil, wrap404(err, repo)
	}
	if all = same(all); len(all) > 0 {
		return &all[0], nil
	}
	return nil, nil
}

// mrDetails adds an open merge request's approvals, unresolved threads
// and what blocks it. Each is extra: one the host won't say is left out.
func (g *gitlab) mrDetails(ctx context.Context, a api, repo Repo, d glMR, m *MR) {
	base := project(repo) + "/merge_requests/" + strconv.Itoa(d.IID)
	var ap struct {
		ApprovedBy        []struct{} `json:"approved_by"`
		ApprovalsRequired int        `json:"approvals_required"`
	}
	if a.get(ctx, base+"/approvals", &ap) == nil {
		m.Approvals, m.Required = len(ap.ApprovedBy), ap.ApprovalsRequired
	}
	var ds []struct {
		Notes []struct {
			Resolvable bool `json:"resolvable"`
			Resolved   bool `json:"resolved"`
		} `json:"notes"`
	}
	if a.get(ctx, base+"/discussions?per_page=100", &ds) == nil {
		m.Unresolved = 0
		for _, d := range ds {
			for _, n := range d.Notes {
				if n.Resolvable && !n.Resolved {
					m.Unresolved++
					break
				}
			}
		}
	}
	if w := glBlocker(d.DetailedStatus); w != "" {
		m.Blockers = append(m.Blockers, w)
	} else if d.DetailedStatus == "" && d.HasConflicts {
		m.Blockers = append(m.Blockers, "has conflicts")
	}
}

// glBlocker is GitLab's detailed_merge_status in words, "" when nothing
// blocks the merge.
func glBlocker(s string) string {
	switch s {
	case "", "mergeable", "checking", "unchecked", "approvals_syncing", "preparing", "not_open":
		return ""
	case "draft_status":
		return "" // MR.Draft says it, as its state
	case "conflict":
		return "has conflicts"
	case "need_rebase":
		return "needs a rebase"
	case "ci_must_pass":
		return "the pipeline must pass"
	case "ci_still_running":
		return "the pipeline is still running"
	case "discussions_not_resolved":
		return "threads aren't resolved"
	case "not_approved":
		return "isn't approved"
	case "requested_changes":
		return "changes were requested"
	case "blocked_status":
		return "is blocked by another merge request"
	case "external_status_checks":
		return "external status checks haven't passed"
	case "jira_association_missing":
		return "needs a Jira issue in its title or description"
	case "commits_status":
		return "the source branch is missing or has no commits"
	case "broken_status":
		return "can't be merged"
	}
	return strings.ReplaceAll(s, "_", " ")
}

// glJob is a job, or a trigger job (a bridge), which starts a downstream
// pipeline and opens to it.
type glJob struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Stage        string     `json:"stage"`
	Status       string     `json:"status"`
	WebURL       string     `json:"web_url"`
	Duration     float64    `json:"duration"`
	AllowFailure bool       `json:"allow_failure"`
	Started      *time.Time `json:"started_at"`
	Finished     *time.Time `json:"finished_at"`
	Downstream   *struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		Status    string `json:"status"`
		WebURL    string `json:"web_url"`
	} `json:"downstream_pipeline"`
}

// maxDownstream is how many downstream pipelines one pipeline's reading
// follows, each two calls; maxDepth how deep: a child's child, no further.
const (
	maxDownstream = 8
	maxDepth      = 2
)

// stages are pipeline id's jobs and trigger jobs by stage, the stages in
// the order their first jobs were made; proj is its project's part of an
// API URL. A trigger job brings the pipeline it started, while left (of
// maxDownstream) and depth allow.
func (g *gitlab) stages(ctx context.Context, a api, proj string, id int64, left *int, depth int) ([]Stage, error) {
	var jobs []glJob
	base := proj + "/pipelines/" + strconv.FormatInt(id, 10)
	if err := a.get(ctx, base+"/jobs?per_page=100", &jobs); err != nil {
		return nil, err
	}
	// Trigger jobs aren't among the jobs. A host too old to list them
	// shows the jobs alone.
	var bridges []glJob
	if err := a.get(ctx, base+"/bridges?per_page=100", &bridges); err == nil {
		jobs = append(jobs, bridges...)
	} else if errors.Is(err, ErrToken) || Unreachable(err) {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	out := []Stage{}
	at := map[string]int{}
	var folded [][]string
	for _, j := range jobs {
		i, ok := at[j.Stage]
		if !ok {
			i = len(out)
			at[j.Stage] = i
			out = append(out, Stage{Name: j.Stage, Jobs: []Job{}})
			folded = append(folded, nil)
		}
		job := Job{Name: j.Name, Status: glStatus(j.Status), URL: j.WebURL, Seconds: j.Duration, Ran: Ran{j.Started, j.Finished}}
		if d := j.Downstream; d != nil {
			if d.WebURL != "" {
				job.URL = d.WebURL
			}
			if d.ID != 0 {
				down, err := g.downstream(ctx, a, proj, j, left, depth)
				if err != nil {
					return nil, err
				}
				job.Downstream = down
				job.Status, job.Ran, job.Seconds = down.Status, down.Ran, 0
				if down.Ran.Finished != nil {
					job.Seconds = down.Ran.Finished.Sub(*down.Ran.Started).Seconds()
				}
			}
		}
		out[i].Jobs = append(out[i].Jobs, job)
		st := job.Status
		if st == StatusFailed && j.AllowFailure {
			st = StatusSuccess
		}
		folded[i] = append(folded[i], st)
	}
	for i := range out {
		out[i].Status = fold(folded[i])
	}
	return out, nil
}

// downstream is the pipeline trigger job b started, with its stages
// while left and depth allow, and as long as the token can see them:
// else its status alone.
func (g *gitlab) downstream(ctx context.Context, a api, proj string, b glJob, left *int, depth int) (*Pipeline, error) {
	d := b.Downstream
	p := &Pipeline{ID: d.ID, Status: glStatus(d.Status), URL: d.WebURL, Stages: []Stage{}}
	if *left <= 0 || depth >= maxDepth {
		return p, nil
	}
	*left--
	if d.ProjectID != 0 {
		proj = a.repo.API + "/projects/" + strconv.FormatInt(d.ProjectID, 10)
	}
	stages, err := g.stages(ctx, a, proj, d.ID, left, depth+1)
	if err != nil {
		if errors.Is(err, ErrToken) || Unreachable(err) {
			return nil, err
		}
		return p, nil
	}
	p.Stages = stages
	spans(p)
	return p, nil
}

// glStatus is a GitLab pipeline's or job's status, folded.
func glStatus(s string) string {
	switch s {
	case "running":
		return StatusRunning
	case "success":
		return StatusSuccess
	case "failed":
		return StatusFailed
	case "canceled", "canceling":
		return StatusCanceled
	case "skipped":
		return StatusSkipped
	case "manual":
		return StatusManual
	}
	return StatusPending // created, waiting_for_resource, preparing, pending, scheduled
}

// wrap404 says a 404 in words.
func wrap404(err error, repo Repo) error {
	if errors.Is(err, errNotFound) {
		return notFound(repo)
	}
	return err
}
