// Package deferral is defer to prod (ADR 0031) for Dataform projects: an
// upstream action that a dry run or a run doesn't build, and that isn't
// built in dev, is read from its table in prod. Dataform has no --defer, so
// xf rewrites the compiled SQL it dry runs itself, and before a run makes a
// Proxy View at each such dev table that reads the prod one. The rules are
// the VS Code extension's (src/defer/deferRules.ts), and its Proxy Views
// are the same, so each tool finds the other's.
//
// This file is the rules, which ask nothing of BigQuery; lookup.go asks it.
package deferral

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// ProxyLabel is the label on a Proxy View, the extension's. A Proxy View
// counts as not built in dev.
const ProxyLabel = "dataform_tools_proxy"

// Status is what defer to prod does with an upstream action not built in
// dev.
type Status string

const (
	// Deferred: read from its Prod Target.
	Deferred Status = "deferred"
	// MissingEverywhere: built neither in dev nor in prod, or new on the
	// branch, so it has no Prod Target.
	MissingEverywhere Status = "missingEverywhere"
	// Unreadable: in prod, but the account may not read it.
	Unreadable Status = "unreadable"
)

// Entry is an upstream action that isn't built in dev.
type Entry struct {
	Dev project.Target `json:"dev"`
	// Prod is nil when the action has no Prod Target.
	Prod   *project.Target `json:"prod,omitempty"`
	Status Status          `json:"status"`
	// Stale says it is also a Changed Action, so prod doesn't have the
	// branch's code (a Stale Deferral).
	Stale bool `json:"stale,omitempty"`
	// Routine says its output is a function or procedure, which a view
	// can't stand in for: a run builds it in dev.
	Routine bool `json:"routine,omitempty"`
}

// Read says the entry is read from prod.
func (e Entry) Read() bool { return e.Status == Deferred && e.Prod != nil }

// BuiltInDev is an upstream action that has a Prod Target but is read
// from dev, because it is built there.
type BuiltInDev struct {
	Dev project.Target `json:"dev"`
	// LastModified is Unix milliseconds, 0 when it couldn't be read.
	LastModified int64 `json:"lastModified,omitempty"`
}

// Deferral is what defer to prod does with the upstream actions of some
// Selected Actions.
type Deferral struct {
	Entries    []Entry      `json:"entries,omitempty"`
	BuiltInDev []BuiltInDev `json:"builtInDev,omitempty"`
}

// Deferred counts the entries read from prod.
func (d *Deferral) Deferred() int {
	if d == nil {
		return 0
	}
	n := 0
	for _, e := range d.Entries {
		if e.Read() {
			n++
		}
	}
	return n
}

// For is the part of d about the upstream actions of a: what its own SQL
// is rewritten with.
func (d *Deferral) For(a *project.Action) *Deferral {
	if d == nil || a == nil {
		return nil
	}
	deps := map[project.ActionID]bool{}
	for _, t := range a.DependencyTargets {
		deps[t.ID()] = true
	}
	out := &Deferral{}
	for _, e := range d.Entries {
		if deps[e.Dev.ID()] {
			out.Entries = append(out.Entries, e)
		}
	}
	for _, b := range d.BuiltInDev {
		if deps[b.Dev.ID()] {
			out.BuiltInDev = append(out.BuiltInDev, b)
		}
	}
	return out
}

// ProdTargets is each action's table in prod, from a compile with the
// prod compiler options, matched by canonical target: the canonical target
// ignores table prefixes and schema suffixes but follows the default
// database, so an action is found by its whole canonical target when its
// database matches, else by schema and name alone when only one prod
// action has them.
type ProdTargets struct {
	full  map[project.ActionID]project.Target
	short map[string]project.Target
}

func shortKey(t project.Target) string { return t.Schema + "." + t.Name }

// NewProdTargets indexes prod, the project compiled with the prod compiler
// options.
func NewProdTargets(prod *project.Graph) ProdTargets {
	p := ProdTargets{full: map[project.ActionID]project.Target{}, short: map[string]project.Target{}}
	ambiguous := map[string]bool{}
	for _, a := range prod.Actions {
		if a.Kind == project.KindUnknown {
			continue
		}
		p.full[a.CanonicalTarget.ID()] = a.Target
		key := shortKey(a.CanonicalTarget)
		if t, ok := p.short[key]; ok && t != a.Target {
			ambiguous[key] = true
		}
		p.short[key] = a.Target
	}
	for key := range ambiguous {
		delete(p.short, key)
	}
	return p
}

