package web

import (
	"net/http"
	"os"

	"github.com/ashish10alex/xf/internal/backend/dbt"
)

// Pane 2's target section sets what a project's compiles add to the names
// it builds: a Dataform project's table prefix, schema suffix and
// database suffix, a dbt project's target. It reads and writes them as the
// settings page does (/api/settings); this is what else it shows: dbt's
// targets.

// targetView is what the target section shows besides the settings.
type targetView struct {
	// Profile is a dbt project's profile, with its targets; nil for
	// Dataform.
	Profile *dbt.Profile `json:"profile,omitempty"`
	// Target is the dbt target the project compiles with; "" for the
	// profile's default.
	Target string `json:"target,omitempty"`
	// ProfileErr is why the profile couldn't be read.
	ProfileErr string `json:"profileErr,omitempty"`
}

// target answers with the target section's view of the project.
func (p *projectState) target(w http.ResponseWriter, r *http.Request) {
	v := targetView{}
	if p.tool == "dbt" {
		prof, err := dbt.ReadProfile(p.deps.Root, os.Getenv)
		if err != nil {
			v.ProfileErr = err.Error()
		}
		if prof.Name != "" {
			v.Profile = &prof
		}
		v.Target = p.deps.Target
	}
	writeJSON(w, v)
}
