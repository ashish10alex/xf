package dataformapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// A workflow invocation xf did not start (a scheduled one, a colleague's,
// one started from the console) is watched the way an API run's is, and
// ends in the same report: what each action's BigQuery job cost.

// InvocationRef names a workflow invocation. Only ID is needed: what the
// rest leaves empty is the project's own (see Resolve).
type InvocationRef struct {
	Project, Location, Repo, ID string
}

// invocationID is what Dataform allows in an invocation's ID: the ones it
// makes are a number and a UUID joined with a dash.
var invocationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// What each part of a pasted name may be. Parts go into the API's URL and
// into log lines, so nothing but these characters gets that far.
var (
	projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
	locationPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	repoNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// consoleHost is the only host a pasted URL may name: it is only ever read
// for the parts of its path, never fetched, but a link to anywhere else is
// not a workflow of ours.
const consoleHost = "console.cloud.google.com"

// maxInput is the longest paste read.
const maxInput = 2048

// ParseInvocation reads what a user pastes to name a workflow invocation:
// its ID, its page in the Cloud console
// (…/locations/L/repositories/R/workflows/ID?project=P), or its resource
// name (projects/P/locations/L/repositories/R/workflowInvocations/ID).
func ParseInvocation(input string) (InvocationRef, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return InvocationRef{}, errors.New("paste a workflow ID, or the URL of its page in the console")
	}
	if len(s) > maxInput {
		return InvocationRef{}, errors.New("that is too long for a workflow ID or a console URL")
	}
	var ref InvocationRef
	if strings.Contains(s, "/") {
		path := s
		if u, err := url.Parse(s); err == nil && (u.Scheme != "" || u.Host != "") {
			if u.Scheme != "https" || u.Host != consoleHost {
				return InvocationRef{}, fmt.Errorf("only a page of %s names a workflow: that URL is of %s", consoleHost, cmp.Or(u.Host, u.Scheme+":"))
			}
			path = u.Path
			ref.Project = u.Query().Get("project")
		}
		segs := strings.Split(strings.Trim(path, "/"), "/")
		for i := 0; i+1 < len(segs); i++ {
			v := segs[i+1]
			switch segs[i] {
			case "projects":
				ref.Project = v
			case "locations":
				ref.Location = v
			case "repositories":
				ref.Repo = v
			case "workflows", "workflowInvocations":
				ref.ID = v
			}
		}
		if ref.ID == "" {
			return InvocationRef{}, fmt.Errorf("%q names no workflow: its URL ends in …/workflows/<ID>", s)
		}
	} else {
		ref.ID = s
	}
	for _, c := range []struct {
		what, v string
		re      *regexp.Regexp
	}{
		{"workflow ID", ref.ID, invocationID},
		{"project", ref.Project, projectIDPattern},
		{"location", ref.Location, locationPattern},
		{"repository name", ref.Repo, repoNamePattern},
	} {
		if c.v != "" && !c.re.MatchString(c.v) {
			return InvocationRef{}, fmt.Errorf("%q is not a %s", c.v, c.what)
		}
	}
	return ref, nil
}

// Resolve is the repository the invocation is in: what ref names, and
// otherwise base's.
func (ref InvocationRef) Resolve(base Repository) Repository {
	return Repository{
		Project:  cmp.Or(ref.Project, base.Project),
		Location: cmp.Or(ref.Location, base.Location),
		Name:     cmp.Or(ref.Repo, base.Name),
	}
}

// name is the invocation's resource name in repo.
func (ref InvocationRef) name(repo Repository) string {
	return repo.path() + "/workflowInvocations/" + url.PathEscape(ref.ID)
}

// WatchOptions say which invocation to watch, and how.
type WatchOptions struct {
	// Repo is the Dataform repository the invocation is in; all three of
	// its fields are needed.
	Repo Repository
	ID   string
	// PollInterval is how often the invocation is polled; 0 means 5s.
	PollInterval time.Duration
	// Jobs and JobLocation are as for Options.
	Jobs        JobReader
	JobLocation string
}

// Started is when the invocation started; the zero time if it has not.
func (i *Invocation) Started() time.Time { return parseTime(i.Timing.StartTime) }

// Watch follows the workflow invocation opts names, whoever started it,
// until it ends: an invocation that has ended is read once. The first look
// is made before Watch returns, so an ID that is not found, or that these
// credentials may not see, is an error here; the invocation is returned
// with it.
//
// Unlike Start's run, this one never cancels the invocation: it is not
// xf's. Cancelling ctx, like Kill, stops watching it.
func Watch(ctx context.Context, api API, opts WatchOptions) (*project.Run, *Invocation, error) {
	repo := opts.Repo
	switch {
	case repo.Name == "":
		return nil, nil, errors.New("no Dataform repository: paste the workflow's URL, or set dataformRepo in .xf/config.json (or pass --dataform-repo)")
	case repo.Project == "":
		return nil, nil, errors.New("no GCP project: set defaultProject in workflow_settings.yaml")
	case repo.Location == "":
		return nil, nil, errors.New("no location: set defaultLocation in workflow_settings.yaml or pass --dataform-location")
	}
	name := InvocationRef{ID: opts.ID}.name(repo)
	inv, err := api.GetWorkflowInvocation(ctx, name)
	if err != nil {
		if LooksLikeLocationError(err, repo.Location) {
			err = fmt.Errorf("%w: Dataform repositories live in a region such as europe-west2, not a multi-region like EU (the location is set by \"dataformLocation\" in .xf/config.json, or by the workflow's URL)", err)
		}
		return nil, nil, err
	}
	// Dataform answers with the invocation's own name: it must be the one
	// asked for, in the repository and location asked for. (The project
	// part may be spelled as its number.)
	if tail := name[strings.Index(name, "/locations/"):]; !strings.HasSuffix(inv.Name, tail) {
		return nil, nil, fmt.Errorf("Dataform answered with %q, not the workflow asked for", inv.Name)
	}

	lines := make(chan string)
	done := make(chan project.RunResult, 1)
	watch, stopWatching := context.WithCancel(context.Background())
	stopOnCancel := context.AfterFunc(ctx, stopWatching)
	r := &runner{
		api:   api,
		opts:  Options{Repo: repo, PollInterval: opts.PollInterval, Jobs: opts.Jobs, JobLocation: opts.JobLocation},
		lines: lines,
		watch: watch,
	}
	u := repo.ConsoleURL(name)
	r.url.Store(&u)
	go func() {
		defer close(lines)
		defer stopWatching()
		defer stopOnCancel()
		r.emit("watching workflow %s in %s (%s, %s) · %s", opts.ID, repo.Name, repo.Project, repo.Location, strings.ToLower(inv.State))
		r.emit("↗ %s", u)
		// A background context: asking to stop is stopping to watch.
		res := r.poll(context.Background(), name)
		res.Duration = watchedFor(inv.Started(), r.report.Load())
		done <- res
	}()
	return &project.Run{
		Cmdline: "dataform workflow " + opts.ID,
		Lines:   lines,
		Done:    done,
		Kill:    stopWatching,
		URL:     func() string { return u },
		Report:  r.report.Load,
	}, inv, nil
}

// watchedFor is how long the invocation took, or has taken so far, by its
// own clock: it started before xf began to watch it.
func watchedFor(started time.Time, rep *project.RunReport) time.Duration {
	if started.IsZero() {
		return 0
	}
	end := time.Now()
	if rep != nil && !rep.Ended.IsZero() {
		end = rep.Ended
	}
	return max(end.Sub(started), 0)
}
