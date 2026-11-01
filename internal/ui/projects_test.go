package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/session"
)

var pickerNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func pickerProjects() []session.Project {
	return []session.Project{
		{Root: filepath.FromSlash("/home/me/work/analytics-dbt"), Backend: "dbt", LastVisited: pickerNow.Add(-2 * time.Hour)},
		{Root: filepath.FromSlash("/home/me/work/client-a/dataform"), Backend: "dataform", LastVisited: pickerNow.Add(-26 * time.Hour)},
		{Root: filepath.FromSlash("/srv/warehouse"), Backend: "dataform", LastVisited: pickerNow.Add(-15 * 24 * time.Hour)},
	}
}

func newPicker(deps PickerDeps) ProjectPicker {
	if deps.Projects == nil {
		deps.Projects = pickerProjects()
	}
	deps.Home = filepath.FromSlash("/home/me")
	deps.Now = func() time.Time { return pickerNow }
	return NewProjectPicker(deps)
}

func pickerPress(t *testing.T, p ProjectPicker, msgs ...tea.Msg) ProjectPicker {
	t.Helper()
	for _, msg := range msgs {
		m, _ := p.Update(msg)
		p = m.(ProjectPicker)
	}
	return p
}

func TestProjectPickerListsProjects(t *testing.T) {
	view := ansi.Strip(newPicker(PickerDeps{}).render())
	for _, want := range []string{
		"> analytics-dbt  dbt       ~/work/analytics-dbt      2h ago",
		"  dataform       dataform  ~/work/client-a/dataform  yesterday",
		"  warehouse      dataform  /srv/warehouse            2w ago",
	} {
		// The paths are the OS's own, as the roots are.
		if want = filepath.FromSlash(want); !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestProjectPickerOpensTheSelected(t *testing.T) {
	p := pickerPress(t, newPicker(PickerDeps{}), press("j", "enter")...)
	if got, ok := p.Chosen(); !ok || got.Root != filepath.FromSlash("/home/me/work/client-a/dataform") {
		t.Errorf("Chosen = %+v, %v", got, ok)
	}
	p = pickerPress(t, newPicker(PickerDeps{}), press("q")...)
	if got, ok := p.Chosen(); ok {
		t.Errorf("after q, Chosen = %+v", got)
	}
}

func TestProjectPickerFilters(t *testing.T) {
	p := pickerPress(t, newPicker(PickerDeps{}), append(press("/"), typeText("dataform srv")...)...)
	view := ansi.Strip(p.render())
	if strings.Contains(view, "analytics-dbt") || strings.Contains(view, "client-a") || !strings.Contains(view, "> warehouse") {
		t.Errorf("filtered view:\n%s", view)
	}
	// q is text while filtering; enter opens the match.
	p = pickerPress(t, p, press("enter")...)
	if got, _ := p.Chosen(); got.Root != filepath.FromSlash("/srv/warehouse") {
		t.Errorf("Chosen = %+v", got)
	}
}

func TestProjectPickerStartsBesideTheCurrentProject(t *testing.T) {
	p := newPicker(PickerDeps{Current: filepath.FromSlash("/home/me/work/analytics-dbt")})
	view := ansi.Strip(p.render())
	if !strings.Contains(view, "2h ago · here") || !strings.Contains(view, "> dataform") {
		t.Errorf("view:\n%s", view)
	}
}

func TestProjectPickerForgets(t *testing.T) {
	var forgot []string
	deps := PickerDeps{Forget: func(p session.Project) error {
		forgot = append(forgot, p.Root)
		return nil
	}}
	p := pickerPress(t, newPicker(deps), press("x")...)
	if !strings.Contains(ansi.Strip(p.render()), "forget analytics-dbt?") {
		t.Errorf("no confirmation:\n%s", ansi.Strip(p.render()))
	}
	p = pickerPress(t, p, press("n")...)
	if len(forgot) != 0 {
		t.Fatalf("forgot %v without y", forgot)
	}
	p = pickerPress(t, p, press("x", "y")...)
	view := ansi.Strip(p.render())
	if len(forgot) != 1 || forgot[0] != filepath.FromSlash("/home/me/work/analytics-dbt") || strings.Contains(view, filepath.FromSlash("~/work/analytics-dbt")) {
		t.Errorf("forgot %v; view:\n%s", forgot, view)
	}

	failing := PickerDeps{Forget: func(session.Project) error { return errors.New("read-only") }}
	p = pickerPress(t, newPicker(failing), press("x", "y")...)
	if view := ansi.Strip(p.render()); !strings.Contains(view, "read-only") || !strings.Contains(view, "> analytics-dbt") {
		t.Errorf("a failed forget should say so and keep the project:\n%s", view)
	}
}

func TestProjectPickerKeepsTheTimeInANarrowTerminal(t *testing.T) {
	p := pickerPress(t, newPicker(PickerDeps{}), tea.WindowSizeMsg{Width: 50, Height: 10})
	view := ansi.Strip(p.render())
	if !strings.Contains(view, filepath.FromSlash("dataform  …/dataform  yesterday│")) {
		t.Errorf("view:\n%s", view)
	}
}

func TestRelTime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:     "just now",
		5 * time.Minute:      "5m ago",
		3 * time.Hour:        "3h ago",
		30 * time.Hour:       "yesterday",
		4 * 24 * time.Hour:   "4d ago",
		21 * 24 * time.Hour:  "3w ago",
		90 * 24 * time.Hour:  "3mo ago",
		800 * 24 * time.Hour: "2y ago",
	} {
		if got := relTime(pickerNow, pickerNow.Add(-d)); got != want {
			t.Errorf("relTime(%v) = %q, want %q", d, got, want)
		}
	}
}

