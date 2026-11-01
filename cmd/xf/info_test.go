package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/testutil"
)

func TestProjectInfo(t *testing.T) {
	root := t.TempDir()
	userDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userDir)
	t.Setenv("DBT_TARGET", "")
	for path, data := range map[string]string{
		filepath.Join(root, "dbt_project.yml"):       "name: shop\n",
		filepath.Join(userDir, "xf", "config.json"):  `{"editor": "nvim", "edtor": "x"}`,
		filepath.Join(root, ".xf", "unused"):         "",
		filepath.Join(root, "models", "placeholder"): "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := testutil.FakeCLI(t, "dbt")
	fs, _, sf, _ := tuiFlagSet()
	if help, err := parseArgs(fs, []string{"--dbt-bin", bin, "--dbt-target", "dev"}); help || err != nil {
		t.Fatal(help, err)
	}
	settings, err := sf.load(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := openBackend(backend.DBT, root, settings)
	if err != nil {
		t.Fatal(err)
	}
	info := projectInfo(root, backend.DBT, b, settings)

	if info.BackendFrom != "detected" || info.Target != "dev" || info.TargetFrom != "--dbt-target" {
		t.Errorf("backend from %q, target %q from %q", info.BackendFrom, info.Target, info.TargetFrom)
	}
	if info.ToolBin != bin || info.ToolFrom != "--dbt-bin" || !filepath.IsAbs(info.Exe) {
		t.Errorf("tool %q from %q, xf %q", info.ToolBin, info.ToolFrom, info.Exe)
	}
	if info.Socket != remote.SocketPath(root, backend.EngineName(b)) || info.Log != remote.LogPath(root, backend.EngineName(b)) {
		t.Errorf("engine at %q, log %q", info.Socket, info.Log)
	}
	if len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "edtor") || info.EditorCommand != "nvim" {
		t.Errorf("warnings %q, editor %q", info.Warnings, info.EditorCommand)
	}

	// The config files by their full paths.
	if len(info.Files) != 2 ||
		info.Files[0].Path != filepath.Join(userDir, "xf", "config.json") || !info.Files[0].Found ||
		info.Files[1].Path != filepath.Join(root, config.ProjectFile) || info.Files[1].Found {
		t.Errorf("files = %+v", info.Files)
	}

	// A dbt project's settings only, each as xf config prints it.
	var keys []string
	for _, s := range info.Settings {
		keys = append(keys, s.Key)
		want := settingRows(settings, root, []string{s.Key})[0]
		if s.Value != want.value || s.From != want.from {
			t.Errorf("%s = %q from %q, but xf config has %q from %q", s.Key, s.Value, s.From, want.value, want.from)
		}
	}
	if got := strings.Join(keys, " "); got != "dbtProdTarget deferToProd dbtCompileWithHooks defaultBranch editor theme webTheme webThemeLight webThemeDark scratchWarnGB scratchConfirmGB sourceAutosaveMs diffStyle pythonLanguageServer autoHidePane2 pane2Tab wrapLines webAutoExitSeconds webOpenProjectIn webBrowser forgePollSeconds vimMode" {
		t.Errorf("settings = %s", got)
	}

	if v, err := info.ToolVersion(context.Background()); err != nil || v != "dbt 2.0.6" {
		t.Errorf("version = %q, %v", v, err)
	}
	if _, err := info.Git(context.Background()); err == nil || err.Error() != "not a git repository" {
		t.Errorf("git outside a repository: %v", err)
	}
}

// Every setting is in the project info of the tool it belongs to, in its
// table or, for the binary and dbt's target, beside them; the vim keys,
// only xf web's editor's, are on its settings page.
func TestInfoSettingsCoverEveryKey(t *testing.T) {
	shown := map[string]bool{config.KeyDataformBin: true, config.KeyDbtBin: true, config.KeyDbtTarget: true}
	for _, keys := range infoSettings {
		for _, k := range keys {
			shown[k] = true
		}
	}
	for _, k := range config.Keys {
		if !shown[k] && !config.IsVimKey(k) {
			t.Errorf("no project info shows %s", k)
		}
	}
}

func TestDataformVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "workflow_settings.yaml"), []byte("defaultProject: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _, sf, _ := tuiFlagSet()
	if help, err := parseArgs(fs, []string{"--dataform-bin", testutil.FakeCLI(t, "dataform")}); help || err != nil {
		t.Fatal(help, err)
	}
	settings, err := sf.load(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := openBackend(backend.Dataform, root, settings)
	if err != nil {
		t.Fatal(err)
	}
	info := projectInfo(root, backend.Dataform, b, settings)
	if v, err := info.ToolVersion(context.Background()); err != nil || v != "3.0.8" {
		t.Errorf("version = %q, %v", v, err)
	}
	if info.Target != "" || info.ToolFrom != "--dataform-bin" {
		t.Errorf("target %q, tool from %q", info.Target, info.ToolFrom)
	}
}

func TestBuildText(t *testing.T) {
	built := time.Date(2026, 10, 5, 12, 30, 0, 0, time.Local)
	read := func(bi *debug.BuildInfo) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return bi, bi != nil }
	}
	checkout := &debug.BuildInfo{GoVersion: "go1.26.0", Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "91ec6140aa7c0de"}, {Key: "vcs.time", Value: "2026-10-05T10:00:00Z"}, {Key: "vcs.modified", Value: "true"},
	}}
	for want, bi := range map[string]*debug.BuildInfo{
		"devel 91ec614 2026-10-05 10:00 UTC with uncommitted changes (go1.26.0)": checkout,
		"devel fe50330 (go1.26.0)": {GoVersion: "go1.26.0", Main: debug.Module{Version: "v0.0.0-20261005224111-fe5033068497+dirty"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "fe5033068497aa"}}},
		"v0.3.1 (go1.26.0)":        {GoVersion: "go1.26.0", Main: debug.Module{Version: "v0.3.1"}},
		"built 2026-10-05 12:30":   {GoVersion: "go1.26.0", Main: debug.Module{Version: "(devel)"}},
	} {
		if got := buildText(read(bi), built); got != want {
			t.Errorf("buildText = %q, want %q", got, want)
		}
	}
	if got := buildText(read(nil), time.Time{}); got != "(unknown build)" {
		t.Errorf("no build info: %q", got)
	}
}

func TestVersionRequested(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-version"}, {"-v"}, {"version"}} {
		if !versionRequested(args) {
			t.Errorf("%v: not a version request", args)
		}
	}
	for _, args := range [][]string{nil, {"web", "--version"}, {"--dir", "version"}} {
		if versionRequested(args) {
			t.Errorf("%v: taken for a version request", args)
		}
	}
}
