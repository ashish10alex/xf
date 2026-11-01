package dbt

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/examples/dbt-*.json by compiling the examples with dbt on PATH and $XF_DBT_CORE_BIN")

// TestUpdateFixtures rewrites the trimmed manifests the other tests read:
// dbt-v2.json from dbt on PATH (dbt v2) and dbt-core.json from
// $XF_DBT_CORE_BIN (dbt-core 1.x). `just fixtures-examples` runs it.
func TestUpdateFixtures(t *testing.T) {
	if !*update {
		t.Skip("pass -update to rewrite the fixtures")
	}
	bins := map[string]string{"dbt-v2": "dbt", "dbt-core": os.Getenv("XF_DBT_CORE_BIN")}
	for name, bin := range bins {
		if bin == "" {
			t.Logf("skipping %s: no binary", name)
			continue
		}
		b := New(bin, "", func(string) string { return t.TempDir() }, false)
		major, version := b.version(context.Background())
		if want := map[string]int{"dbt-v2": 2, "dbt-core": 1}[name]; major != want {
			t.Fatalf("%s is %q, want dbt %d", bin, version, want)
		}
		for _, ex := range []string{"dbt", "dbt-broken"} {
			g, err := b.Compile(context.Background(), example(t, ex))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join("..", "..", "..", "testdata", "examples", name+map[string]string{"dbt": "", "dbt-broken": "-broken"}[ex]+".json")
			if err := os.WriteFile(out, append(g.JSON, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s (%s, %d actions, %d errors)", out, version, len(g.Actions), len(g.Errors))
		}
	}
}
