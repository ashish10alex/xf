//go:build windows

package changes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// A junction in the tree (pnpm and npm workspaces link packages this way)
// is mirrored as a junction into the mirror, so the package is there and
// its real path is inside the snapshot.
func TestMirrorTreeKeepsJunctions(t *testing.T) {
	src := t.TempDir()
	pkg := filepath.Join(src, ".pnpm", "core@3", "node_modules", "@dataform", "core")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "index.js"), []byte("module.exports = {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "@dataform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createJunction(filepath.Join(src, "@dataform", "core"), pkg); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "node_modules")
	if err := mirrorTree(src, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "@dataform", "core", "index.js"))
	if err != nil || string(data) != "module.exports = {}" {
		t.Fatalf("mirrored package: %q, %v", data, err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dest, "@dataform", "core"))
	if err != nil {
		t.Fatal(err)
	}
	// Both resolved: a temp dir may be an 8.3 short path (C:\Users\RUNNER~1).
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(realDest, real); err != nil || !filepath.IsLocal(rel) {
		t.Errorf("the mirrored junction resolves to %s, outside the mirror %s", real, dest)
	}
}

// Git works in a repository whose root is deep: without core.longpaths,
// git can't read one deeper than about 200 characters on Windows.
func TestSnapshotOfADeepRepository(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 220 {
		dir = filepath.Join(dir, "a-fairly-long-directory-name")
	}
	root := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string]string{"workflow_settings.yaml": "defaultProject: p\n", "definitions/a.sqlx": "select 1"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"add", "."}, {"commit", "-q", "-m", "base"},
	} {
		if out, err := cmdrun.Git(t.Context(), root, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sha, err := git(t.Context(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapshot(t.Context(), root, sha, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("snapshot of a %d-character root: %v", len(root), err)
	}
	if _, err := os.Stat(filepath.Join(snap, "definitions", "a.sqlx")); err != nil {
		t.Error(err)
	}
}
