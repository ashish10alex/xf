// Package runhistory keeps the runs xf started in a project, and what their
// BigQuery jobs cost, so they can be looked through later. The runs of a
// project are one JSON file, the last Keep of them, in a directory beside
// the sessions (see package session).
package runhistory

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runjobs"
)

// Keep is how many runs of a project are kept.
const Keep = 50

// saveInterval is how often a run that is going is kept again between its
// lines: that says it is still followed, and keeps its jobs that came and
// went since.
const saveInterval = 5 * time.Second

// staleAfter is how long a run may go without being kept again before it
// is taken to be followed by nobody.
const staleAfter = 4 * saveInterval

// How a run was started.
const (
	ViaCLI = "cli"
	ViaAPI = "api"
	// ViaExternal is a workflow invocation someone else started (a
	// schedule, a colleague, the console), which xf was asked to watch.
	ViaExternal = "external"
)

// StateInterrupted is the state of a run xf stopped following before it
// ended: one that was running when xf was closed or killed.
const StateInterrupted = "INTERRUPTED"

// Entry is a kept run.
type Entry struct {
	// ID tells the run from the project's others.
	ID string `json:"id"`
	// Via is how the run was started: ViaCLI, ViaAPI or ViaExternal.
	Via     string    `json:"via"`
	Cmdline string    `json:"cmdline,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// State is RUNNING until the run ends, then SUCCEEDED, FAILED or
	// CANCELLED, or StateInterrupted.
	State string `json:"state"`
	// Updated is when the xf that follows the run last kept it.
	Updated time.Time `json:"updated,omitzero"`
	// Project and Location are where a CLI run's jobs are, to look them up.
	Project  string `json:"project,omitempty"`
	Location string `json:"location,omitempty"`
	// Deferred are the upstream tables the run read from prod through
	// Proxy Views, "dev → prod" (defer to prod, ADR 0031).
	Deferred []string `json:"deferred,omitempty"`
	// Report is the run's actions and jobs; nil for a run that had none.
	Report *project.RunReport `json:"report,omitempty"`
}

// Jobs is how many BigQuery jobs the run has.
func (e Entry) Jobs() int {
	n := 0
	if e.Report != nil {
		for _, a := range e.Report.Actions {
			if a.JobID != "" {
				n++
			}
		}
	}
	return n
}

// Duration is how long the run took; ok is false while it has not ended.
func (e Entry) Duration() (time.Duration, bool) {
	if e.Ended.Before(e.Started) || e.Started.IsZero() {
		return 0, false
	}
	return e.Ended.Sub(e.Started), true
}

// NewID is an ID for a new run: eight hex digits, short enough to prefix
// the IDs of its BigQuery jobs with.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Store keeps the runs of projects in Dir.
type Store struct {
	Dir string
	// mu keeps this process's saves one at a time.
	mu sync.Mutex
}

type file struct {
	Root string  `json:"root"`
	Runs []Entry `json:"runs"`
}

func (s *Store) path(root string) string {
	root = project.CanonicalDrive(root)
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.Dir, filepath.Base(root)+"-"+hex.EncodeToString(sum[:8])+".json")
}

// Load returns the runs kept for the project in root, newest first.
func (s *Store) Load(root string) ([]Entry, error) {
	data, err := os.ReadFile(s.path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Runs, nil
}

// Put keeps e among the runs of the project in root, in place of the run
// with its ID if there is one. The file is read again first, so that the
// runs another xf has put there since are kept too.
func (s *Store) Put(root string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A file that can't be read is replaced: history is a convenience.
	runs, _ := s.Load(root)
	if i := slices.IndexFunc(runs, func(r Entry) bool { return r.ID == e.ID }); i >= 0 {
		runs[i] = e
	} else {
		runs = append(runs, e)
	}
	slices.SortStableFunc(runs, func(x, y Entry) int { return y.Started.Compare(x.Started) })
	runs = runs[:min(len(runs), Keep)]

	return s.write(root, runs)
}

// write replaces the project's file with runs, whole, so a crash mid-write
// never leaves half a history.
func (s *Store) write(root string, runs []Entry) error {
	data, err := json.Marshal(file{Root: root, Runs: runs})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".runs-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(root))
}

// Clear forgets the runs kept for the project in root, but for those an xf
// still follows: a run that is going stays until it has ended.
func (s *Store) Clear(root string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, _ := s.Load(root)
	runs = slices.DeleteFunc(runs, func(e Entry) bool { return e.State != "RUNNING" || e.Stale(now) })
	if len(runs) == 0 {
		if err := os.Remove(s.path(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return s.write(root, runs)
}

// Record returns run, kept in s as e while it goes: when it starts, each
// time its report changes, and when it ends. e needs its ID and Via; the
// rest is taken from the run.
func (s *Store) Record(root string, e Entry, run *project.Run) *project.Run {
	lines := make(chan string)
	done := make(chan project.RunResult, 1)
	e.Cmdline, e.State = cmp.Or(e.Cmdline, run.Cmdline), "RUNNING"
	if e.Started.IsZero() {
		e.Started = time.Now()
	}
	var saved *project.RunReport
	save := func() {
		if run.Report != nil {
			if r := run.Report(); r != nil {
				e.Report, saved = r, r
			}
		}
		e.Updated = time.Now()
		_ = s.Put(root, e)
	}
	save()
	go func() {
		// The report of a run with jobs still running changes between its
		// lines too.
		tick := time.NewTicker(saveInterval)
		defer tick.Stop()
		for open := true; open; {
			var l string
			isLine := false
			select {
			case l, open = <-run.Lines:
				isLine = open
				// A line follows the change it tells of, so the report
				// is looked at before the line is passed on.
				if run.Report != nil && run.Report() != saved {
					save()
				}
			case <-tick.C:
				save()
			}
			if isLine {
				lines <- l
			}
		}
		res := <-run.Done
		e.Ended, e.State = e.Started.Add(res.Duration), ResultState(res)
		save()
		close(lines)
		done <- res
	}()
	out := *run
	out.Lines, out.Done = lines, done
	return &out
}

// ResultState is the state of a run that ended in res.
func ResultState(res project.RunResult) string {
	switch {
	case res.Detached:
		return StateInterrupted
	case res.State != "":
		return res.State
	case res.Canceled:
		return "CANCELLED"
	case res.ExitCode == 0 && res.Err == nil:
		return "SUCCEEDED"
	}
	return "FAILED"
}

// Stale says e is a run nobody follows any more though it never ended: its
// jobs may have ended since (see Refresh). The xf that follows a run keeps
// it again every few seconds, so one kept that lately is not stale.
func (e Entry) Stale(now time.Time) bool {
	return e.State == "RUNNING" && now.Sub(e.Updated) > staleAfter
}

// PutRefreshed keeps now in place of was, a stale run of the project in
// root as it was read, unless the run has been kept again since: then an xf
// follows it after all, and what that one kept stands. It returns the run
// as it is kept.
func (s *Store) PutRefreshed(root string, was, now Entry) (Entry, error) {
	runs, err := s.Load(root)
	if err != nil {
		return was, err
	}
	i := slices.IndexFunc(runs, func(r Entry) bool { return r.ID == was.ID })
	if i < 0 {
		return now, nil
	}
	if runs[i].State != was.State || !runs[i].Updated.Equal(was.Updated) {
		return runs[i], nil
	}
	return now, s.Put(root, now)
}

// Refresh looks up the jobs of a stale CLI run that had not ended when it
// was last kept, and returns the run as it is known now: its jobs that have
// ended with what they did, and the run interrupted once none is left
// running. A run that is not the CLI's comes back as it is.
func Refresh(ctx context.Context, jobs runjobs.JobReader, e Entry) Entry {
	if e.Via != ViaCLI || e.Report == nil {
		if e.Via == ViaCLI {
			e.State = StateInterrupted
		}
		return e
	}
	rep := *e.Report
	rep.Actions = slices.Clone(rep.Actions)
	running := false
	for i, a := range rep.Actions {
		if a.JobID == "" || a.Job != nil || a.JobErr != "" {
			continue
		}
		ref := dataformapi.ParseJobID(a.JobID, e.Project)
		ref.Location = cmp.Or(ref.Location, e.Location)
		j, err := lookUp(ctx, jobs, ref)
		switch {
		case err != nil:
			a.JobErr = err.Error()
			if a.State == "RUNNING" {
				a.State = StateInterrupted
			}
		case !j.Done:
			running = true
		default:
			a.Job = runjobs.Stats(j)
			if a.State == "RUNNING" {
				a.State = "SUCCEEDED"
				if j.Failed {
					a.State = "FAILED"
				}
			}
			if j.Ended.After(rep.Ended) {
				rep.Ended = j.Ended
			}
		}
		rep.Actions[i] = a
	}
	e.Report = &rep
	if !running {
		e.State, rep.State = StateInterrupted, StateInterrupted
		e.Ended = cmp.Or(rep.Ended, e.Started)
	}
	return e
}

func lookUp(ctx context.Context, jobs runjobs.JobReader, ref dataformapi.JobRef) (*bigquery.Job, error) {
	if jobs == nil {
		return nil, errors.New("no BigQuery client")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return jobs.Job(ctx, ref.Project, ref.Location, ref.ID)
}
