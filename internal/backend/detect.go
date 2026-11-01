package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The tools xf opens projects of, as --backend names them.
const (
	Dataform = "dataform"
	DBT      = "dbt"
)

// Names are the tools Detect knows, in the order help and errors list them.
var Names = []string{Dataform, DBT}

// markers are the files whose presence in a directory makes it the root of
// a tool's project.
var markers = map[string][]string{
	Dataform: {"workflow_settings.yaml", "dataform.json"},
	DBT:      {"dbt_project.yml"},
}

// ErrNoProject is returned when a directory is not the root of any
// project Detect knows.
var ErrNoProject = errors.New("not a Dataform or dbt project")

// Detect says which tool's project dir is the root of. It looks in dir
// alone, never above it, so xf runs where the project is. want, when not
// empty, is the tool asked for with --backend: dir must be its project,
// and it settles a directory that is the root of both.
func Detect(dir, want string) (string, error) {
	var found []string
	for _, name := range Names {
		if hasMarker(dir, name) {
			found = append(found, name)
		}
	}
	switch {
	case want != "":
		if _, ok := markers[want]; !ok {
			return "", fmt.Errorf("unknown backend %q: want %s", want, strings.Join(Names, " or "))
		}
		for _, name := range found {
			if name == want {
				return want, nil
			}
		}
		return "", fmt.Errorf("%w: %s has no %s", ErrNoProject, dir, strings.Join(markers[want], " or "))
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		return "", fmt.Errorf("%s is both a Dataform and a dbt project: pass --backend %s", dir, strings.Join(Names, " or --backend "))
	}
	return "", fmt.Errorf("%w: %s has none of %s; run xf in the project's root or pass --dir", ErrNoProject, dir, strings.Join(allMarkers(), ", "))
}

func hasMarker(dir, name string) bool {
	for _, m := range markers[name] {
		if info, err := os.Stat(filepath.Join(dir, m)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func allMarkers() []string {
	var out []string
	for _, name := range Names {
		out = append(out, markers[name]...)
	}
	return out
}
