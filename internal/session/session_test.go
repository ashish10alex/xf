package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

func TestSaveThenLoad(t *testing.T) {
	path := Path(filepath.Join(t.TempDir(), "sessions"), "/work/demo")
	if got := Load(path); got.Selected != "" || got.Recent != nil {
		t.Fatalf("Load of a missing file = %+v, want the empty session", got)
	}
	want := Session{
		Root:     "/work/demo",
		Selected: "p.d.orders",
		Recent:   []project.ActionID{"p.d.orders", "p.d.customers"},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.Root != want.Root || got.Selected != want.Selected ||
		len(got.Recent) != 2 || got.Recent[1] != "p.d.customers" {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoadIgnoresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got.Selected != "" {
		t.Errorf("Load = %+v, want the empty session", got)
	}
}

func TestPathTellsProjectsApart(t *testing.T) {
	a, b := Path("/s", "/work/a/demo"), Path("/s", "/work/b/demo")
	if a == b {
		t.Errorf("two projects named demo share %s", a)
	}
	if filepath.Dir(a) != filepath.FromSlash("/s") || filepath.Base(a)[:5] != "demo-" {
		t.Errorf("Path = %s, want /s/demo-<hash>.json", a)
	}
}

func TestDir(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	home := filepath.FromSlash("/home/me")
	for _, tt := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", map[string]string{"XDG_STATE_HOME": "/xdg"}, "/xdg/xf/sessions"},
		{"darwin", nil, "/home/me/.local/state/xf/sessions"},
		{"windows", map[string]string{"AppData": "/users/me/AppData/Roaming"}, "/users/me/AppData/Roaming/xf/sessions"},
		{"windows", map[string]string{"XDG_STATE_HOME": "/xdg", "AppData": "/a"}, "/xdg/xf/sessions"},
	} {
		goos = tt.goos
		got := Dir(func(k string) string { return filepath.FromSlash(tt.env[k]) }, home)
		if got != filepath.FromSlash(tt.want) {
			t.Errorf("%s %v: %s, want %s", tt.goos, tt.env, got, filepath.FromSlash(tt.want))
		}
	}
}

// detectIn is a detect for Projects: the roots in tools are projects of
// their tool, whatever tool is wanted.
func detectIn(tools map[string]string) func(root, want string) (string, error) {
	return func(root, want string) (string, error) {
		if tool, ok := tools[root]; ok {
			return tool, nil
		}
		return "", errors.New("not a project")
	}
}

func TestVisitKeepsTheSession(t *testing.T) {
	path := Path(t.TempDir(), "/work/demo")
	if err := Save(path, Session{Root: "/work/demo", Selected: "p.d.orders"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if err := Visit(path, "/work/demo", "dbt", now); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.Selected != "p.d.orders" || got.Backend != "dbt" || !got.LastVisited.Equal(now) {
		t.Errorf("after Visit, Load = %+v", got)
	}
}

func TestProjects(t *testing.T) {
	dir := t.TempDir()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	for root, s := range map[string]Session{
		"/work/old":  {LastVisited: day(1), Backend: "dataform"},
		"/work/new":  {LastVisited: day(3), Backend: "dbt"},
		"/work/gone": {LastVisited: day(4), Backend: "dbt"},
		"/work/v1":   {}, // from before sessions had a time or a backend
	} {
		s.Root = root
		if err := Save(Path(dir, root), s); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(Path(dir, "/work/v1"), day(2), day(2)); err != nil {
		t.Fatal(err)
	}
	got := Projects(dir, detectIn(map[string]string{"/work/old": "dataform", "/work/new": "dbt", "/work/v1": "dataform"}))
	var roots []string
	for _, p := range got {
		roots = append(roots, p.Root+" "+p.Backend)
	}
	want := []string{"/work/new dbt", "/work/v1 dataform", "/work/old dataform"}
	if len(roots) != len(want) {
		t.Fatalf("Projects = %v, want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("Projects = %v, want %v", roots, want)
		}
	}

	if err := Forget(got[0]); err != nil {
		t.Fatal(err)
	}
	if after := Projects(dir, detectIn(map[string]string{"/work/new": "dbt"})); len(after) != 0 {
		t.Errorf("after Forget, Projects = %+v", after)
	}
	if Load(Path(dir, "/work/gone")).Root == "" {
		t.Error("a project that is gone lost its session")
	}
}

func TestProjectsListsOnePlaceOnce(t *testing.T) {
	dir, real := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for root, at := range map[string]time.Time{real: now, link: now.Add(time.Hour)} {
		if err := Visit(Path(dir, root), root, "dbt", at); err != nil {
			t.Fatal(err)
		}
	}
	got := Projects(dir, func(string, string) (string, error) { return "dbt", nil })
	if len(got) != 1 || got[0].Root != link || len(got[0].Files) != 2 {
		t.Fatalf("Projects = %+v, want the link's spelling alone, with both files", got)
	}
}

func TestAdd(t *testing.T) {
	dir, real := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	visited := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if err := Save(Path(dir, real), Session{Root: real, Backend: "dataform", Selected: "p.d.orders", LastVisited: visited}); err != nil {
		t.Fatal(err)
	}
	if added, err := Add(dir, real, "dbt"); err != nil || added {
		t.Fatalf("Add of a known project = %v, %v; want false", added, err)
	}
	if err := os.Symlink(real, link); err == nil {
		if added, err := Add(dir, link, "dbt"); err != nil || added {
			t.Fatalf("Add of a known project through a link = %v, %v; want false", added, err)
		}
		if _, err := os.Stat(Path(dir, link)); !os.IsNotExist(err) {
			t.Error("Add of a known project through a link saved a session for the link")
		}
	}
	if s := Load(Path(dir, real)); s.Backend != "dataform" || s.Selected != "p.d.orders" || !s.LastVisited.Equal(visited) {
		t.Errorf("a known project's session changed: %+v", s)
	}

	fresh := filepath.Join(t.TempDir(), "fresh")
	if added, err := Add(dir, fresh, "dbt"); err != nil || !added {
		t.Fatalf("Add of a new project = %v, %v; want true", added, err)
	}
	if s := Load(Path(dir, fresh)); s.Root != fresh || s.Backend != "dbt" || !s.LastVisited.IsZero() {
		t.Errorf("a new project's session = %+v, want its root and tool, not visited", s)
	}
}

func TestProjectsListsUnvisitedLastByName(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for root, s := range map[string]Session{
		"/work/zeta":    {LastVisited: day, Backend: "dataform"},
		"/work/beta":    {Backend: "dbt"}, // found by xf search
		"/other/alpha":  {Backend: "dataform"},
		"/work/alpha":   {Backend: "dbt"},
		"/work/visited": {LastVisited: day.Add(time.Hour), Backend: "dbt"},
	} {
		s.Root = root
		if err := Save(Path(dir, root), s); err != nil {
			t.Fatal(err)
		}
	}
	got := Projects(dir, func(string, string) (string, error) { return "dbt", nil })
	var roots []string
	for _, p := range got {
		roots = append(roots, p.Root)
		if p.Root == "/work/beta" && !p.LastVisited.IsZero() {
			t.Errorf("an unvisited project has the time %v", p.LastVisited)
		}
	}
	want := []string{"/work/visited", "/work/zeta", "/other/alpha", "/work/alpha", "/work/beta"}
	if !slices.Equal(roots, want) {
		t.Errorf("Projects = %v, want %v", roots, want)
	}
}
