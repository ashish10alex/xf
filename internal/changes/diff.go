// Package changes works out a project's Changed Actions: the actions whose
// compiled output differs from the merge-base with the default branch, like
// dbt's state:modified. It ports vscode-dataform-tools' Run Changed
// (src/changedActions.ts and src/utils/compiledGraphDiff.ts), which stays the
// reference: keep the two in step, test cases included.
//
// The base is compiled from a git archive snapshot of the merge-base with
// the same dataform binary, and cached on disk by commit, binary and UTC
// day, so after the first compile a diff costs only the git calls.
//
// For dbt, dbt itself decides: Source.Base parses the snapshot into a
// manifest, and Source.Diff asks dbt for state:modified against it (see
// package dbt). Compare is Dataform's.
package changes

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// Reason says how an action changed.
type Reason string

const (
	// ReasonNew is an action the base doesn't have.
	ReasonNew Reason = "new"
	// ReasonSQL is an action whose query, or pre or post operations, differ.
	ReasonSQL Reason = "sql"
	// ReasonConfig is an action with a setting that changes what is built
	// (type, protected, uniqueKey, bigquery options, onSchemaChange,
	// disabled).
	ReasonConfig Reason = "config"
	// ReasonUpstream is a dbt node dbt calls modified though its own SQL
	// and config are the same: a macro it calls, or its contract, changed.
	ReasonUpstream Reason = "upstream"
)

// Reasons are the reasons, in the order they are shown.
var Reasons = []Reason{ReasonNew, ReasonSQL, ReasonConfig, ReasonUpstream}

// Changed is an action that runs differently than on the base.
type Changed struct {
	ID       project.ActionID `json:"id"`
	Target   project.Target   `json:"target"`
	FileName string           `json:"fileName"`
	// Type is the compile output's: table, view, incremental, operations,
	// assertion or notebook; for dbt, the action kind (table, seed, test…).
	Type    string   `json:"type"`
	Reasons []Reason `json:"reasons"`
	// Parent is the table an assertion was generated from (uniqueKey,
	// nonNull, rowConditions in its config); "" for other actions.
	Parent project.ActionID `json:"parent,omitempty"`
	// RenamedFrom is set on a new action whose file git calls a rename of
	// RenamedFromFile, the file of RenamedFrom, an action the branch
	// deletes: the same action under the name of its new file. Similarity
	// is how alike git says the two files are, in percent.
	RenamedFrom     project.ActionID `json:"renamedFrom,omitempty"`
	RenamedFromFile string           `json:"renamedFromFile,omitempty"`
	Similarity      int              `json:"similarity,omitempty"`
}

// IsNew says whether the base doesn't have c.
func (c Changed) IsNew() bool { return slices.Contains(c.Reasons, ReasonNew) }

// Deleted is an action the base has and the head doesn't. It is listed,
// never run.
type Deleted struct {
	ID       project.ActionID `json:"id"`
	FileName string           `json:"fileName"`
	Type     string           `json:"type"`
	Parent   project.ActionID `json:"parent,omitempty"` // as for Changed
	// RenamedTo is the new action this one was renamed to (see
	// Changed.RenamedFrom).
	RenamedTo project.ActionID `json:"renamedTo,omitempty"`
}

// Diff is how two compiles of a project differ, each list sorted by file,
// then target.
type Diff struct {
	Changed []Changed `json:"changed,omitempty"`
	Deleted []Deleted `json:"deleted,omitempty"`
}

// rawAction is an action of the compile output, with every field kept.
type rawAction map[string]any

type rawGraph struct {
	Tables     []rawAction `json:"tables"`
	Operations []rawAction `json:"operations"`
	Assertions []rawAction `json:"assertions"`
	Notebooks  []rawAction `json:"notebooks"`
}

// comparableAction is what of an action is compared: its SQL and the settings
// that change what a run builds.
type comparableAction struct {
	target   project.Target
	parent   project.ActionID
	fileName string
	typ      string
	sql      map[string]any
	config   map[string]any
}

func (a rawAction) target() (project.Target, bool) { return a.targetAt("target") }

// parent is the ID of the table a generated assertion comes from, or "".
func (a rawAction) parent() project.ActionID {
	if t, ok := a.targetAt("parentAction"); ok {
		return t.ID()
	}
	return ""
}

