package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// dryRunTimeout bounds one dry run; BigQuery usually answers in about a second.
const dryRunTimeout = time.Minute

// dryRunEntry is a dry run started for one script, and its result once done.
type dryRunEntry struct {
	done bool
	res  *bigquery.DryRun
	err  error
}

type dryRunMsg struct {
	key string
	res *bigquery.DryRun
	err error
}

// dryRunKey identifies a dry run by what it sends, so an action whose SQL
// did not change keeps its result across recompiles.
func dryRunKey(project, location, sql string) string {
	return project + "\x00" + location + "\x00" + sql
}

// dryRunTarget is where Dataform would run a's jobs.
func (m Model) dryRunTarget(a *project.Action) (project, location string) {
	if m.graph == nil {
		return a.Target.Database, ""
	}
	return m.graph.DryRunTarget(a)
}

// uncompiled reports whether the graph's SQL is uncompiled templates, which
// BigQuery can't plan: a dbt v2 project with hooks is only parsed, and its
// compile's Notice says so.
func (m Model) uncompiled() bool { return m.graph != nil && m.graph.Notice != "" }

// startDryRuns dry-runs each action in the Actions pane that has no result
// yet. Update calls it after every message, so selecting an action or a
// recompile that changed its SQL starts them. An outdated saved graph's SQL
// waits for the compile replacing it, and so does an action of a staged
// graph whose SQL is yet to be compiled.
func (m *Model) startDryRuns() tea.Cmd {
	if m.deps.DryRun == nil || m.graph == nil || m.outdated != "" || m.uncompiled() {
		return nil
	}
	var cmds []tea.Cmd
	for _, a := range m.paneActions() {
		project, location := m.dryRunTarget(a)
		if project == "" || m.sqlNote(a) != "" {
			continue
		}
		for _, q := range a.DryRunQueries() {
			key := dryRunKey(project, location, q.SQL)
			if _, ok := m.dryRuns[key]; ok {
				continue
			}
			m.dryRuns[key] = &dryRunEntry{}
			cmds = append(cmds, dryRunCmd(m.deps.DryRun, key, project, location, q.SQL))
		}
	}
	return tea.Batch(cmds...)
}

func dryRunCmd(dryRun func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error), key, project, location, sql string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), dryRunTimeout)
		defer cancel()
		res, err := dryRun(ctx, project, location, sql)
		return dryRunMsg{key: key, res: res, err: err}
	}
}

func (m *Model) handleDryRun(msg dryRunMsg) {
	if e := m.dryRuns[msg.key]; e != nil {
		e.done, e.res, e.err = true, msg.res, msg.err
	}
}

// forgetFailedDryRuns drops failed dry runs so they are tried again: after
// a recompile, which is also what c does once you have logged in, or a run
// that may have built the tables they read.
func (m *Model) forgetFailedDryRuns() {
	for k, e := range m.dryRuns {
		if e.done && e.err != nil {
			delete(m.dryRuns, k)
		}
	}
}