// o opens the selected project in the editor: the picker stays for a
// window editor, runs a terminal one, and leaves for neovim beside xf.
func TestProjectPickerOpensInEditor(t *testing.T) {
	var asked []string
	var opening Opening
	var fail error
	p := newPicker(PickerDeps{Open: func(it session.Project) (Opening, error) {
		asked = append(asked, it.Root)
		return opening, fail
	}})
	if view := ansi.Strip(p.render()); !strings.Contains(view, "o open in editor") {
		t.Errorf("footer lacks o:\n%s", view)
	}
	pressO := func(p ProjectPicker) (ProjectPicker, tea.Cmd) {
		t.Helper()
		m, cmd := p.Update(press("o")[0])
		if cmd == nil {
			t.Fatal("o did nothing")
		}
		m, cmd = m.Update(cmd())
		return m.(ProjectPicker), cmd
	}

	p = pickerPress(t, p, press("j")...)
	p, _ = pressO(p)
	if len(asked) != 1 || asked[0] != filepath.FromSlash("/home/me/work/client-a/dataform") {
		t.Errorf("asked to open %q", asked)
	}
	if _, ok := p.Chosen(); ok || p.quitting {
		t.Error("the picker left after a window editor opened the project")
	}
	if view := ansi.Strip(p.render()); !strings.Contains(view, "opened dataform in the editor") {
		t.Errorf("view doesn't say it opened:\n%s", view)
	}

	fail = errors.New("code isn't on PATH")
	p, _ = pressO(p)
	if view := ansi.Strip(p.render()); !strings.Contains(view, "could not open dataform: code isn't on PATH") {
		t.Errorf("view doesn't say why it failed:\n%s", view)
	}

	fail, opening = nil, Opening{Leave: true}
	p, _ = pressO(p)
	if got, ok := p.Chosen(); !ok || !p.InEditor() || got.Root != filepath.FromSlash("/home/me/work/client-a/dataform") {
		t.Errorf("Chosen = %+v, %v; InEditor %v", got, ok, p.InEditor())
	}

	// Without an opener there is no o.
	p = newPicker(PickerDeps{})
	if view := ansi.Strip(p.render()); strings.Contains(view, "open in editor") {
		t.Errorf("footer offers o:\n%s", view)
	}
	if _, cmd := p.Update(press("o")[0]); cmd != nil {
		t.Error("o did something without an opener")
	}
}
