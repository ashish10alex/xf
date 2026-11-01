package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// impactTimeout bounds one column impact check: dry runs of every changed
// table and a few lineage searches per changed column.
const impactTimeout = 30 * time.Minute

// ErrNoImpact is returned by ColumnImpact when it is off.
var ErrNoImpact = errors.New("column impact is not available")

// ImpactFunc works out the column impact of res, a diff of g against the
// merge-base, as req asks, telling progress how far it got (see
// impact.Run). g is the graph the working tree's Changed Actions were worked
// out from; nil for the pushed commit's, whose compile is res.HeadJSON.
type ImpactFunc func(ctx context.Context, req ImpactRequest, res *changes.Result, g *project.Graph, progress func(impact.Progress)) (*impact.Report, error)

// ImpactRequest asks for the column impact of the Changed Actions.
type ImpactRequest struct {
	// DefaultBranch and Head are as for changes.Request.
	DefaultBranch string       `json:"defaultBranch"`
	Head          changes.Head `json:"head"`
	// Refresh checks again even when a report on the same changes is
	// cached.
	Refresh bool `json:"refresh,omitempty"`
	// DevCompilerOptions are the compiler options of the user's dev runs,
	// whose lineage is folded into the actions they run; "" folds none.
	DevCompilerOptions string `json:"devCompilerOptions,omitempty"`
	// ProdCompilerOptions are the compiler options of the project's prod
	// runs, whose tables the changes are compared with; "" compares with
	// the default targets.
	ProdCompilerOptions string `json:"prodCompilerOptions,omitempty"`
	// DbtProdTarget is the profile target of a dbt project's prod runs:
	// the changes are compiled with it and compared with the tables it
	// builds; "" compares with the tables of the target xf compiles with.
	DbtProdTarget string `json:"dbtProdTarget,omitempty"`
	// Unset is the setting that picks the tables compared with,
	// "prodCompilerOptions" or "dbtProdTarget", when the caller has it set
	// nowhere, not even to "": the report then says it compared with the
	// default targets (impact.Report.Unset).
	Unset string `json:"unset,omitempty"`
}

type impactKey struct {
	branch string
	head   changes.Head
	dev    string
	prod   string
	dbt    string
}

// impactRun is one column impact check.
type impactRun = checkRun[impact.Report, impact.Progress]

// ColumnImpact works out the column impact of the Changed Actions req asks
// for: the columns they drop or retype against the tables in BigQuery, and
// who reads them (see package impact). The Changed Actions are worked out as
// ChangedActions does with Compile, so it refuses while the working tree
// has compile errors. A report is kept per branch and head: asked again, it
// is returned at once, marked stale when the changes have moved on since,
// until req.Refresh checks again. A check under way is joined rather than
// started twice, and carries on when the caller stops waiting, so the next
// caller finds it. progress, when not nil, hears how far it got.
func (e *Engine) ColumnImpact(ctx context.Context, req ImpactRequest, progress func(impact.Progress)) (*impact.Report, error) {
	if e.cfg.Impact == nil {
		return nil, ErrNoImpact
	}
	res, g, err := e.changedActions(ctx, changes.Request{DefaultBranch: req.DefaultBranch, Head: req.Head, Compile: true})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("the base was not compiled")
	}
	sig := checkSignature(res, g)
	key := impactKey{req.DefaultBranch, req.Head, req.DevCompilerOptions, req.ProdCompilerOptions, req.DbtProdTarget}

	e.mu.Lock()
	run := e.impacts[key]
	if run == nil || req.Refresh && (run.sig != sig || !run.running()) {
		run = e.startImpact(key, sig, req, res, g)
	}
	e.mu.Unlock()

	report, err := run.wait(ctx, progress)
	if err != nil {
		return nil, err
	}
	// The report is kept for callers with other settings: say it of a copy.
	r := *report
	r.Unset = req.Unset
	r.Stale = report.Signature != sig
	return &r, nil
}

// startImpact starts checking the impact of res and makes it key's run.
// Call it with e.mu held.
func (e *Engine) startImpact(key impactKey, sig string, req ImpactRequest, res *changes.Result, g *project.Graph) *impactRun {
	run := newCheckRun[impact.Report, impact.Progress](sig)
	e.impacts[key] = run
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), impactTimeout)
		defer cancel()
		report, err := e.cfg.Impact(ctx, req, res, g, run.publish)
		if err == nil && report == nil {
			err = errors.New("column impact returned no report")
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
			if e.impacts[key] == run {
				delete(e.impacts, key)
			}
			e.mu.Unlock()
		}
		close(run.done)
	}()
	return run
}
