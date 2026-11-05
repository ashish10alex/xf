package frontend

import (
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

// The checks before a run with defer to prod (ADRs 0031 and 0038), as the
// VS Code extension makes them. xf web asks about what they find in
// dialogs; xf run refuses, and its flags give the answers.

// Answers to the checks: what the run may do all the same.
const (
	// DeferAnswerChanged adds the Stale Deferrals to the run, built in dev
	// before what reads them.
	DeferAnswerChanged = "changed"
	// DeferAnswerStale runs with Stale Deferrals all the same.
	DeferAnswerStale = "stale"
	// DeferAnswerLeftover runs with defer off though Proxy Views read prod.
	DeferAnswerLeftover = "leftover"
	// DeferAnswerAPI lets a deferred API run read prod as the repository's
	// service account.
	DeferAnswerAPI = "api"
)

// DeferStop is why a run can't start before a question is answered.
type DeferStop int

const (
	// DeferGo: nothing to ask.
	DeferGo DeferStop = iota
	// DeferStopLeftover: defer is off, but upstream Proxy Views read prod.
	DeferStopLeftover
	// DeferStopBlocked: some upstream tables can't be read from prod.
	DeferStopBlocked
	// DeferStopStale: some upstream tables read from prod change on the
	// branch.
	DeferStopStale
	// DeferStopAPI: an API run reads prod as an account xf can't check.
	DeferStopAPI
)

// DeferRun is a run to check.
type DeferRun struct {
	// On says defer to prod is on for the run.
	On bool
	// Deferral is what it reads from prod, nil when nothing; Proxies are
	// the upstream Proxy Views (looked up with defer off too).
	Deferral *deferral.Deferral
	Proxies  []project.Target
	// MissingDatasets says which dev datasets ("project.dataset") of the
	// tables read from prod don't exist; nil when not known (dbt).
	MissingDatasets func(entries []deferral.Entry) map[string]bool
	// Tags says the run selects by tag, so actions can't be added to it.
	Tags bool
	// API says it runs in GCP, and APIApproved that the user said its
	// account may read prod.
	API, APIApproved bool
	// Answers are the Defer* answers given so far.
	Answers map[string]bool
}

// DeferCheck is what the check of a DeferRun found.
type DeferCheck struct {
	// Stop is DeferGo when the run can start as it is, or with Add.
	Stop DeferStop
	// Detail are the tables (or datasets) that stopped it, worded.
	Detail []string
	// Proxies are the leftover Proxy Views, for DeferStopLeftover.
	Proxies []project.Target
	// Add are actions to add to the run before it starts: functions and
	// procedures it reads (AddRoutines), or, with DeferAnswerChanged, the
	// Stale Deferrals. The run is then checked again.
	Add         []project.ActionID
	AddRoutines bool
	// Entries are the tables to read from prod, through Proxy Views in
	// Dataform, when the run can start.
	Entries []deferral.Entry
}

// CheckDeferredRun checks run as the extension does, in its order:
// leftover Proxy Views with defer off, then functions and procedures to
// build, tables that can't be read from prod, Stale Deferrals and an API
// run's account.
func CheckDeferredRun(run DeferRun) DeferCheck {
	if !run.On {
		if len(run.Proxies) > 0 && !run.Answers[DeferAnswerLeftover] {
			return DeferCheck{Stop: DeferStopLeftover, Proxies: run.Proxies, Detail: targetIDs(run.Proxies)}
		}
		return DeferCheck{}
	}
	d := run.Deferral
	if d == nil {
		return DeferCheck{}
	}
	// A view can't stand in for a function or procedure, and building one
	// is cheap, so the run builds the missing ones in dev.
	var routines []project.ActionID
	for _, e := range d.Entries {
		if e.Routine {
			routines = append(routines, e.Dev.ID())
		}
	}
	if len(routines) > 0 && !run.Tags {
		return DeferCheck{Add: routines, AddRoutines: true}
	}
	var deferred []deferral.Entry
	for _, e := range d.Entries {
		if e.Read() {
			deferred = append(deferred, e)
		}
	}
	var missing map[string]bool
	if run.MissingDatasets != nil {
		missing = run.MissingDatasets(deferred)
	}
	if blocked := DeferBlocked(d, missing); len(blocked) > 0 {
		return DeferCheck{Stop: DeferStopBlocked, Detail: blocked}
	}
	if stale := DeferStale(d); len(stale) > 0 && !run.Answers[DeferAnswerStale] {
		if run.Answers[DeferAnswerChanged] && !run.Tags {
			var ids []project.ActionID
			for _, e := range deferred {
				if e.Stale {
					ids = append(ids, e.Dev.ID())
				}
			}
			return DeferCheck{Add: ids}
		}
		return DeferCheck{Stop: DeferStopStale, Detail: stale}
	}
	if len(deferred) > 0 && run.API && !run.APIApproved && !run.Answers[DeferAnswerAPI] {
		return DeferCheck{Stop: DeferStopAPI, Detail: DeferProdDatasets(d)}
	}
	return DeferCheck{Entries: deferred}
}

func targetIDs(ts []project.Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t.ID())
	}
	return out
}
