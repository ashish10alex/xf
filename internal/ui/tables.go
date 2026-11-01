package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

const (
	// tableTimeout bounds one table lookup.
	tableTimeout = 30 * time.Second
	// tableMaxAge is how long a lookup's answer is shown before the table
	// is looked up again: something other than xf may have updated it.
	tableMaxAge = time.Minute
)

// tableEntry is the lookup of one action's table in BigQuery.
type tableEntry struct {
	loading bool
	// done says a lookup has finished: tbl and err are the last one's
	// answer, shown while the next is on its way.
	done bool
	at   time.Time // when the answer came; zero to look it up again
	tbl  *bigquery.Table
	err  error
}

type tableMsg struct {
	ref bigquery.TableRef
	tbl *bigquery.Table
	err error
}

func tableRef(a *project.Action) bigquery.TableRef {
	return bigquery.TableRef{Project: a.Target.Database, Dataset: a.Target.Schema, Table: a.Target.Name}
}

// startTableLookups looks up the table of each action in the Actions pane
// that has no answer yet, or an old one. Update calls it after every
// message, as it does startDryRuns.
func (m *Model) startTableLookups() tea.Cmd {
	if m.deps.Table == nil || m.graph == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, a := range m.paneActions() {
		if !hasTable(a) {
			continue
		}
		ref := tableRef(a)
		e := m.tables[ref]
		if e == nil {
			e = &tableEntry{}
			m.tables[ref] = e
		}
		if e.loading || e.done && m.deps.Now().Sub(e.at) < tableMaxAge {
			continue
		}
		e.loading = true
		cmds = append(cmds, tableCmd(m.deps.Table, ref))
	}
	return tea.Batch(cmds...)
}

func tableCmd(lookup func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error), ref bigquery.TableRef) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tableTimeout)
		defer cancel()
		tbl, err := lookup(ctx, ref.Project, ref.Dataset, ref.Table)
		return tableMsg{ref: ref, tbl: tbl, err: err}
	}
}

func (m *Model) handleTable(msg tableMsg) {
	if e := m.tables[msg.ref]; e != nil {
		e.loading, e.done, e.at, e.tbl, e.err = false, true, m.deps.Now(), msg.tbl, msg.err
	}
}

// expireTables has tables looked up again when they are next shown: the
// failed ones after a recompile (which is what c does once you have logged
// in), all of them after a run.
func (m *Model) expireTables(failedOnly bool) {
	for _, e := range m.tables {
		if !failedOnly || e.err != nil {
			e.at = time.Time{}
		}
	}
}

// updatedDetail is the line saying when a's table was last updated in
// BigQuery: "2026-10-07 11:27 · 3h ago", red when that was before today, or
// a dim N/A for one that hasn't been built yet.
func (m Model) updatedDetail(a *project.Action) (d detail, ok bool) {
	if m.deps.Table == nil || !hasTable(a) {
		return d, false
	}
	s := m.styles
	d = detail{label: "updated", style: s.dim}
	e := m.tables[tableRef(a)]
	var apiErr *bigquery.APIError
	switch {
	case e == nil || !e.done:
		d.value = "checking…"
	case errors.As(e.err, &apiErr) && apiErr.Status == http.StatusNotFound:
		d.value = "N/A"
	case errors.Is(e.err, bigquery.ErrAuth):
		d.value, d.style = "no credentials", s.warnText
	case apiErr != nil:
		d.value, d.style = "✗ "+firstLine(apiErr.Message), s.errorText
	case e.err != nil:
		d.value, d.style = "✗ "+firstLine(e.err.Error()), s.errorText
	case e.tbl.LastModified.IsZero():
		d.value = "N/A"
	default:
		t, now := e.tbl.LastModified.Local(), m.deps.Now().Local()
		d.value, d.style = t.Format("2006-01-02 15:04")+" · "+relTime(now, t), s.text
		if t.Format(time.DateOnly) < now.Format(time.DateOnly) {
			d.style = s.errorText // not updated today
		}
	}
	return d, true
}
