package backend_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/project"
)

// shared are the actions examples/dataform and examples/dbt both define,
// under the same targets.
var shared = []project.ActionID{
	"alex-personal-dev-01.raw.customers",
	"alex-personal-dev-01.raw.orders",
	"alex-personal-dev-01.raw.payments",
	"alex-personal-dev-01.xf_example.stg_customers",
	"alex-personal-dev-01.xf_example.stg_orders",
	"alex-personal-dev-01.xf_example.stg_payments",
	"alex-personal-dev-01.xf_example.dim_customers",
	"alex-personal-dev-01.xf_example.fct_orders",
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestExamplesParity checks that the two example projects, compiled by
// their own tools, give the same graph where they model the same thing:
// what xf shows doesn't depend on the tool.
func TestExamplesParity(t *testing.T) {
	var df backend.Backend = dataform.Backend{}
	want, err := df.Parse(fixture(t, "dataform.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"dbt-v2.json", "dbt-core.json"} {
		t.Run(f, func(t *testing.T) {
			var b backend.Backend = &dbt.Backend{}
			got, err := b.Parse(fixture(t, f))
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range shared {
				w, g := want.Actions[id], got.Actions[id]
				if w == nil || g == nil {
					t.Errorf("%s: dataform has it %v, dbt %v", id, w != nil, g != nil)
					continue
				}
				if sameKind(w.Kind) != sameKind(g.Kind) {
					t.Errorf("%s: dataform %s, dbt %s", id, w.Kind, g.Kind)
				}
				if w.Description != g.Description {
					t.Errorf("%s description: dataform %q, dbt %q", id, w.Description, g.Description)
				}
				if w.PartitionBy != g.PartitionBy {
					t.Errorf("%s partition: dataform %q, dbt %q", id, w.PartitionBy, g.PartitionBy)
				}
				if !sameSet(w.Tags, g.Tags) {
					t.Errorf("%s tags: dataform %v, dbt %v", id, w.Tags, g.Tags)
				}
				if wc, gc := columns(w), columns(g); !slices.Equal(wc, gc) {
					t.Errorf("%s columns: dataform %v, dbt %v", id, wc, gc)
				}
				if wd, gd := sharedIDs(want.Dependencies(id)), sharedIDs(got.Dependencies(id)); !slices.Equal(wd, gd) {
					t.Errorf("%s reads: dataform %v, dbt %v", id, wd, gd)
				}
				if wd, gd := sharedIDs(want.Dependents(id)), sharedIDs(got.Dependents(id)); !slices.Equal(wd, gd) {
					t.Errorf("%s is read by: dataform %v, dbt %v", id, wd, gd)
				}
			}
		})
	}
}

// sameKind maps a dbt source to the Dataform declaration it stands for.
func sameKind(k project.ActionKind) project.ActionKind {
	if k == project.KindSource {
		return project.KindDeclaration
	}
	return k
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func columns(a *project.Action) []string {
	var out []string
	for _, c := range a.Columns {
		out = append(out, c.Path[0]+": "+c.Description)
	}
	return out
}

// sharedIDs are the IDs of actions among the shared ones, sorted.
func sharedIDs(actions []*project.Action) []project.ActionID {
	var out []project.ActionID
	for _, a := range actions {
		if slices.Contains(shared, a.ID) {
			out = append(out, a.ID)
		}
	}
	slices.Sort(out)
	return out
}
