package changes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/project"
)

// TestEndToEndWithDataform runs the real dataform CLI on a copy of
// testdata/projects/navigation in a git repository. It is slow and needs
// `dataform` on PATH, so it runs only with XF_E2E=1.
func TestEndToEndWithDataform(t *testing.T) {
	if os.Getenv("XF_E2E") == "" {
		t.Skip("set XF_E2E=1 to run against the real dataform CLI")
	}
	bin, err := exec.LookPath("dataform")
	if err != nil {
		t.Skip("no dataform on PATH")
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.root = filepath.Join(r.dir, "dataform")
	if err := os.CopyFS(r.root, os.DirFS(filepath.Join("..", "..", "testdata", "projects", "navigation"))); err != nil {
		t.Fatal(err)
	}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.commit("base")
	r.git("checkout", "-q", "-b", "feat")
	// Only an include changes: the action using it changes in its compiled SQL.
	r.write("includes/helpers.js", "function dateFilter(col) {\n  return `${col} >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)`;\n}\n\nmodule.exports = { dateFilter };\n")

	compile := func(ctx context.Context, dir string) (*project.Graph, error) { return dataform.Compile(ctx, bin, dir) }
	head, err := compile(context.Background(), r.root)
	if err != nil {
		t.Fatal(err)
	}
	s := &Source{Root: r.root, Key: bin, CacheDir: t.TempDir(), Compile: compile}
	res, err := s.Changes(context.Background(), Request{DefaultBranch: "main", Head: HeadWorktree, Compile: true}, head.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range res.Changed {
		got = append(got, string(c.ID)+" "+c.FileName+" "+c.ReasonsText())
	}
	want := []string{"demo-project.analytics.dev_stg_events definitions/staging/stg_events.sqlx sql"}
	if !reflect.DeepEqual(got, want) || len(res.Deleted) != 0 {
		t.Errorf("changed = %v, deleted = %v; want %v", got, res.Deleted, want)
	}
	t.Logf("%s: %v", res.Title(), got)
}
