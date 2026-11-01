package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

const testDebounce = 50 * time.Millisecond

// dataformSpec is what the Dataform backend watches.
var dataformSpec = Spec{
	Dirs:      []string{"definitions", "includes"},
	Exts:      map[string]bool{".sqlx": true, ".js": true, ".sql": true, ".json": true, ".yaml": true, ".yml": true, ".ipynb": true},
	RootFiles: []string{"workflow_settings.yaml"},
}

func newProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"definitions/sub", "includes", "node_modules/x"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "workflow_settings.yaml"), "defaultProject: p\n")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countEvents collects notifications for the given window.
func countEvents(ch <-chan struct{}, window time.Duration) int {
	n := 0
	deadline := time.After(window)
	for {
		select {
		case <-ch:
			n++
		case <-deadline:
			return n
		}
	}
}

func startWatch(t *testing.T, root string) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := Watch(ctx, root, dataformSpec, testDebounce)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestWatchNotifies(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		want   int
	}{
		{"sqlx in definitions", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "definitions", "a.sqlx"), "select 1")
		}, 1},
		{"nested js", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "definitions", "sub", "b.js"), "//")
		}, 1},
		{"includes", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "includes", "c.js"), "//")
		}, 1},
		{"workflow settings", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "workflow_settings.yaml"), "defaultProject: q\n")
		}, 1},
		{"burst debounced to one", func(t *testing.T, root string) {
			for i := 0; i < 5; i++ {
				write(t, filepath.Join(root, "definitions", "a.sqlx"), "select 1")
				time.Sleep(5 * time.Millisecond)
			}
		}, 1},
		{"vim style save: probe, backup, rename", func(t *testing.T, root string) {
			d := filepath.Join(root, "definitions")
			write(t, filepath.Join(d, "a.sqlx"), "select 1")
			time.Sleep(3 * testDebounce) // let the initial write settle
			write(t, filepath.Join(d, "4913"), "")
			_ = os.Remove(filepath.Join(d, "4913"))
			_ = os.Rename(filepath.Join(d, "a.sqlx"), filepath.Join(d, "a.sqlx~"))
			write(t, filepath.Join(d, "a.sqlx"), "select 2")
			_ = os.Remove(filepath.Join(d, "a.sqlx~"))
		}, 2}, // the initial write, then the save
		{"new directory is watched", func(t *testing.T, root string) {
			d := filepath.Join(root, "definitions", "new")
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
			// At once, before the watcher has added it: the file is found
			// when it does.
			write(t, filepath.Join(d, "x.sqlx"), "select 1")
		}, 1},
		{"new directory, then a file in it later", func(t *testing.T, root string) {
			d := filepath.Join(root, "definitions", "later")
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
			time.Sleep(3 * testDebounce)
			write(t, filepath.Join(d, "x.sqlx"), "select 1")
		}, 1},
		{"ignored: swap file, other ext, root file, node_modules, hidden", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "definitions", ".a.sqlx.swp"), "")
			write(t, filepath.Join(root, "definitions", "notes.md"), "")
			write(t, filepath.Join(root, "package.json"), "{}")
			write(t, filepath.Join(root, "node_modules", "x", "y.js"), "")
			write(t, filepath.Join(root, "definitions", ".hidden.sqlx"), "")
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newProject(t)
			ch := startWatch(t, root)
			tt.change(t, root)
			if got := countEvents(ch, 6*testDebounce); got != tt.want {
				t.Errorf("got %d notifications, want %d", got, tt.want)
			}
		})
	}
}

func TestWatchClosesOnCancel(t *testing.T) {
	root := newProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx, root, dataformSpec, testDebounce)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("got a notification instead of close")
		}
	case <-time.After(time.Second):
		t.Error("channel not closed after cancel")
	}
}

func TestRelevant(t *testing.T) {
	root := "/p"
	tests := []struct {
		name string
		op   fsnotify.Op
		want bool
	}{
		{"/p/definitions/a.sqlx", fsnotify.Write, true},
		{"/p/definitions/a.SQLX", fsnotify.Create, true},
		{"/p/definitions/a.sqlx", fsnotify.Rename, true},
		{"/p/definitions/a.sqlx", fsnotify.Remove, true},
		{"/p/definitions/a.sqlx", fsnotify.Chmod, false},
		{"/p/workflow_settings.yaml", fsnotify.Write, true},
		{"/p/other.yaml", fsnotify.Write, false},
		{"/p/definitions/a.sqlx~", fsnotify.Create, false},
		{"/p/definitions/4913", fsnotify.Create, false},
		{"/p/definitions/.a.sqlx.swp", fsnotify.Write, false},
		{"/p/definitions/readme.md", fsnotify.Write, false},
	}
	for _, tt := range tests {
		t.Run(tt.name+" "+tt.op.String(), func(t *testing.T) {
			if got := dataformSpec.relevant(root, fsnotify.Event{Name: tt.name, Op: tt.op}); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelevantEveryDir(t *testing.T) {
	spec := Spec{Skip: []string{"target"}, Exts: map[string]bool{".sql": true, ".yml": true}, RootFiles: []string{"dbt_project.yml"}}
	root := t.TempDir()
	for _, d := range []string{"models/marts", "target/compiled", ".git", "seeds"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := spec.dirs(root); len(got) != 2 || got[0] != "models" || got[1] != "seeds" {
		t.Errorf("dirs = %v, want [models seeds]", got)
	}
	for path, want := range map[string]bool{
		"models/marts":    true,
		"target/compiled": false,
		".git":            false,
	} {
		if got := spec.isWatchedDir(root, filepath.Join(root, path)); got != want {
			t.Errorf("isWatchedDir(%s) = %v, want %v", path, got, want)
		}
	}
	ch := startWatchSpec(t, root, spec)
	write(t, filepath.Join(root, "target", "compiled", "a.sql"), "select 1")
	if n := countEvents(ch, 5*testDebounce); n != 0 {
		t.Errorf("a write under target/ notified %d times", n)
	}
	write(t, filepath.Join(root, "models", "marts", "a.sql"), "select 1")
	if n := countEvents(ch, 5*testDebounce); n != 1 {
		t.Errorf("a write under models/ notified %d times, want 1", n)
	}
}

func startWatchSpec(t *testing.T, root string, spec Spec) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := Watch(ctx, root, spec, testDebounce)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// A spec with AnyFile counts every file and folder, in the root too, but
// not git's own files, skipped folders or an editor's temporary files.
func TestRelevantAnyFile(t *testing.T) {
	spec := Spec{Skip: []string{"target"}, AnyFile: true}
	for name, want := range map[string]bool{
		"/p/definitions/readme.md":  true,
		"/p/definitions/new-folder": true,
		"/p/README.md":              true,
		"/p/.gitignore":             true,
		"/p/.git":                   false,
		"/p/target":                 false,
		"/p/definitions/a.sqlx~":    false,
		"/p/definitions/.a.swp":     false,
	} {
		if got := spec.relevant("/p", fsnotify.Event{Name: name, Op: fsnotify.Create}); got != want {
			t.Errorf("relevant(%s) = %v, want %v", name, got, want)
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ch := startWatchSpec(t, root, spec)
	write(t, filepath.Join(root, "docs", "notes.txt"), "x")
	if n := countEvents(ch, 5*testDebounce); n != 1 {
		t.Errorf("a new file notified %d times, want 1", n)
	}
	if err := os.Mkdir(filepath.Join(root, "docs", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(ch, 5*testDebounce); n != 1 {
		t.Errorf("a new folder notified %d times, want 1", n)
	}
}