// Of is the Prod Target of a, an action of the dev compile.
func (p ProdTargets) Of(a *project.Action) (project.Target, bool) {
	if t, ok := p.full[a.CanonicalTarget.ID()]; ok {
		return t, true
	}
	t, ok := p.short[shortKey(a.CanonicalTarget)]
	return t, ok
}

// Len is how many actions p knows.
func (p ProdTargets) Len() int { return len(p.full) }

// SameAsDev says prod names the same table as dev for every action the
// project builds that it matches, so there is nothing to read from prod:
// prod is the default targets, say, and xf compiles with them too.
// Declarations are left out, as they name the same table in both anyway.
func SameAsDev(dev *project.Graph, p ProdTargets) bool {
	matched := 0
	for _, a := range dev.Actions {
		if a.Kind.External() || a.Kind == project.KindUnknown || a.Kind == project.KindOperation && !a.HasOutput || !a.Kind.HasTable() && a.Kind != project.KindOperation {
			continue
		}
		t, ok := p.Of(a)
		if !ok {
			continue
		}
		if t != a.Target {
			return false
		}
		matched++
	}
	return matched > 0
}

// Candidate is an upstream action whose Dev Target differs from its Prod
// Target, before BigQuery is asked which exist.
type Candidate struct {
	Dev project.Target
	// Prod is nil when the action has no Prod Target.
	Prod    *project.Target
	Routine bool
}

