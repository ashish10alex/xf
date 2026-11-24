package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/agentskills"
)

func testSkillsEnv(t *testing.T, agents ...string) skillsEnv {
	t.Helper()
	home := t.TempDir()
	for _, a := range agents {
		if err := os.MkdirAll(filepath.Join(home, a), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	return skillsEnv{
		home:        home,
		getenv:      func(string) string { return "" },
		projectRoot: func() (string, error) { return root, nil },
		in:          strings.NewReader(""),
	}
}

func runSkillsCmd(t *testing.T, env skillsEnv, args ...string) string {
	t.Helper()
	var b strings.Builder
	if err := skills(&b, env, args); err != nil {
		t.Fatalf("xf skills %v: %v", args, err)
	}
	return b.String()
}

func TestSkillsInstallAsksAtATerminal(t *testing.T) {
	env := testSkillsEnv(t, ".claude", ".codex")
	env.interactive = true
	env.in = strings.NewReader("n\n")
	out := runSkillsCmd(t, env, "install")
	for _, want := range []string{"Claude Code", filepath.Join("~", ".claude", "skills"), "Codex", "Install? [Y/n]", "nothing installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(env.home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatal("installed after a no")
	}

	env.in = strings.NewReader("\n")
	out = runSkillsCmd(t, env, "install")
	if !strings.Contains(out, "installed "+filepath.Join("~", ".agents", "skills", "xf-run")) {
		t.Fatalf("enter didn't install:\n%s", out)
	}
}

func TestSkillsInstallNeedsAnAgent(t *testing.T) {
	env := testSkillsEnv(t)
	var b strings.Builder
	err := skills(&b, env, []string{"install"})
	if err == nil || !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("install with no agent: %v", err)
	}
	if err := skills(&b, env, []string{"install", "--agent", "vscode"}); err == nil {
		t.Fatal("unknown agent accepted")
	}
	// --agent installs for an agent that isn't found.
	out := runSkillsCmd(t, env, "install", "--agent", "cursor")
	if !strings.Contains(out, "installed "+filepath.Join("~", ".cursor", "skills", "xf-checks")) {
		t.Fatalf("--agent cursor:\n%s", out)
	}
}

func TestSkillsSkipsClaudeWithThePlugin(t *testing.T) {
	env := testSkillsEnv(t, ".claude")
	plugins := filepath.Join(env.home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(plugins), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugins, []byte(`{"plugins":{"xf@xf":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runSkillsCmd(t, env, "install")
	if !strings.Contains(out, "skipping Claude Code") {
		t.Fatalf("installed beside the plugin:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(env.home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatal("wrote skills beside the plugin")
	}
}

func TestSkillsProjectSharesAgentsFolder(t *testing.T) {
	env := testSkillsEnv(t, ".codex", ".gemini/config")
	out := runSkillsCmd(t, env, "--project")
	if !strings.Contains(out, "Codex, Antigravity") {
		t.Fatalf("Codex and Antigravity should share .agents/skills:\n%s", out)
	}
	runSkillsCmd(t, env, "install", "--project")
	root, _ := env.projectRoot()
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "xf-checks", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.home, ".agents")); !os.IsNotExist(err) {
		t.Fatal("--project wrote to the home folder")
	}
}

func TestSkillsUpdateQuiet(t *testing.T) {
	env := testSkillsEnv(t, ".claude")
	if out := runSkillsCmd(t, env, "update", "--quiet"); out != "" {
		t.Fatalf("update --quiet with nothing installed said:\n%s", out)
	}
	runSkillsCmd(t, env, "install")
	if out := runSkillsCmd(t, env, "update", "--quiet"); out != "" {
		t.Fatalf("update --quiet with nothing out of date said:\n%s", out)
	}
	if out := runSkillsCmd(t, env, "update"); !strings.Contains(out, "is up to date") {
		t.Fatalf("update:\n%s", out)
	}
}

func TestSkillsNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	if n := skillsNotice(); n != "" {
		t.Fatalf("notice with no skills: %q", n)
	}
	dir := filepath.Join(home, ".claude", "skills", "xf-run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), agentskills.Marked([]byte("old\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := skillsNotice(); !strings.Contains(n, "xf skills update") {
		t.Fatalf("notice for an older xf's skill: %q", n)
	}
}
