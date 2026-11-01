package impact

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// TestDevToProdWithDataform compiles testdata/projects/basic with and
// without a schema suffix using the real dataform CLI, and checks that every
// dev target folds into its prod one. It needs `dataform` on PATH, so it
// runs only with XF_E2E=1.
func TestDevToProdWithDataform(t *testing.T) {
	if os.Getenv("XF_E2E") == "" {
		t.Skip("set XF_E2E=1 to run against the real dataform CLI")
	}
	bin, err := exec.LookPath("dataform")
	if err != nil {
		t.Skip("no dataform on PATH")
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "testdata", "projects", "basic"))); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prod, err := dataform.Compile(ctx, bin, root)
	if err != nil || prod.HasErrors() {
		t.Fatalf("prod compile: %v %v", err, prod.Errors)
	}
	args, _ := dataform.SplitOptions("--schema-suffix=ci")
	dev, err := dataform.CompileWith(ctx, bin, root, args)
	if err != nil || dev.HasErrors() {
		t.Fatalf("dev compile: %v %v", err, dev.Errors)
	}
	var deletedID project.ActionID
	n := 0
	m := DevToProd(dev, prod, nil)
	for id, a := range prod.Actions {
		if !mapped(a) || a.Kind == project.KindDeclaration {
			continue
		}
		n++
		devID := a.Target.Database + "." + a.Target.Schema + "_ci." + a.Target.Name
		if got := m[devID]; got != string(id) {
			t.Errorf("%s folds into %q, want %s", devID, got, id)
		}
		if a.Kind == project.KindTable {
			deletedID = project.ActionID(a.Target.Database + "." + a.Target.Schema + ".gone")
		}
	}
	if n == 0 {
		t.Fatal("no actions compared")
	}
	// A deleted table's dev run is guessed from a table in its dataset.
	m = DevToProd(dev, prod, []changes.Deleted{{ID: deletedID}})
	schema := string(deletedID[:len(deletedID)-len(".gone")])
	if got := m[schema+"_ci.gone"]; got != string(deletedID) {
		t.Errorf("deleted: %v", m)
	}
}
