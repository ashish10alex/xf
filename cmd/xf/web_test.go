package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/config"
)

func TestWebAutoExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	userFile := filepath.Join(home, "cfg", "xf", "config.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	setUser := func(text string) {
		t.Helper()
		if err := os.WriteFile(userFile, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	after := func(remote bool, args ...string) time.Duration {
		t.Helper()
		fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
		sf := addSettingFlags(fs, config.Keys...)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		sf.remote = remote
		return webAutoExit(sf)()
	}
	setUser(`{}`)
	if d := after(false); d != 45*time.Second {
		t.Errorf("default = %v, want 45s", d)
	}
	if d := after(true); d != 0 {
		t.Errorf("default with --allow-host = %v, want off", d)
	}
	if d := after(true, "--web-auto-exit-seconds", "30"); d != 30*time.Second {
		t.Errorf("--allow-host and the flag = %v, want 30s", d)
	}
	setUser(`{"webAutoExitSeconds": 45}`)
	if d := after(true); d != 45*time.Second {
		t.Errorf("--allow-host and the user's file = %v, want 45s", d)
	}
	if d := after(false, "--web-auto-exit-seconds", "0"); d != 0 {
		t.Errorf("--web-auto-exit-seconds 0 = %v, want off", d)
	}
	// The settings page writes the file; the next countdown reads it.
	fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	get := webAutoExit(sf)
	setUser(`{"webAutoExitSeconds": 5}`)
	if d := get(); d != 5*time.Second {
		t.Errorf("after the page set 5 = %v", d)
	}
	setUser(`{"webAutoExitSeconds": 0}`)
	if d := get(); d != 0 {
		t.Errorf("after the page set 0 = %v", d)
	}
}

func TestWebAutoExitShownOffWithAllowHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	if k := keyOf(t, settingsView(sf, "", ""), config.KeyWebAutoExitSeconds); k.Value != "45" || !k.Live || !k.UserOnly || k.Kind != "number" {
		t.Errorf("webAutoExitSeconds = %+v", k)
	}
	sf.remote = true
	if k := keyOf(t, settingsView(sf, "", ""), config.KeyWebAutoExitSeconds); k.Value != "0" {
		t.Errorf("with --allow-host webAutoExitSeconds = %+v, want 0", k)
	}
}
