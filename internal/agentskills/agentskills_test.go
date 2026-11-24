package agentskills

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func states(t *testing.T, dir string) map[string]State {
	t.Helper()
	m := map[string]State{}
	for _, st := range Check(dir) {
		m[st.Skill.Name] = st.State
	}
	return m
}

func TestBundledSkills(t *testing.T) {
	b := Bundled()
	var names []string
	for _, s := range b {
		names = append(names, s.Name)
		if !bytes.HasPrefix(s.Body, []byte("---\nname: "+s.Name+"\n")) {
			t.Errorf("%s's SKILL.md doesn't start with its name in the frontmatter", s.Name)
		}
	}
	if got := strings.Join(names, ","); got != "xf-checks,xf-run" {
		t.Fatalf("bundled skills = %s", got)
	}
	// xf writes only SKILL.md: a skill that gains another file needs
	// Install and Uninstall to handle it.
	entries, err := os.ReadDir("../../skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join("../../skills", e.Name()))
		if len(files) != 1 || files[0].Name() != "SKILL.md" {
			t.Errorf("skills/%s holds more than SKILL.md, which xf skills install doesn't write", e.Name())
		}
	}
}

func TestInstallUpdateUninstall(t *testing.T) {
	dir := t.TempDir()
	for name, s := range states(t, dir) {
		if s != Missing {
			t.Fatalf("%s: %v before install", name, s)
		}
	}
	acts, err := Install(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if !a.Done {
			t.Errorf("%s not installed", a.Skill.Name)
		}
	}
	for name, s := range states(t, dir) {
		if s != Current {
			t.Errorf("%s: %v after install", name, s)
		}
	}

	// An older xf's skill, unedited, is out of date; Update rewrites it.
	checks := filepath.Join(dir, "xf-checks", "SKILL.md")
	if err := os.WriteFile(checks, Marked([]byte("---\nname: xf-checks\n---\nold\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-checks"]; s != Outdated {
		t.Fatalf("older xf's skill: %v", s)
	}
	if acts, err := Update(dir); err != nil || !acts[0].Done || acts[1].Done {
		t.Fatalf("Update = %+v, %v", acts, err)
	}
	if s := states(t, dir)["xf-checks"]; s != Current {
		t.Fatalf("after Update: %v", s)
	}

	// An edit since makes it the user's: neither Update nor Install
	// replaces it, and Uninstall leaves it without force.
	b, _ := os.ReadFile(checks)
	edited := bytes.Replace(b, []byte("# xf checks"), []byte("# my checks"), 1)
	if err := os.WriteFile(checks, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-checks"]; s != Edited {
		t.Fatalf("edited skill: %v", s)
	}
	if _, err := Update(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(dir, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(checks); !bytes.Equal(got, edited) {
		t.Fatal("an edited skill was replaced or removed without force")
	}
	if _, err := os.Stat(filepath.Join(dir, "xf-run")); !os.IsNotExist(err) {
		t.Fatalf("xf-run's folder is still there after uninstall: %v", err)
	}
	if _, err := Install(dir, true); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-checks"]; s != Current {
		t.Fatalf("after a forced install: %v", s)
	}
}

func TestOtherToolsSkillsAreLeftAlone(t *testing.T) {
	dir := t.TempDir()
	// npx skills add, or a curl, writes the skill as it is, unmarked.
	own := filepath.Join(dir, "xf-checks", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	body := Bundled()[0].Body
	if err := os.WriteFile(own, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-checks"]; s != Foreign {
		t.Fatalf("unmarked skill: %v", s)
	}
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(dir, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(own); !bytes.Equal(got, body) {
		t.Fatal("another tool's skill was replaced or removed")
	}

	// npx skills links each agent's folder to one copy.
	if runtime.GOOS == "windows" {
		return
	}
	shared := filepath.Join(t.TempDir(), "xf-run")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, "xf-run")); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-run"]; s != Linked {
		t.Fatalf("linked skill: %v", s)
	}
	if _, err := Install(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("install wrote through another tool's link")
	}
}

func TestCRLFStillMatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "xf-run", "SKILL.md")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := states(t, dir)["xf-run"]; s != Current {
		t.Fatalf("CRLF copy: %v", s)
	}
}

func TestAgents(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, "cc")}
	agents := Agents(home, func(k string) string { return env[k] })
	byID := map[string]Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	if got := byID["claude"].UserDir; got != filepath.Join(home, "cc", "skills") {
		t.Errorf("claude's skills with CLAUDE_CONFIG_DIR: %s", got)
	}
	if got := byID["codex"].UserDir; got != filepath.Join(home, ".agents", "skills") {
		t.Errorf("codex's skills: %s", got)
	}
	if byID["cursor"].Installed() {
		t.Error("cursor installed with no ~/.cursor")
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !byID["cursor"].Installed() {
		t.Error("cursor not found with ~/.cursor")
	}

	claude := byID["claude"]
	if claude.HasPlugin() {
		t.Error("plugin found with no plugins file")
	}
	plugins := filepath.Join(claude.Home, "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(plugins), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugins, []byte(`{"version":2,"plugins":{"xf@xf":[{"scope":"user"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !claude.HasPlugin() {
		t.Error("xf@xf plugin not found")
	}
}

func TestStale(t *testing.T) {
	home := t.TempDir()
	agents := Agents(home, func(string) string { return "" })
	if got := Stale(agents); len(got) != 0 {
		t.Fatalf("stale with nothing installed: %v", got)
	}
	dir := agents[1].UserDir // ~/.agents/skills
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xf-run", "SKILL.md"), Marked([]byte("old\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Stale(agents)
	if len(got) != 1 || got[0].Skill.Name != "xf-run" {
		t.Fatalf("Stale = %+v", got)
	}
}