func (a rawAction) targetAt(key string) (project.Target, bool) {
	t, ok := a[key].(map[string]any)
	if !ok {
		return project.Target{}, false
	}
	str := func(k string) string { s, _ := t[k].(string); return s }
	return project.Target{Database: str("database"), Schema: str("schema"), Name: str("name")}, true
}

func (a rawAction) str(k string) string {
	s, _ := a[k].(string)
	return s
}

// pick copies keys of a, absent ones as nil.
func (a rawAction) pick(keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = a[k]
	}
	return out
}

// runnableActions indexes the actions a run can build by ID. Descriptions,
// column docs, tags and dependencies are left out: changing only those
// doesn't change what a run builds. Declarations are never run.
func runnableActions(data []byte) (map[project.ActionID]comparableAction, error) {
	var g rawGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("decode compile result: %w", err)
	}
	out := map[project.ActionID]comparableAction{}
	add := func(a rawAction, typ string, sql, config map[string]any) {
		if t, ok := a.target(); ok {
			out[t.ID()] = comparableAction{target: t, parent: a.parent(), fileName: project.SlashPath(a.str("fileName")), typ: typ, sql: sql, config: config}
		}
	}
	for _, t := range g.Tables {
		add(t, t.str("type"),
			t.pick("query", "incrementalQuery", "preOps", "postOps", "incrementalPreOps"),
			t.pick("type", "protected", "uniqueKey", "bigquery", "onSchemaChange", "disabled"))
	}
	for _, o := range g.Operations {
		add(o, "operations", o.pick("queries"), o.pick("hasOutput", "disabled"))
	}
	for _, a := range g.Assertions {
		add(a, "assertion", a.pick("query", "preOps", "postOps"), a.pick("disabled"))
	}
	for _, n := range g.Notebooks {
		add(n, "notebook", n.pick("notebookContents"), n.pick("disabled"))
	}
	return out, nil
}

// normalize treats absent, null, false, "" and empty arrays and objects
// alike, since the CLI and the API omit defaults inconsistently. It returns
// nil for an empty value.
func normalize(v any) any {
	switch v := v.(type) {
	case []any:
		var items []any
		for _, x := range v {
			items = append(items, normalize(x))
		}
		if len(items) == 0 {
			return nil
		}
		return items
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if n := normalize(x); n != nil {
				out[k] = n
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case nil, bool, string:
		if v == nil || v == false || v == "" {
			return nil
		}
	}
	return v
}

func equal(a, b map[string]any) bool {
	// encoding/json sorts map keys, so equal values encode equally.
	x, _ := json.Marshal(normalize(a))
	y, _ := json.Marshal(normalize(b))
	return string(x) == string(y)
}

// Compare finds the actions whose compiled output differs between two
// compile results (dataform compile --json output), base first.
// Comparing compiled output rather than changed files also catches actions
// affected by edits to includes/ or workflow_settings.yaml.
func Compare(base, head []byte) (Diff, error) {
	before, err := runnableActions(base)
	if err != nil {
		return Diff{}, err
	}
	after, err := runnableActions(head)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	for id, a := range after {
		b, ok := before[id]
		var reasons []Reason
		if !ok {
			reasons = append(reasons, ReasonNew)
		} else {
			if !equal(b.sql, a.sql) {
				reasons = append(reasons, ReasonSQL)
			}
			if !equal(b.config, a.config) {
				reasons = append(reasons, ReasonConfig)
			}
		}
		if len(reasons) > 0 {
			d.Changed = append(d.Changed, Changed{ID: id, Target: a.target, FileName: a.fileName, Type: a.typ, Reasons: reasons, Parent: a.parent})
		}
	}
	for id, b := range before {
		if _, ok := after[id]; !ok {
			d.Deleted = append(d.Deleted, Deleted{ID: id, FileName: b.fileName, Type: b.typ, Parent: b.parent})
		}
	}
	slices.SortFunc(d.Changed, func(x, y Changed) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	slices.SortFunc(d.Deleted, func(x, y Deleted) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	return d, nil
}

// ReasonsText is c's reasons as shown: "sql, config".
func (c Changed) ReasonsText() string {
	s := make([]string, len(c.Reasons))
	for i, r := range c.Reasons {
		s[i] = string(r)
	}
	return strings.Join(s, ", ")
}
