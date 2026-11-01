package web

import (
	"net/http"
	"os"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
)

// Pane 2's target section sets what a project's compiles add to the names
// it builds: a Dataform project's table prefix, schema suffix and
// database suffix, a dbt project's target. It reads and writes them as the
// settings page does (/api/settings); this is what else it shows: dbt's
// targets, and the names the prodCompilerOptions form (ADR 0037) shows
// and checks prod's against.

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
	// Workflow are a Dataform project's names in workflow_settings.yaml,
	// which the prod compile uses where its options say nothing; Dev are
	// those of xf's compile, nil before one. Both nil for dbt.
	Workflow *dataform.WorkflowNames `json:"workflow,omitempty"`
	Dev      *dataform.WorkflowNames `json:"dev,omitempty"`
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
	} else {
		if wn, err := dataform.ReadWorkflowNames(p.deps.Root); err == nil {
			v.Workflow = &wn
		}
		if g := p.graph(); g != nil {
			v.Dev = &dataform.WorkflowNames{Project: g.DefaultDatabase, Dataset: g.DefaultSchema, Prefix: g.TablePrefix,
				SchemaSuffix: g.SchemaSuffix, DatabaseSuffix: g.DatabaseSuffix}
		}
	}
	writeJSON(w, v)
}
