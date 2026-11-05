package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/backend"
)

// xf run's command line (ADR 0038): xf's own flags, and the tool's run
// flags, which go to `dataform run` or `dbt build` as given. Of the tool's
// flags xf reads the ones that select what the run builds, and those that
// change the compile, which --defer refuses.

// runArgs are xf's own flags and the tool's arguments as given.
type runArgs struct {
	// dir and backend find the project, as --dir and --backend do for
	// xf's other commands; a first argument that isn't a flag is the dir,
	// as `dataform run [project-dir]` takes it.
	dir, backend string

	deferOn, buildChanged, allowStale, allowProxies, removeViews bool
	// dryRun is --dry-run: xf's dry run, in both tools.
	dryRun bool
	// json is --json: the plan as JSON. Dataform's --json also goes to
	// the tool; dbt has none, so it is xf's alone there.
	json bool
	help bool

	// tool are the tool's arguments, in order, without xf's.
	tool []string
}

// xfRunFlags are xf run's own boolean flags.
var xfRunFlags = map[string]func(*runArgs) *bool{
	"--defer":               func(r *runArgs) *bool { return &r.deferOn },
	"--defer-build-changed": func(r *runArgs) *bool { return &r.buildChanged },
	"--defer-allow-stale":   func(r *runArgs) *bool { return &r.allowStale },
	"--allow-proxy-views":   func(r *runArgs) *bool { return &r.allowProxies },
	"--defer-remove-views":  func(r *runArgs) *bool { return &r.removeViews },
	"--dry-run":             func(r *runArgs) *bool { return &r.dryRun },
}

// parseRunArgs splits xf run's arguments into xf's flags and the tool's.
// Everything after "--" is the tool's.
func parseRunArgs(args []string) (runArgs, error) {
	var r runArgs
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		r.dir, args = args[0], args[1:]
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			r.tool = append(r.tool, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(a, "=")
		switch {
		case a == "-h" || a == "--help" || a == "-help":
			r.help = true
		case name == "--dir" || name == "--backend":
			if !hasValue {
				if i+1 >= len(args) {
					return r, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			if name == "--dir" {
				if r.dir != "" {
					return r, fmt.Errorf("the project's dir is given twice: %q and %q", r.dir, value)
				}
				r.dir = value
			} else {
				r.backend = value
			}
		case name == "--json":
			on, err := boolValue(name, value, hasValue)
			if err != nil {
				return r, err
			}
			r.json = on
			r.tool = append(r.tool, a)
		case xfRunFlags[name] != nil:
			on, err := boolValue(name, value, hasValue)
			if err != nil {
				return r, err
			}
			*xfRunFlags[name](&r) = on
		default:
			r.tool = append(r.tool, a)
		}
	}
	return r, nil
}

func boolValue(name, value string, hasValue bool) (bool, error) {
	switch {
	case !hasValue || value == "true":
		return true, nil
	case value == "false":
		return false, nil
	}
	return false, fmt.Errorf("%s takes true or false, not %q", name, value)
}

// toolArgs are the tool's run arguments, read.
type toolArgs struct {
	// args go to the tool: all it was given but xf's.
	args []string
	// Dataform: --actions and --tags as given, and whether the run
	// includes dependencies or dependents.
	actions, tags    []string
	deps, dependents bool
	// dbt: the selection flags as given, for dbt ls.
	selection []string
	// compile are the flags given that change the compile (--vars …).
	compile []string
	// selects says the run selects actions, so it isn't the whole project.
	selects bool
}

// The tool's flags xf knows. A flag not listed takes a value unless the
// next argument is a flag; Dataform's --actions and --tags, and dbt's
// selection flags, take every value up to the next flag.
var (
	dataformBools = []string{"--run-tests", "--emit-lineage", "--full-refresh", "--include-deps", "--include-dependents", "--json", "--disable-assertions", "--help", "--version"}
	dataformLists = []string{"--actions", "--tags"}
	// dataformCompile change what the compile names, so xf's Deferral
	// would be of other tables than the run's.
	dataformCompile = []string{"--default-database", "--default-schema", "--default-location", "--assertion-schema", "--vars", "--database-suffix", "--schema-suffix", "--table-prefix"}

	dbtBools = []string{"--full-refresh", "--fail-fast", "-x", "--empty", "--debug", "-d", "--warn-error", "--store-failures", "--favor-state",
		"--quiet", "-q", "--print", "--use-colors", "--version-check", "--partial-parse", "--static-parser", "--introspect", "--write-json",
		"--populate-cache", "--cache-selected-only", "--log-cache-events", "--use-experimental-parser", "--include-saved-query", "--show-resource-report"}
	dbtLists = []string{"--select", "-s", "--models", "--model", "-m", "--exclude", "--resource-type", "--resource-types", "--exclude-resource-type", "--exclude-resource-types"}
	// dbtSelectionValues select by one value.
	dbtSelectionValues = []string{"--selector", "--indirect-selection"}
	dbtCompile         = []string{"--target", "-t", "--vars", "--profile", "--profiles-dir", "--project-dir", "--state", "--defer-state", "--favor-state"}
)

// readToolArgs reads the tool's arguments for a project of tool.
func readToolArgs(tool string, args []string) (toolArgs, error) {
	t := toolArgs{args: args}
	bools, lists, compile := dataformBools, dataformLists, dataformCompile
	if tool == backend.DBT {
		bools, lists, compile = dbtBools, dbtLists, dbtCompile
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			return t, fmt.Errorf("unexpected argument %q: only the first argument may be the project's dir", a)
		}
		name, value, hasValue := strings.Cut(a, "=")
		negated := strings.HasPrefix(name, "--no-")
		if negated {
			name = "--" + strings.TrimPrefix(name, "--no-")
		}
		if slices.Contains(compile, name) {
			t.compile = append(t.compile, name)
		}
		switch {
		case slices.Contains(lists, name):
			var values []string
			if hasValue {
				values = append(values, value)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				values = append(values, args[i])
			}
			if len(values) == 0 {
				return t, fmt.Errorf("%s needs a value", name)
			}
			switch name {
			case "--actions":
				t.actions = append(t.actions, values...)
			case "--tags":
				t.tags = append(t.tags, values...)
			default:
				t.selection = append(append(t.selection, name), values...)
			}
			t.selects = true
		case slices.Contains(bools, name) || negated:
			on := !negated
			if hasValue {
				on = value == "true"
			}
			switch name {
			case "--include-deps":
				t.deps = on
			case "--include-dependents":
				t.dependents = on
			}
		default:
			if !hasValue {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return t, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			if slices.Contains(dbtSelectionValues, name) && tool == backend.DBT {
				t.selection = append(t.selection, name, value)
				t.selects = t.selects || name == "--selector"
			}
		}
	}
	return t, nil
}
