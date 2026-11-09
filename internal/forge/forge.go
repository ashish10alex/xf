// Package forge finds a branch's merge request and its last pipeline on
// GitLab, or its pull request and workflow runs on GitHub, for xf web's
// status line and Source Control (ADR 0020). It only reads: xf calls the
// hosts' APIs with a token of its own, kept in tokens.json beside the
// user's config.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Kind is what a host runs.
type Kind string

const (
	GitLab Kind = "gitlab"
	GitHub Kind = "github"
)

// Valid says k is a kind xf knows.
func (k Kind) Valid() bool { return k == GitLab || k == GitHub }

// Repo is a project on a host, with the addresses of its pages and API.
type Repo struct {
	Kind Kind
	// Host is the host's name as the remote and the entries say it, e.g.
	// gitlab.example.com; Path the project's, e.g. group/sub/project or
	// owner/repo.
	Host, Path string
	// Web is the project's page; API the host's REST API, and GraphQL
	// its GraphQL endpoint (GitHub's only).
	Web, API, GraphQL string
}

// RepoAt is the project at path on host, a host's name or its URL
// (https://host, or http://host:port for a test server).
func RepoAt(kind Kind, host, path string) Repo {
	scheme, name := "https", host
	if s, rest, ok := strings.Cut(host, "://"); ok {
		scheme, name = s, rest
	}
	name = strings.TrimRight(name, "/")
	origin := scheme + "://" + name
	r := Repo{Kind: kind, Host: name, Path: path, Web: origin + "/" + path}
	switch {
	case kind == GitLab:
		r.API = origin + "/api/v4"
	case name == "github.com":
		r.API, r.GraphQL = "https://api.github.com", "https://api.github.com/graphql"
	default: // GitHub Enterprise
		r.API, r.GraphQL = origin+"/api/v3", origin+"/api/graphql"
	}
	return r
}

// MR is a merge request (a pull request on GitHub).
type MR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// State is "open", "merged" or "closed"; Draft says an open one is a
	// draft.
	State  string `json:"state"`
	Draft  bool   `json:"draft,omitempty"`
	Source string `json:"source"`
	Target string `json:"target"`
	URL    string `json:"url"`
	// Approvals are how many have approved; Required how many must, 0
	// when the host doesn't say.
	Approvals int `json:"approvals"`
	Required  int `json:"required,omitempty"`
	// Unresolved are the threads still to resolve, -1 when unknown.
	Unresolved int `json:"unresolved"`
	// Blockers are what stops it being merged, in plain words.
	Blockers []string `json:"blockers"`
	HeadSHA  string   `json:"headSha,omitempty"`
}

// Pipeline is a branch's last pipeline: on GitHub, the workflow runs of
// one commit, each a stage.
type Pipeline struct {
	ID     int64   `json:"id"`
	Status string  `json:"status"`
	URL    string  `json:"url"`
	SHA    string  `json:"sha,omitempty"`
	Stages []Stage `json:"stages"`
	Ran
}

// Stage is a pipeline's stage, or on GitHub a workflow run.
type Stage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Jobs   []Job  `json:"jobs"`
	Ran
}

// Job is one of a stage's jobs. Seconds is how long it ran, 0 for one
// that hasn't.
type Job struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	URL     string  `json:"url"`
	Seconds float64 `json:"seconds,omitempty"`
	Ran
	// Downstream is the pipeline a trigger job started, whose status,
	// times and URL the job then takes: the trigger job itself is done
	// once the pipeline is made. On GitLab only.
	Downstream *Pipeline `json:"downstream,omitempty"`
}

// Ran is when a job, stage or pipeline started and finished, nil for
// not yet. A stage's and a pipeline's are their jobs': the first start
// and, once all are done, the last finish.
type Ran struct {
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

// span is the Ran of jobs that together have status: none finished
// while status is going.
func span(status string, jobs []Ran) Ran {
	var r Ran
	for _, j := range jobs {
		if j.Started != nil && (r.Started == nil || j.Started.Before(*r.Started)) {
			r.Started = j.Started
		}
		if j.Finished != nil && (r.Finished == nil || j.Finished.After(*r.Finished)) {
			r.Finished = j.Finished
		}
	}
	if Going(status) || r.Started == nil {
		r.Finished = nil
	}
	return r
}

// spans fills in p's and its stages' Ran from their jobs'.
func spans(p *Pipeline) {
	var all []Ran
	for i := range p.Stages {
		s := &p.Stages[i]
		rs := make([]Ran, len(s.Jobs))
		for k, j := range s.Jobs {
			rs[k] = j.Ran
		}
		s.Ran = span(s.Status, rs)
		all = append(all, s.Ran)
	}
	p.Ran = span(p.Status, all)
}

// The statuses of pipelines, stages and jobs, the hosts' many folded into
// these.
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusSuccess  = "success"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
	StatusSkipped  = "skipped"
	StatusManual   = "manual"
)

