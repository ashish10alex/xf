package cost

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
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
	// Waits are, when New is set, every table the branch adds that the
	// dry run waits on a run to build, Missing first.
	Waits []string
}

// notFoundRE is BigQuery's message for a query that reads a table, view or
// dataset it doesn't have.
var notFoundRE = regexp.MustCompile(`^dry run failed: Not found: (Table|View|Dataset) (\S+) was not found in location (\S+)`)

// MissingHint says why a dry run reads a table BigQuery doesn't have.
const MissingHint = "A dry run needs every table it reads to exist. A table this branch adds exists only once a run builds it."

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
	var out []Skipped
	for _, a := range r.Actions {
		if a.Estimate != nil {
			continue
		}
		s := Skipped{Action: a, Why: firstLine(a.Reason)}
		if m := notFoundRE.FindStringSubmatch(s.Why); m != nil {
			s.Missing, s.Location = strings.Replace(m[2], ":", ".", 1), m[3]
			if b, ok := byName[strings.ToLower(s.Missing)]; ok {
				s.New, s.Changed = b.New, !b.New
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
			if s.Missing != "" {
				out[i].Hint = MissingHint
			}
		}
		out[i].Actions = append(out[i].Actions, s)
	}
	slices.SortStableFunc(out, func(a, b Skip) int { return cmp.Compare(len(b.Actions), len(a.Actions)) })
	return out
}

// Tag is "new on this branch" or "changed on this branch" when the branch
// adds or changes the missing table, else "".
func (s Skipped) Tag() string {
	switch {
	case s.New:
		return "new on this branch"
	case s.Changed:
		return "changed on this branch"
	}
	return ""
}
