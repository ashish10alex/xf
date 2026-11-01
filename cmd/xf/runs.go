package main

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/runhistory"
	"github.com/ashish10alex/xf/internal/runjobs"
)

// keptRuns is a project's kept runs, as the TUI's history has them.
type keptRuns struct {
	store *runhistory.Store
	root  string

	mu sync.Mutex
	bq *bigquery.Client
}

func (k *keptRuns) List() ([]runhistory.Entry, error) { return k.store.Load(k.root) }

func (k *keptRuns) Refresh(ctx context.Context, e runhistory.Entry) (runhistory.Entry, error) {
	var jobs runjobs.JobReader
	if c := k.bigquery(); c != nil {
		jobs = c
	}
	return k.store.PutRefreshed(k.root, e, runhistory.Refresh(ctx, jobs, e))
}

func (k *keptRuns) Clear() error { return k.store.Clear(k.root, time.Now()) }

// bigquery is a BigQuery client, nil while there are no credentials for
// one: a client is tried for again the next time.
func (k *keptRuns) bigquery() *bigquery.Client {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.bq == nil {
		// Not a run's context: the client outlives the run it is made for.
		if c, err := bigquery.NewClient(context.Background()); err == nil {
			k.bq = c
		}
	}
	return k.bq
}

// addRunJobs has the TUI follow the BigQuery jobs of the runs of a Dataform
// project, and keep its runs: a CLI run gets a report of its jobs, as an
// API run has, and both are put in the project's history.
func addRunJobs(deps *frontend.Deps, root string, b dataform.Backend, client *remote.Client) {
	k := &keptRuns{root: root}
	if dir := sessionDir(); dir != "" {
		k.store = &runhistory.Store{Dir: filepath.Join(filepath.Dir(dir), "runs")}
		deps.Runs = k
	}
	record := func(e runhistory.Entry, run *project.Run) *project.Run {
		if k.store == nil {
			return run
		}
		return k.store.Record(root, e, run)
	}

	// The CLI is asked once, while the user finds something to run,
	// whether it can prefix the IDs of a run's jobs.
	var prefixes atomic.Bool
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		prefixes.Store(b.PrefixesJobs(ctx))
	}()
	// next is the ID of the next CLI run, so that the command shown before
	// a run is the one that runs.
	var mu sync.Mutex
	next := runhistory.NewID()
	prefixed := func(o project.RunOptions, id string) project.RunOptions {
		if prefixes.Load() {
			o.JobPrefix = "xf-" + id
		}
		return o
	}

	if start := deps.StartRun; start != nil {
		deps.RunCommand = func(o project.RunOptions) string {
			mu.Lock()
			defer mu.Unlock()
			return b.RunCommand(root, prefixed(o, next))
		}
		deps.StartRun = func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
			mu.Lock()
			id := next
			next = runhistory.NewID()
			mu.Unlock()
			o = prefixed(o, id)
			run, err := start(ctx, o)
			if err != nil {
				return nil, err
			}
			// The CLI runs its jobs in the project of its credentials file,
			// else in the Application Default Credentials' own.
			opts := runjobs.Options{}
			opts.Project, opts.Location = dataform.JobTarget(root)
			bq := k.bigquery()
			if bq != nil {
				opts.Jobs, opts.Active = bq, bq
				opts.Project = cmp.Or(opts.Project, os.Getenv("GOOGLE_CLOUD_PROJECT"), bq.QuotaProject())
			}
			if g := client.State().Graph; g != nil {
				opts.Project, opts.Location = cmp.Or(opts.Project, g.DefaultDatabase), cmp.Or(opts.Location, g.DefaultLocation)
				targets := make([]project.Target, 0, len(g.Actions))
				for _, a := range g.Actions {
					targets = append(targets, a.Target)
				}
				opts.Match = runjobs.Matcher(targets)
			}
			if o.JobPrefix != "" {
				opts.Prefix = runjobs.Prefix(o.JobPrefix)
			}
			e := runhistory.Entry{ID: id, Via: runhistory.ViaCLI, Project: opts.Project, Location: opts.Location, Deferred: o.Deferred}
			return record(e, runjobs.Track(run, opts)), nil
		}
	}
	if start := deps.StartAPIRun; start != nil {
		deps.StartAPIRun = func(ctx context.Context, opts dataformapi.Options) (*project.Run, error) {
			run, err := start(ctx, opts)
			if err != nil {
				return nil, err
			}
			return record(runhistory.Entry{ID: runhistory.NewID(), Via: runhistory.ViaAPI, Deferred: opts.Deferred}, run), nil
		}
	}
	if watch := deps.WatchWorkflow; watch != nil {
		deps.WatchWorkflow = func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
			run, inv, err := watch(ctx, ref, base)
			if err != nil {
				return nil, nil, err
			}
			// Watching it again keeps it once: the invocation's ID is the
			// run's, and it started when the invocation did.
			e := runhistory.Entry{ID: ref.ID, Via: runhistory.ViaExternal, Started: inv.Started()}
			return record(e, run), inv, nil
		}
	}
}