// Going says a pipeline with status s has yet to finish, so it is asked
// about again.
func Going(s string) bool { return s == StatusPending || s == StatusRunning }

// Unfinished says p, or a pipeline one of its trigger jobs started, has
// yet to finish: a parent pipeline can pass while its child still runs.
func (p *Pipeline) Unfinished() bool {
	if p == nil {
		return false
	}
	if Going(p.Status) {
		return true
	}
	for _, s := range p.Stages {
		for _, j := range s.Jobs {
			if j.Downstream.Unfinished() {
				return true
			}
		}
	}
	return false
}

// Found is what a host says of a branch.
type Found struct {
	MR       *MR       `json:"mr"`
	Pipeline *Pipeline `json:"pipeline"`
}

// Client reads a host.
type Client interface {
	// Find is branch's merge request, the open one targeting base first,
	// and its last pipeline.
	Find(ctx context.Context, repo Repo, branch, base string) (Found, error)
	// Whoami is the user the token signs in as.
	Whoami(ctx context.Context, repo Repo) (string, error)
	// CommitMR is the merge request that brought commit sha in (see
	// commit.go); nil for none.
	CommitMR(ctx context.Context, repo Repo, sha string) (*MR, error)
}

// ErrToken is a host refusing the token: it is wrong, has expired or has
// been revoked.
var ErrToken = errors.New("the host refused the token")

// UnreachableError is a host that didn't answer, or answered that it is
// down: a VPN that is off, say.
type UnreachableError struct {
	Host string
	Err  error
}

func (e *UnreachableError) Error() string { return "couldn't reach " + e.Host + ": " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// Unreachable says err is a host that didn't answer.
func Unreachable(err error) bool {
	var u *UnreachableError
	return errors.As(err, &u)
}

// NewMRURL is the host's page for making a merge request from source into
// target, its title filled in.
func NewMRURL(repo Repo, source, target, title string) string {
	if repo.Kind == GitHub {
		q := url.Values{"expand": {"1"}}
		if title != "" {
			q.Set("title", title)
		}
		return repo.Web + "/compare/" + escapeRef(target) + "..." + escapeRef(source) + "?" + q.Encode()
	}
	q := url.Values{"merge_request[source_branch]": {source}, "merge_request[target_branch]": {target}}
	if title != "" {
		q.Set("merge_request[title]", title)
	}
	return repo.Web + "/-/merge_requests/new?" + q.Encode()
}

// escapeRef escapes a branch for a URL's path, keeping its slashes.
func escapeRef(b string) string {
	parts := strings.Split(b, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// TokenPage is the host's page for making a token xf can read with.
func TokenPage(kind Kind, host string) string {
	r := RepoAt(kind, host, "")
	origin := strings.TrimSuffix(r.Web, "/")
	if kind == GitLab {
		return origin + "/-/user_settings/personal_access_tokens?name=xf&scopes=read_api"
	}
	return origin + "/settings/tokens/new?description=xf&scopes=repo"
}

// pick is the open merge request to show of several: the newest that
// targets base, else the newest.
func pick(mrs []MR, created []time.Time, base string) int {
	best := -1
	for i := range mrs {
		switch {
		case best < 0:
			best = i
		case (mrs[i].Target == base) != (mrs[best].Target == base):
			if mrs[i].Target == base {
				best = i
			}
		case created[i].After(created[best]):
			best = i
		}
	}
	return best
}

// fold is the status of jobs together: running while any runs, failed
// once any failed that may not, else as the most telling of the rest.
func fold(statuses []string) string {
	has := func(s string) bool { return slices.Contains(statuses, s) }
	switch {
	case len(statuses) == 0:
		return StatusPending
	case has(StatusRunning):
		return StatusRunning
	case has(StatusPending):
		if has(StatusSuccess) || has(StatusFailed) {
			return StatusRunning
		}
		return StatusPending
	case has(StatusFailed):
		return StatusFailed
	case has(StatusCanceled):
		return StatusCanceled
	case has(StatusManual):
		return StatusManual
	case has(StatusSuccess):
		return StatusSuccess
	}
	return StatusSkipped
}

// notFound is a 404's error: the project, or the token's view of it.
func notFound(repo Repo) error {
	return fmt.Errorf("%s isn't on %s, or the token can't see it", repo.Path, repo.Host)
}
