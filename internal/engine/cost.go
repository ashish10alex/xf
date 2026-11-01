package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// costTimeout bounds one cost check: a compile of the merge-base, and two
// dry runs of every changed action.
const costTimeout = 30 * time.Minute

// ErrNoCost is returned by QueryCost when it is off.
var ErrNoCost = errors.New("query cost is not available")

// CostFunc estimates what the Changed Actions of res scan, as req asks,
// telling progress how far it got (see cost.Run). g is the graph the
// working tree's Changed Actions were worked out from; nil for the pushed
// commit's, whose compile is res.HeadJSON.
type CostFunc func(ctx context.Context, req CostRequest, res *changes.Result, g *project.Graph, progress func(cost.Progress)) (*cost.Report, error)

// CostRequest asks for the query cost of the Changed Actions.
type CostRequest struct {
	// DefaultBranch and Head are as for changes.Request.
	DefaultBranch string       `json:"defaultBranch"`
	Head          changes.Head `json:"head"`
	// Refresh checks again even when a report on the same changes is
	// cached.
	Refresh bool `json:"refresh,omitempty"`
	// ProdCompilerOptions are the compiler options of the project's prod
	// runs: the dry runs are of the project compiled with them, so they
	// read the tables prod runs read; "" dry runs the project as xf
	// compiles it.
	ProdCompilerOptions string `json:"prodCompilerOptions,omitempty"`
	// DbtProdTarget is the profile target of a dbt project's prod runs:
	// the dry runs are of the project compiled with it; "" dry runs the
	// project as xf compiles it.
	DbtProdTarget string `json:"dbtProdTarget,omitempty"`
	// Unset is the setting that picks the tables the dry runs read,
	// "prodCompilerOptions" or "dbtProdTarget", when the caller has it set
	// nowhere, not even to "": the report then says it used the defaults
	// (cost.Report.Unset).
	Unset string `json:"unset,omitempty"`
	// Defer is whether defer to prod is on, which the engine sets: a table
	// the branch adds is then read from dev when prod doesn't have it.
	Defer bool `json:"-"`
}

type costKey struct {
	branch   string
	head     changes.Head
	prod     string
	dbt      string
	deferred bool
}

// costRun is one cost check.
type costRun = checkRun[cost.Report, cost.Progress]

// QueryCost estimates what the Changed Actions req asks for would scan in
// BigQuery, and what they scanned at the merge-base (see package cost). The
// Changed Actions are worked out as ChangedActions does with Compile, so it
// refuses while the working tree has compile errors. As with ColumnImpact,
// a report is kept per branch and head and returned at once when asked
// again, marked stale when the changes have moved on since, until
// req.Refresh checks again; a check under way is joined rather than started
// twice, and carries on when the caller stops waiting. progress, when not
// nil, hears how far it got.
func (e *Engine) QueryCost(ctx context.Context, req CostRequest, progress func(cost.Progress)) (*cost.Report, error) {
	if e.cfg.Cost == nil {
		return nil, ErrNoCost
	}
	res, g, err := e.changedActions(ctx, changes.Request{DefaultBranch: req.DefaultBranch, Head: req.Head, Compile: true})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("the base was not compiled")
	}
	sig := checkSignature(res, g)

	e.mu.Lock()
	req.Defer = e.state.Defer
	key := costKey{req.DefaultBranch, req.Head, req.ProdCompilerOptions, req.DbtProdTarget, req.Defer}
	run := e.costs[key]
	if run == nil || req.Refresh && (run.sig != sig || !run.running()) {
		run = e.startCost(key, sig, req, res, g)
	}
	e.mu.Unlock()

	// The report is kept for callers with other settings: say it of a copy.
	if progress != nil && req.Unset != "" {
		told := progress
		progress = func(p cost.Progress) {
			if p.Partial != nil {
				r := *p.Partial
				r.Unset = req.Unset
				p.Partial = &r
			}
			told(p)
		}
	}
	report, err := run.wait(ctx, progress)
	if err != nil {
		return nil, err
	}
	r := *report
	r.Unset = req.Unset
	r.Stale = report.Signature != sig
	return &r, nil
}

// checkSignature is what a report on res is a report on: impact.Signature
// when the graph is known, else the commits compared.
func checkSignature(res *changes.Result, g *project.Graph) string {
	if g == nil {
		return string(res.Head) + "\n" + res.MergeBase + "\n" + res.HeadSHA
	}
	return impact.Signature(res, g)
}

// startCost starts estimating res and makes it key's run. Call it with
// e.mu held.
func (e *Engine) startCost(key costKey, sig string, req CostRequest, res *changes.Result, g *project.Graph) *costRun {
	run := newCheckRun[cost.Report, cost.Progress](sig)
	e.costs[key] = run
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), costTimeout)
		defer cancel()
		// What is told ahead of the report carries the signature too.
		publish := func(p cost.Progress) {
			if p.Partial != nil {
				r := *p.Partial
				r.Signature = sig
				p.Partial = &r
			}
			run.publish(p)
		}
		report, err := e.cfg.Cost(ctx, req, res, g, publish)
		if err == nil && report == nil {
			err = errors.New("the cost check returned no report")
		}
		if err == nil {
			r := *report
			r.Signature = sig
			report = &r
		}
		run.mu.Lock()
		run.report, run.err = report, err
		run.mu.Unlock()
		if err != nil {
			// A failed check is never kept: the next request tries again.
			e.mu.Lock()
			if e.costs[key] == run {
				delete(e.costs, key)
			}
			e.mu.Unlock()
		}
		close(run.done)
	}()
	return run
}
