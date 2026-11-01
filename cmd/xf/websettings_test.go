package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/web"
)

func keyOf(t *testing.T, v web.SettingsView, key string) web.SettingKey {
	t.Helper()
	for _, k := range v.Keys {
		if k.Key == key {
			return k
		}
	}
	t.Fatalf("no %s in the view", key)
	return web.SettingKey{}
}

func TestWebSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	root := filepath.Join(home, "proj")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "cfg", "xf", "config.json"), `{"sourceAutosaveMs": 1500}`)
	write(filepath.Join(root, ".xf", "config.json"), `{"editor": "code", "webTheme": "dracula", "theme": "dark"}`)

	fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	if err := fs.Parse([]string{"--default-branch", "trunk"}); err != nil {
		t.Fatal(err)
	}
	v := settingsView(sf, root, "dataform")
	if len(v.Levels) != 2 || v.Levels[0].Error != "" || v.Levels[1].Error != "" {
		t.Fatalf("levels = %+v", v.Levels)
	}
	if k := keyOf(t, v, config.KeySourceAutosaveMs); k.User == nil || *k.User != "1500" || k.Project != nil || k.Value != "1500" || !k.Live || k.Kind != "number" {
		t.Errorf("sourceAutosaveMs = %+v", k)
	}
	if k := keyOf(t, v, config.KeyEditor); k.Project == nil || *k.Project != "code" || k.Value != "code" {
		t.Errorf("editor = %+v", k)
	}
	if k := keyOf(t, v, config.KeyWebTheme); k.Project == nil || *k.Project != "dracula" || k.Value != "dracula" || k.UserOnly || k.Kind != "choice" {
		t.Errorf("webTheme = %+v: a project file sets it", k)
	}
	if k := keyOf(t, v, config.KeyTheme); k.Project != nil || !k.UserOnly || k.Kind != "choice" {
		t.Errorf("theme = %+v: a project file doesn't set it", k)
	}
	if k := keyOf(t, v, config.KeyDefaultBranch); k.Flag == nil || *k.Flag != "trunk" || k.Value != "trunk" || !k.Live {
		t.Errorf("defaultBranch = %+v", k)
	}
	if k := keyOf(t, v, config.KeyRunMode); strings.Contains(k.Doc, "`") || strings.Contains(k.Doc, "(default:") {
		t.Errorf("runMode doc = %q", k.Doc)
	}
	for _, k := range v.Keys {
		if strings.HasPrefix(k.Key, "dbt") {
			t.Errorf("a Dataform project lists %s", k.Key)
		}
	}
	if !strings.Contains(strings.Join(v.Warnings, "\n"), "theme is ignored") {
		t.Errorf("warnings = %v", v.Warnings)
	}

	s := webSettings(sf)
	if _, err := s.Set(root, "project", config.KeySourceAutosaveMs, "soon", false); err == nil {
		t.Error("wrote a bad value")
	}
	if _, err := s.Set(root, "project", config.KeyTheme, "dark", false); err == nil {
		t.Error("wrote a user-only key to the project file")
	}
	file, err := s.Set(root, "project", config.KeySourceAutosaveMs, "2000", false)
	if err != nil || file != filepath.Join(root, ".xf", "config.json") {
		t.Fatalf("Set = %q, %v", file, err)
	}
	if k := keyOf(t, settingsView(sf, root, "dataform"), config.KeySourceAutosaveMs); k.Value != "2000" || k.From != filepath.Join(".xf", "config.json") {
		t.Errorf("after a project value: %+v", k)
	}
	if _, err := s.Set(root, "project", config.KeySourceAutosaveMs, "", true); err != nil {
		t.Fatal(err)
	}
	if k := keyOf(t, settingsView(sf, root, "dataform"), config.KeySourceAutosaveMs); k.Value != "1500" || k.Project != nil {
		t.Errorf("after unset: %+v", k)
	}

	// A project file that isn't JSON can't be edited from the page.
	write(filepath.Join(root, ".xf", "config.json"), `{"editor": `)
	if v := settingsView(sf, root, "dataform"); v.Levels[1].Error == "" {
		t.Error("a broken project file has no error")
	}
	// A dbt project lists dbt's keys, not Dataform's.
	for _, k := range settingsView(sf, "", "dbt").Keys {
		if k.Key == config.KeyRunMode {
			t.Error("a dbt project lists runMode")
		}
	}
}

// The vim keys are the Vim tab's: the user's, with their defaults in use,
// and no two the same.
func TestWebSettingsVimKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	if fs.Lookup("vim-next-hunk") != nil {
		t.Error("a vim key has a flag")
	}
	k := keyOf(t, settingsView(sf, root, "dataform"), config.KeyVimNextHunk)
	if k.Section != "Vim" || !k.UserOnly || !k.Live || k.Value != "]g" || k.From != "default" || strings.HasPrefix(k.Doc, "keys of") {
		t.Errorf("vimNextHunk = %+v", k)
	}
	s := webSettings(sf)
	if _, err := s.Set(root, "user", config.KeyVimNextHunk, "]e", false); err == nil || !strings.Contains(err.Error(), "vimNextError already has ]e") {
		t.Errorf("took another key's keys: %v", err)
	}
	if _, err := s.Set(root, "project", config.KeyVimNextHunk, "]c", false); err == nil {
		t.Error("wrote a vim key to the project file")
	}
	if _, err := s.Set(root, "user", config.KeyVimNextHunk, "]c", false); err != nil {
		t.Fatal(err)
	}
	if k := keyOf(t, settingsView(sf, root, "dataform"), config.KeyVimNextHunk); k.Value != "]c" || k.User == nil {
		t.Errorf("after a user value: %+v", k)
	}
}