var createsRoutine = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:TEMP(?:ORARY)?\s+)?(?:TABLE\s+)?(?:AGGREGATE\s+)?(?:FUNCTION|PROCEDURE)\b`)

// IsRoutine says a is an operation whose output is a function or
// procedure, which BigQuery lists apart from tables.
func IsRoutine(a *project.Action) bool {
	return a != nil && a.Kind == project.KindOperation && a.HasOutput && slices.ContainsFunc(a.Queries, createsRoutine.MatchString)
}

// Candidates are the upstream actions of the Selected Actions in g that
// defer to prod may read from prod: not Selected Actions themselves, not
// operations without output, which have no table to read, and not those
// whose Prod Target is their Dev Target, declarations among them.
func Candidates(g *project.Graph, selected []project.ActionID, p ProdTargets) []Candidate {
	isSelected := map[project.ActionID]bool{}
	for _, id := range selected {
		isSelected[id] = true
	}
	seen := map[project.ActionID]bool{}
	var out []Candidate
	for _, id := range selected {
		a := g.Actions[id]
		if a == nil || a.Kind == project.KindTest {
			continue
		}
		for _, dep := range a.DependencyTargets {
			did := dep.ID()
			if isSelected[did] || seen[did] {
				continue
			}
			seen[did] = true
			up := g.Actions[did]
			if up == nil {
				up = &project.Action{ID: did, Target: dep, CanonicalTarget: dep, Kind: project.KindUnknown}
			}
			if up.Kind == project.KindOperation && !up.HasOutput {
				continue
			}
			c := Candidate{Dev: dep, Routine: IsRoutine(up)}
			if t, ok := p.Of(up); ok {
				if t == dep {
					continue
				}
				c.Prod = &t
			}
			out = append(out, c)
		}
	}
	return out
}

// ProdStatus is whether a Prod Target exists and may be read.
type ProdStatus int

const (
	ProdMissing ProdStatus = iota
	ProdExists
	ProdUnreadable
)

// Decide applies the rule to candidates once BigQuery has said which
// exist: a candidate built in dev is read from dev, and is not an entry.
func Decide(cands []Candidate, devExists func(project.Target) bool, prodStatus func(project.Target) ProdStatus) []Entry {
	var out []Entry
	for _, c := range cands {
		if devExists(c.Dev) {
			continue
		}
		e := Entry{Dev: c.Dev, Prod: c.Prod, Status: MissingEverywhere, Routine: c.Routine}
		if c.Prod != nil {
			switch prodStatus(*c.Prod) {
			case ProdExists:
				e.Status = Deferred
			case ProdUnreadable:
				e.Status = Unreadable
			}
		}
		out = append(out, e)
	}
	return out
}

// quoted is how Dataform's compiled SQL names a table.
func quoted(t project.Target) string { return "`" + string(t.ID()) + "`" }

// Rewrite reads each deferred entry's Prod Target in sql instead of its
// Dev Target. Dataform names tables as backtick-quoted
// `database.schema.name`, so only that form is replaced. Newlines are left
// alone, so a dry run's error stays on its line.
func Rewrite(sql string, entries []Entry) string {
	for _, e := range entries {
		if e.Read() {
			sql = strings.ReplaceAll(sql, quoted(e.Dev), quoted(*e.Prod))
		}
	}
	return sql
}

// RewriteAction is a copy of a whose SQL reads each deferred entry from
// prod; a itself when nothing is deferred.
func RewriteAction(a *project.Action, entries []Entry) *project.Action {
	if a == nil || !slices.ContainsFunc(entries, Entry.Read) {
		return a
	}
	r := func(s string) string { return Rewrite(s, entries) }
	all := func(ss []string) []string {
		if ss == nil {
			return nil
		}
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = r(s)
		}
		return out
	}
	c := *a
	c.Query, c.IncrementalQuery = r(a.Query), r(a.IncrementalQuery)
	c.PreOps, c.PostOps = all(a.PreOps), all(a.PostOps)
	c.IncrementalPreOps, c.IncrementalPostOps = all(a.IncrementalPreOps), all(a.IncrementalPostOps)
	c.Queries = all(a.Queries)
	return &c
}

var accessDenied = regexp.MustCompile(`Access Denied: Table ([^:\s]+):([^.\s]+)\.([^:\s,.]+)`)

// AccessDenied are the tables BigQuery's "Access Denied: Table
// project:dataset.table" errors in msg name. A table name has no dots, so
// a sentence's full stop isn't taken as part of it.
func AccessDenied(msg string) []project.Target {
	var out []project.Target
	for _, m := range accessDenied.FindAllStringSubmatch(msg, -1) {
		out = append(out, project.Target{Database: m[1], Schema: m[2], Name: m[3]})
	}
	return out
}

// RunSet is the actions a run with o builds: those it selects, by action
// or tag, with their dependencies or dependents when it includes them.
// Their other upstream actions are what a deferred run reads from prod.
func RunSet(g *project.Graph, o project.RunOptions) []project.ActionID {
	tags := map[string]bool{}
	for _, t := range o.Tags {
		tags[t] = true
	}
	in := map[project.ActionID]bool{}
	var queue []project.ActionID
	add := func(id project.ActionID) {
		if a := g.Actions[id]; a != nil && a.Runnable() && !in[id] {
			in[id] = true
			queue = append(queue, id)
		}
	}
	for _, id := range o.Actions {
		add(id)
	}
	if len(tags) > 0 {
		for _, a := range g.Actions {
			if slices.ContainsFunc(a.Tags, func(t string) bool { return tags[t] }) {
				add(a.ID)
			}
		}
	}
	walk := func(next func(project.ActionID) []*project.Action) {
		q := slices.Clone(queue)
		for len(q) > 0 {
			id := q[len(q)-1]
			q = q[:len(q)-1]
			for _, a := range next(id) {
				if a.Runnable() && !in[a.ID] {
					in[a.ID] = true
					queue = append(queue, a.ID)
					q = append(q, a.ID)
				}
			}
		}
	}
	if o.IncludeDeps {
		walk(g.Dependencies)
	}
	if o.IncludeDependents {
		walk(g.Dependents)
	}
	slices.Sort(queue)
	return queue
}

// ProxyQuery is the SQL of the Proxy View that reads prod.
func ProxyQuery(prod project.Target) string { return "SELECT * FROM " + quoted(prod) }

// ProxyDescription is a Proxy View's description, the extension's but
// for its name.
func ProxyDescription(prod project.Target) string {
	return "Defer to prod proxy for " + string(prod.ID()) + ", created by xf. The next dev build of this action replaces it."
}

// ProxyStep is what to do at a Dev Target before a deferred run.
type ProxyStep int

const (
	ProxyCreate ProxyStep = iota
	// ProxyUpdate repoints a Proxy View that reads another table.
	ProxyUpdate
	// ProxyKeep: a Proxy View already reads the Prod Target.
	ProxyKeep
	// ProxyLeave: a real table or view is there, which is never touched.
	ProxyLeave
)

// StepFor is the step for a Dev Target where existing is (nil when
// nothing is), for a Proxy View reading prod.
func StepFor(existingLabels map[string]string, existingQuery string, exists bool, prod project.Target) ProxyStep {
	switch {
	case !exists:
		return ProxyCreate
	case existingLabels[ProxyLabel] != "true":
		return ProxyLeave
	case strings.TrimSpace(existingQuery) == ProxyQuery(prod):
		return ProxyKeep
	}
	return ProxyUpdate
}

// Denied are the Prod Targets d reads that msg, a query's error, says the
// account may not read.
func (d *Deferral) Denied(msg string) []project.Target {
	if d == nil {
		return nil
	}
	read := map[project.ActionID]bool{}
	for _, e := range d.Entries {
		if e.Read() {
			read[e.Prod.ID()] = true
		}
	}
	var out []project.Target
	for _, t := range AccessDenied(msg) {
		if read[t.ID()] {
			out = append(out, t)
		}
	}
	return out
}
