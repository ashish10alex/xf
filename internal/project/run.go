package project

import "time"

// RunOptions selects what a run executes.
type RunOptions struct {
	// Actions are the actions to run; --actions takes them all.
	Actions []ActionID
	// Names are the same actions' RunName, in order, for a tool that
	// selects by name (dbt); nil when the actions have none.
	Names []string
	// Tags selects by tag instead: every action with any of them, as the
	// tool finds them. A run has Tags or Actions, never both.
	Tags              []string
	IncludeDeps       bool
	IncludeDependents bool
	FullRefresh       bool
	// JobPrefix is put in the IDs of the run's BigQuery jobs, so that they
	// can be found while they run (see package runjobs); "" leaves the IDs
	// as the tool makes them. Dataform's CLI only.
	JobPrefix string
	// Deferred are the upstream tables the run reads from prod through
	// Proxy Views, "dev → prod" (ADR 0031), kept in the run history; the
	// tool isn't told.
	Deferred []string
	// DeferState is where a dbt run's --defer-state reads prod's manifest
	// (ADR 0031); "" doesn't defer.
	DeferState string
	// Args are the tool's own run arguments as xf run was given them (ADR
	// 0038): when set, they select the run in place of Actions, Tags,
	// IncludeDeps, IncludeDependents and FullRefresh, which then only say
	// what it builds. xf adds what its own runs add, unless Args give it.
	Args []string
	// Color asks the tool for colour even though its output is a pipe,
	// and keeps the colour codes (SGR sequences) in the run's Lines for a
	// reader that shows them; other escape sequences are still removed.
	// Local runs only.
	Color bool
}

// Run is a started run of a project's actions. Read Lines until it is closed, then read
// the single RunResult from Done.
type Run struct {
	Cmdline string
	Lines   <-chan string
	Done    <-chan RunResult
	// Kill force-stops the run and everything it started. Cancelling the
	// context is the graceful way; Kill is for when that takes too long.
	Kill func()
	// URL is where a run in GCP (see package dataformapi) can be followed,
	// "" until it is known; nil for a local run.
	URL func() string
	// Report is the run's actions and what each cost, as of now: nil for a
	// run that reports none (a local run), and a nil report until the run
	// has actions. Every line of the run follows the changes it tells of,
	// so a reader that looks after each line, and once more after Done,
	// misses nothing.
	Report func() *RunReport
}

// RunResult is how a run ended.
type RunResult struct {
	ExitCode int
	Err      error // set when the process could not run or died unexpectedly
	Canceled bool
	Duration time.Duration
	// State is how a run in GCP ended (SUCCEEDED, FAILED, CANCELLED); empty
	// for a local run, which has an ExitCode instead.
	State string
	// Detached means xf stopped watching a run in GCP, which carries on.
	Detached bool
}

// RunReport is a run's actions and what each cost, for a table of them. It
// is plain data: the TUI draws it, and it travels as JSON.
type RunReport struct {
	// State is the run's own state, as its tool names it (RUNNING,
	// SUCCEEDED, FAILED, CANCELLED, CANCELING for a workflow invocation).
	State string `json:"state"`
	// URL is where the run can be followed, "" when nowhere.
	URL string `json:"url,omitempty"`
	// Started and Ended are the run's own times: actions run in parallel,
	// so their durations don't add up to it. Zero until known.
	Started time.Time   `json:"started"`
	Ended   time.Time   `json:"ended"`
	Actions []RunAction `json:"actions"`
}

// RunAction is one action of a run.
type RunAction struct {
	Target Target `json:"target"`
	// State is PENDING, RUNNING, SKIPPED, DISABLED, SUCCEEDED, CANCELLED
	// or FAILED.
	State         string    `json:"state"`
	FailureReason string    `json:"failureReason,omitempty"`
	Started       time.Time `json:"started"`
	Ended         time.Time `json:"ended"`
	// JobID is the BigQuery job the action ran, "" when it ran none, and
	// JobURL its page in the Cloud console.
	JobID  string `json:"jobId,omitempty"`
	JobURL string `json:"jobUrl,omitempty"`
	// Job is what the job did, nil until looked up; JobErr says why it
	// could not be.
	Job    *JobStats `json:"job,omitempty"`
	JobErr string    `json:"jobErr,omitempty"`
}

// Done says whether the action has ended.
func (a RunAction) Done() bool {
	switch a.State {
	case "SUCCEEDED", "FAILED", "CANCELLED", "SKIPPED", "DISABLED":
		return true
	}
	return false
}

// Duration is how long the action took: its BigQuery job's time when that
// is known, else its own. ok is false for an action that hasn't ended.
func (a RunAction) Duration() (d time.Duration, ok bool) {
	if a.Job != nil && !a.Job.Started.IsZero() && !a.Job.Ended.Before(a.Job.Started) {
		return a.Job.Ended.Sub(a.Job.Started), true
	}
	if a.Started.IsZero() || a.Ended.Before(a.Started) {
		return 0, false
	}
	return a.Ended.Sub(a.Started), true
}

// JobStats is what a BigQuery job did.
type JobStats struct {
	Location string `json:"location,omitempty"`
	// BytesBilled is what on-demand pricing charges for, and CostUSD its
	// on-demand price; BytesProcessed is what the job read.
	BytesBilled    int64     `json:"bytesBilled"`
	BytesProcessed int64     `json:"bytesProcessed"`
	CostUSD        float64   `json:"costUsd"`
	SlotMs         int64     `json:"slotMs"`
	Started        time.Time `json:"started"`
	Ended          time.Time `json:"ended"`
}

// RunTotals are the sums of a report's job stats.
type RunTotals struct {
	// Jobs is how many actions have job stats.
	Jobs           int     `json:"jobs"`
	BytesBilled    int64   `json:"bytesBilled"`
	BytesProcessed int64   `json:"bytesProcessed"`
	CostUSD        float64 `json:"costUsd"`
	SlotMs         int64   `json:"slotMs"`
}

// Totals sums the job stats looked up so far.
func (r *RunReport) Totals() RunTotals {
	var t RunTotals
	for _, a := range r.Actions {
		if a.Job == nil {
			continue
		}
		t.Jobs++
		t.BytesBilled += a.Job.BytesBilled
		t.BytesProcessed += a.Job.BytesProcessed
		t.CostUSD += a.Job.CostUSD
		t.SlotMs += a.Job.SlotMs
	}
	return t
}

// Duration is how long the run took, start to end. ok is false until it
// has ended.
func (r *RunReport) Duration() (d time.Duration, ok bool) {
	if r.Started.IsZero() || r.Ended.Before(r.Started) {
		return 0, false
	}
	return r.Ended.Sub(r.Started), true
}
