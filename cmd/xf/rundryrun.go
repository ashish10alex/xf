package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// xf run --dry-run (ADR 0038): the plan of a run, from dry runs that write
// nothing and cost nothing. With --defer, the SQL reads prod for what the
// run would read from prod.

// dryRunPlan is the plan --dry-run prints, as text or JSON.
type dryRunPlan struct {
	Tool  string `json:"tool"`
	Defer bool   `json:"defer"`
	// Command is the tool's command a run would start.
	Command string        `json:"command"`
	Builds  []plannedRun  `json:"builds"`
	Added   []string      `json:"added,omitempty"`
	Prod    []plannedRead `json:"readsFromProd,omitempty"`
	Dev     []plannedDev  `json:"readsFromDev,omitempty"`
	// ProxyViews are leftover Proxy Views that a run without --defer reads.
	ProxyViews []string    `json:"proxyViews,omitempty"`
	Refusal    *runRefusal `json:"refusal,omitempty"`
	TotalBytes int64       `json:"totalBytes"`
	TotalUSD   float64     `json:"totalUSD"`
	Failed     int         `json:"failed"`
	Note       string      `json:"note,omitempty"`
}

type plannedRun struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	DryRuns []plannedDryRun `json:"dryRuns,omitempty"`
}

type plannedDryRun struct {
	// Label tells an incremental table's full and incremental runs apart.
	Label    string  `json:"label,omitempty"`
	Bytes    int64   `json:"bytes"`
	CostUSD  float64 `json:"costUSD"`
	Estimate string  `json:"estimate,omitempty"`
	Error    string  `json:"error,omitempty"`
	// BuiltByRun is a table the SQL reads that isn't built yet and that
	// this run builds first, so the dry run can't find it.
	BuiltByRun string `json:"builtByRun,omitempty"`
}

type plannedRead struct {
	Dev   string `json:"dev"`
	Prod  string `json:"prod"`
	Stale bool   `json:"stale,omitempty"`
}

type plannedDev struct {
	Table   string     `json:"table"`
	BuiltAt *time.Time `json:"builtAt,omitempty"`
}

// notFound is BigQuery's error for a table that isn't there.
var notFound = regexp.MustCompile(`Not found: Table ([^:\s]+):([^.\s]+)\.([^\s,.:]+)`)

// dryRunWorkers bounds the dry runs at once.
const dryRunWorkers = 8

