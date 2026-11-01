package web

// Recent scratch results: the last few runs that returned rows, kept as
// their BigQuery jobs in runs.json beside the scratches, so their results
// can be opened again after a switch, a reload or a restart (ADR 0029).
// No row is written: BigQuery keeps each result in a temporary table for
// about a day, and reading it back from there is free.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
)

const (
	// scratchRunsFile holds the recent runs, in the scratches' folder.
	scratchRunsFile = "runs.json"
	// maxScratchRuns is how many runs are kept, the newest first.
	maxScratchRuns = 5
)

// scratchRunsMu orders the reads and writes of runs.json.
var scratchRunsMu sync.Mutex

// scratchRun is one run whose result can be opened again.
type scratchRun struct {
	// Scratch is the scratch it ran from.
	Scratch string          `json:"scratch"`
	Job     bigquery.JobRef `json:"job"`
	// SQL is what ran: the selection or the whole file, as it was then.
	SQL           string    `json:"sql"`
	Started       time.Time `json:"started"`
	Ended         time.Time `json:"ended"`
	Rows          int64     `json:"rows"`
	StatementType string    `json:"statementType,omitempty"`
}

func (p *projectState) runsPath() string { return filepath.Join(p.scratchFolder(), scratchRunsFile) }

// readRuns is the kept runs, newest first; a missing or broken file is none.
func (p *projectState) readRuns() []scratchRun {
	data, err := os.ReadFile(p.runsPath())
	if err != nil {
		return []scratchRun{}
	}
	var f struct {
		Runs []scratchRun `json:"runs"`
	}
	if json.Unmarshal(data, &f) != nil || f.Runs == nil {
		return []scratchRun{}
	}
	return f.Runs
}

// writeRuns replaces the file, through a temporary one so a crash can't
// leave half of it.
func (p *projectState) writeRuns(runs []scratchRun) error {
	data, err := json.MarshalIndent(struct {
		Runs []scratchRun `json:"runs"`
	}{runs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.scratchFolder(), 0o755); err != nil {
		return err
	}
	tmp := p.runsPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.runsPath())
}

func (p *projectState) scratchRunsList(w http.ResponseWriter, r *http.Request) {
	scratchRunsMu.Lock()
	runs := p.readRuns()
	scratchRunsMu.Unlock()
	writeJSON(w, runs)
}

// scratchRunsAdd keeps a run that returned rows, dropping the oldest past
// maxScratchRuns, and answers the runs kept.
func (p *projectState) scratchRunsAdd(w http.ResponseWriter, r *http.Request) {
	var run scratchRun
	if !readJSON(w, r, &run) {
		return
	}
	if _, ok := p.scratchPath(run.Scratch); !ok {
		writeError(w, errors.New("not a scratch: "+run.Scratch))
		return
	}
	if run.Job.Project == "" || run.Job.ID == "" {
		writeError(w, errors.New("a run needs its job's project and id"))
		return
	}
	if len(run.SQL) > maxScratchBytes {
		writeError(w, errors.New("a scratch holds 1 MiB of SQL at most"))
		return
	}
	scratchRunsMu.Lock()
	defer scratchRunsMu.Unlock()
	runs := slices.DeleteFunc(p.readRuns(), func(x scratchRun) bool { return x.Job == run.Job })
	runs = append([]scratchRun{run}, runs...)
	runs = runs[:min(len(runs), maxScratchRuns)]
	if err := p.writeRuns(runs); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, runs)
}

// dropScratchRuns forgets the runs of a deleted scratch.
func (p *projectState) dropScratchRuns(id string) {
	scratchRunsMu.Lock()
	defer scratchRunsMu.Unlock()
	runs := p.readRuns()
	kept := slices.DeleteFunc(slices.Clone(runs), func(x scratchRun) bool { return x.Scratch == id })
	if len(kept) != len(runs) {
		_ = p.writeRuns(kept)
	}
}
