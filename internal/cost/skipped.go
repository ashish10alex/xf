package cost

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/impact"
)

// Skip is the actions that couldn't be estimated for one reason.
type Skip struct {
	// Why is the reason, with the missing table taken out of it when the
	// dry run read a table BigQuery doesn't have; Hint says why that
	// happens.
	Why     string
	Hint    string
	Actions []Skipped
}

// Skipped is an action that couldn't be estimated.
type Skipped struct {
	Action Action
	// Why is its reason, with the missing table taken out of it.
	Why string
	// Missing is the project.dataset.name the dry run did not find, in
	// Location, "" for another reason. New is set when the branch adds
	// that table, Changed when it changes it.
	Missing, Location string
	New, Changed      bool
	// Kind classifies Missing by who builds it and whether this branch touches
	// it (empty when Missing == "").
	Kind MissingKind
	// FileName and From identify a declaration/source when Kind == MissingPrereq.
	FileName string
	From     string
	// Waits are, when New is set, every table the branch adds that the
	// dry run waits on a run to build, Missing first.
	Waits []string
	// Column is, when the dry run didn't find a column, the changed action
	// that makes it on this branch (see Action.AddedColumn).
	Column *AddedColumn
}

// ColumnHint says why a dry run doesn't find a column the branch adds.
const ColumnHint = "A dry run reads the tables in BigQuery, which have a column this branch adds only once a run builds them."

// WhyNot is why a has no estimate, in a line: its Reason, or the column
// the branch adds that it waits on.
func (a Action) WhyNot() string {
	if c := a.AddedColumn; c != nil {
		return "waits on a column this branch adds that no run has built yet: " + c.Column + " of " + impact.ShortTable(c.Table)
	}
	return firstLine(a.Reason)
}

// notFoundRE is BigQuery's message for a query that reads a table, view or
// dataset it doesn't have.
var notFoundRE = regexp.MustCompile(`^dry run failed: Not found: (Table|View|Dataset) (\S+) was not found in location (\S+)`)

// MissingHint says why a dry run reads a table BigQuery doesn't have.
const MissingHint = "A dry run needs every table it reads to exist. A table this branch adds exists only once a run builds it; an external declaration must be loaded outside Dataform."

// SkippedActions are the actions of r without an estimate, in r's order.
func SkippedActions(r *Report) []Skipped {
	// A missing table is named as the dry runs' compile names it.
	byName := map[string]Action{}
	for _, a := range r.Actions {
		byName[strings.ToLower(a.Name)] = a
		if a.DryRunAs != "" {
			byName[strings.ToLower(a.DryRunAs)] = a
		}
	}
	byDecl := map[string]DeclaredTable{}
	for _, d := range r.Declared {
		byDecl[strings.ToLower(d.Table)] = d
	}
	var out []Skipped
	for _, a := range r.Actions {
		if a.Estimate != nil {
			continue
		}
		s := Skipped{Action: a, Why: firstLine(a.Reason), Column: a.AddedColumn}
		if s.Column != nil {
			s.Why = "Waits on a column this branch adds"
		}
		if m := notFoundRE.FindStringSubmatch(s.Why); m != nil {
			s.Missing, s.Location = strings.Replace(m[2], ":", ".", 1), m[3]
			low := strings.ToLower(s.Missing)
			if b, ok := byName[low]; ok {
				s.New, s.Changed = b.Added(), !b.Added()
				if s.New {
					s.Kind = MissingNew
				} else {
					s.Kind = MissingNeverBuilt
				}
			} else if d, ok := byDecl[low]; ok {
				s.Kind = MissingPrereq
				s.FileName = d.FileName
				s.From = d.From
			} else {
				s.Kind = MissingUntouched
			}
			if s.New {
				s.Waits = waits(s.Missing, a.Waits)
			}
			s.Why = "Reads a " + strings.ToLower(m[1]) + " not in BigQuery (location " + m[3] + ")"
		}
		out = append(out, s)
	}
	return out
}

// waits is missing, then the other tables of all.
func waits(missing string, all []string) []string {
	out := []string{missing}
	for _, t := range all {
		if !strings.EqualFold(t, missing) {
			out = append(out, t)
		}
	}
	return out
}

// Skips groups the actions without an estimate by why, the biggest group
// first. The actions keep their order in r.
func Skips(r *Report) []Skip {
	var out []Skip
	at := map[string]int{}
	for _, s := range SkippedActions(r) {
		i, ok := at[s.Why]
		if !ok {
			i = len(out)
			at[s.Why] = i
			out = append(out, Skip{Why: s.Why})
			switch {
			case s.Missing != "":
				out[i].Hint = MissingHint
			case s.Column != nil:
				out[i].Hint = ColumnHint
			}
		}
		out[i].Actions = append(out[i].Actions, s)
	}
	slices.SortStableFunc(out, func(a, b Skip) int { return cmp.Compare(len(b.Actions), len(a.Actions)) })
	return out
}

// Tag says how the branch relates to the missing table, or "" for untouched.
func (s Skipped) Tag() string {
	switch s.Kind {
	case MissingNew:
		return "new on this branch"
	case MissingNeverBuilt:
		return "never built in prod"
	case MissingPrereq:
		return "prerequisite on this branch"
	}
	return ""
}