func (rc *runCtx) dryRun(ctx context.Context, plan *runPlan) (int, error) {
	out := dryRunPlan{Tool: rc.tool, Defer: rc.ra.deferOn, Refusal: plan.refusal, Note: plan.note, Builds: []plannedRun{}}
	if runner, ok := rc.b.(backend.Runner); ok {
		out.Command = runner.RunCommand(rc.root, project.RunOptions{Args: plan.args, DeferState: plan.dbtState})
	}
	for _, id := range plan.added {
		out.Added = append(out.Added, string(id))
	}
	var read []deferral.Entry
	if rc.ra.deferOn && plan.deferral != nil {
		for _, e := range plan.deferral.Entries {
			if e.Read() {
				read = append(read, e)
				out.Prod = append(out.Prod, plannedRead{Dev: string(e.Dev.ID()), Prod: string(e.Prod.ID()), Stale: e.Stale})
			}
		}
		for _, b := range plan.deferral.BuiltInDev {
			d := plannedDev{Table: string(b.Dev.ID())}
			if b.LastModified > 0 {
				t := time.UnixMilli(b.LastModified)
				d.BuiltAt = &t
			}
			out.Dev = append(out.Dev, d)
		}
	}
	if !rc.ra.deferOn {
		out.ProxyViews = targetStrings(plan.proxies)
	}

	g := plan.graph
	built := map[project.ActionID]bool{}
	for _, id := range plan.runSet {
		built[id] = true
	}
	// Rewriting is Dataform's: dbt's compile with --defer names prod.
	rewrite := rc.tool == backend.Dataform && len(read) > 0
	out.Builds = make([]plannedRun, len(plan.runSet))
	var wg sync.WaitGroup
	sem := make(chan struct{}, dryRunWorkers)
	for i, id := range plan.runSet {
		a := g.Actions[id]
		if a == nil {
			out.Builds[i] = plannedRun{ID: string(id)}
			continue
		}
		out.Builds[i] = plannedRun{ID: string(id), Type: a.Kind.String()}
		queries := a.DryRunQueries()
		out.Builds[i].DryRuns = make([]plannedDryRun, len(queries))
		for j, q := range queries {
			wg.Add(1)
			go func(i, j int, a *project.Action, q project.DryRunQuery) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				sql := q.SQL
				if rewrite {
					sql = deferral.Rewrite(sql, read)
				}
				p := plannedDryRun{Label: q.Label}
				proj, loc := g.DryRunTarget(a)
				res, err := rc.engine.DryRun(ctx, proj, loc, sql)
				switch {
				case err != nil:
					msg := err.Error()
					if m := notFound.FindStringSubmatch(msg); m != nil && built[project.ActionID(m[1]+"."+m[2]+"."+m[3])] && project.ActionID(m[1]+"."+m[2]+"."+m[3]) != a.ID {
						p.BuiltByRun = m[1] + "." + m[2] + "." + m[3]
					} else {
						p.Error = msg
					}
				default:
					p.Bytes, p.CostUSD = res.Bytes, res.CostUSD()
					p.Estimate, _ = frontend.FormatDryRun(res)
				}
				out.Builds[i].DryRuns[j] = p
			}(i, j, a, q)
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	for _, b := range out.Builds {
		for _, d := range b.DryRuns {
			out.TotalBytes += d.Bytes
			out.TotalUSD += d.CostUSD
			if d.Error != "" {
				out.Failed++
			}
		}
	}

	if rc.ra.json {
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(rc.out, string(data))
	} else {
		fmt.Fprint(rc.out, planText(out, time.Now()))
	}
	switch {
	case out.Refusal != nil:
		out.Refusal.print()
		return runNeedsDecision, nil
	case out.Failed > 0:
		return runDryRunFailed, nil
	}
	return runOK, nil
}

// planText is the plan as text.
func planText(p dryRunPlan, now time.Time) string {
	var b strings.Builder
	with := ""
	if p.Defer {
		with = ", with defer to prod"
	}
	fmt.Fprintf(&b, "dry run of %s%s: nothing runs, nothing is written\n", p.Tool, with)
	if p.Command != "" {
		fmt.Fprintf(&b, "a run would start: %s\n", p.Command)
	}
	for _, a := range p.Added {
		fmt.Fprintf(&b, "added to the run: %s\n", a)
	}
	fmt.Fprintf(&b, "\nbuilds %s:\n", plural(len(p.Builds), "action"))
	width := 0
	for _, r := range p.Builds {
		width = max(width, len(r.ID))
	}
	for _, r := range p.Builds {
		if len(r.DryRuns) == 0 {
			fmt.Fprintf(&b, "  · %-*s  nothing to dry run (%s)\n", width, r.ID, r.Type)
			continue
		}
		for _, d := range r.DryRuns {
			id := r.ID
			if d.Label != "" {
				id += " (" + d.Label + ")"
			}
			switch {
			case d.Error != "":
				msg, _, _ := strings.Cut(d.Error, "\n")
				fmt.Fprintf(&b, "  ✗ %-*s  %s\n", width, id, msg)
			case d.BuiltByRun != "":
				fmt.Fprintf(&b, "  ↳ %-*s  reads %s, which this run builds first\n", width, id, d.BuiltByRun)
			default:
				fmt.Fprintf(&b, "  ✓ %-*s  %s\n", width, id, d.Estimate)
			}
		}
	}
	if len(p.Prod) > 0 {
		fmt.Fprintf(&b, "\nreads %s from prod:\n", plural(len(p.Prod), "upstream table"))
		for _, r := range p.Prod {
			mark := ""
			if r.Stale {
				mark = "  ⚠ changed on this branch"
			}
			fmt.Fprintf(&b, "  %s → %s%s\n", r.Dev, r.Prod, mark)
		}
	}
	if len(p.Dev) > 0 {
		fmt.Fprintf(&b, "\nreads from dev, where they are built:\n")
		for _, d := range p.Dev {
			when := ""
			if d.BuiltAt != nil {
				when = " · built " + frontend.RelTime(now, *d.BuiltAt)
			}
			fmt.Fprintf(&b, "  %s%s\n", d.Table, when)
		}
	}
	if len(p.ProxyViews) > 0 {
		fmt.Fprintf(&b, "\nreads prod through %s left by an earlier deferred run:\n", plural(len(p.ProxyViews), "proxy view"))
		for _, v := range p.ProxyViews {
			fmt.Fprintf(&b, "  %s\n", v)
		}
	}
	fmt.Fprintf(&b, "\ntotal scanned by the dry runs: %s $%.3f", frontend.FormatBytes(p.TotalBytes), p.TotalUSD)
	if p.Failed > 0 {
		fmt.Fprintf(&b, " · %s failed", plural(p.Failed, "dry run"))
	}
	b.WriteString("\n")
	return b.String()
}

func targetStrings(ts []project.Target) []string {
	var out []string
	for _, t := range ts {
		out = append(out, string(t.ID()))
	}
	return out
}
