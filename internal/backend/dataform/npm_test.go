package dataform

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// npmInstall lays out a global npm install as on Windows: the dataform.cmd
// shim beside node.exe and node_modules/@dataform/cli.
func npmInstall(t *testing.T) (dir, shim, bundle string) {
	t.Helper()
	dir = t.TempDir()
	shim = filepath.Join(dir, "dataform.cmd")
	bundle = filepath.Join(dir, "node_modules", "@dataform", "cli", "bundle.js")
	for _, f := range []string{shim, bundle, filepath.Join(dir, "node.exe"), filepath.Join(filepath.Dir(bundle), "package.json")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, shim, bundle
}

// npm's Windows shim is run as node on the CLI's bundle, keeping cmd.exe out
// of the run's process tree; anything else runs as it is.
func TestCommandSkipsTheNpmShim(t *testing.T) {
	dir, shim, bundle := npmInstall(t)
	prog, prefix, _ := command(shim)
	if prog != filepath.Join(dir, "node.exe") || !slices.Equal(prefix, []string{bundle}) {
		t.Errorf("shim runs %s %q", prog, prefix)
	}
	if prog, prefix, _ := command(filepath.Join(dir, "dataform")); prog != filepath.Join(dir, "dataform") || prefix != nil {
		t.Errorf("a plain binary runs %s %q", prog, prefix)
	}
	if NpmBundle(filepath.Join(t.TempDir(), "dataform.cmd")) != "" {
		t.Error("a .cmd with no @dataform/cli beside it counts as npm's shim")
	}
}
