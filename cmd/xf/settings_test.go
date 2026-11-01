package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/testutil"
)

func TestHelpDocumentsEveryFlagAndKey(t *testing.T) {
	var b bytes.Buffer
	mainHelp(&b, tuiFlags())
	help := b.String()
	for _, k := range config.Keys {
		for _, want := range []string{k, "--" + config.Flags[k]} {
			if !strings.Contains(help, want) {
				t.Errorf("help doesn't mention %s", want)
			}
		}
	}
	for _, want := range []string{"--config path", "--nvim", "xf init", "xf config", ".xf/config.json", "multi-region", "run changed"} {
		if !strings.Contains(help, want) {
			t.Errorf("help doesn't mention %q", want)
		}
	}
}

func TestHelpRequested(t *testing.T) {
	for args, want := range map[string]bool{"help": true, "--help": true, "-h": true, "": false, "config": false, "--nvim": false} {
		if got := helpRequested(strings.Fields(args)); got != want {
			t.Errorf("helpRequested(%q) = %v", args, got)
		}
	}
}

func TestFlagsWinOverConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectFile), []byte(`{"runMode": "api", "dataformRepo": "shop"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _, sf, _ := tuiFlagSet()
	if help, err := parseArgs(fs, []string{"--run-mode", "cli", "--nvim", "a.sqlx"}); help || err != nil {
		t.Fatal(help, err)
	}
	l, err := sf.load(root)
	if err != nil {
		t.Fatal(err)
	}
	if l.RunMode != "cli" || l.Sources[config.KeyRunMode] != "--run-mode" || l.DataformRepo != "shop" {
		t.Errorf("settings = %+v, sources %v", l.Settings, l.Sources)
	}
	if got := strings.Join(sf.given("nvim"), " "); got != "--run-mode cli" {
		t.Errorf("given = %q", got)
	}

	fs, _, sf, _ = tuiFlagSet()
	_, _ = parseArgs(fs, []string{"--run-mode", "remote"})
	if _, err := sf.load(root); err == nil || !strings.Contains(err.Error(), "--run-mode must be cli or api") {
		t.Errorf("bad --run-mode: err = %v", err)
	}
}

func TestChooseLocation(t *testing.T) {
	var out bytes.Buffer
	if got, err := chooseLocation([]string{"europe-west2"}, "shop", "proj", strings.NewReader(""), &out); err != nil || got != "europe-west2" {
		t.Errorf("one found: %q, %v", got, err)
	}
	if got, err := chooseLocation(nil, "shop", "proj", strings.NewReader("us-east1\n"), &out); err != nil || got != "us-east1" {
		t.Errorf("none found, typed: %q, %v", got, err)
	}
	if got, err := chooseLocation([]string{"a-1", "b-2"}, "shop", "proj", strings.NewReader(" b-2 "), &out); err != nil || got != "b-2" {
		t.Errorf("two found, typed without newline: %q, %v", got, err)
	}
	if _, err := chooseLocation(nil, "shop", "proj", strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "--dataform-location") {
		t.Errorf("nothing typed: err = %v", err)
	}
}

func TestSettingsValue(t *testing.T) {
	root := t.TempDir()
	yaml := "defaultProject: shop-prod\ndefaultLocation: \"EU\" # multi-region\ndefaultDataset: analytics\n"
	if err := os.WriteFile(filepath.Join(root, "workflow_settings.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := settingsValue(root, "defaultLocation"); got != "EU" {
		t.Errorf("defaultLocation = %q", got)
	}
	if got := settingsValue(root, "defaultAssertionDataset"); got != "" {
		t.Errorf("missing key = %q", got)
	}
}

// The xf serve a client starts must compile like the client and listen
// where it dials, even when a config file names another dbt target.
func TestServeMatchesClientDbtTarget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectFile), []byte(`{"dbtTarget": "dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := testutil.FakeCLI(t, "dbt")
	open := func(fs *flag.FlagSet, sf *settingFlags, args []string) backend.Backend {
		t.Helper()
		if help, err := parseArgs(fs, args); help || err != nil {
			t.Fatal(help, err)
		}
		l, err := sf.load(root)
		if err != nil {
			t.Fatal(err)
		}
		b, err := openBackend(backend.DBT, root, l)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, args := range [][]string{nil, {"--dbt-target", "prod"}, {"--dbt-target", ""}} {
		fs, _, sf, _ := tuiFlagSet()
		client := open(fs, sf, append([]string{"--dbt-bin", bin}, args...))
		sfs, _, ssf, _ := serveFlagSet()
		server := open(sfs, ssf, serveArgs(root, client)[1:])
		if got, want := backend.EngineName(server), backend.EngineName(client); got != want {
			t.Errorf("%v: serve is %s, client dials %s", args, got, want)
		}
	}
}

// The xf serve a client starts compiles a Dataform project to the names
// the client does, and listens where it dials, even when a config file
// names others.
func TestServeMatchesClientDataformNames(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectFile), []byte(`{"schemaSuffix": "ashish"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := testutil.FakeCLI(t, "dataform")
	open := func(fs *flag.FlagSet, sf *settingFlags, args []string) dataform.Backend {
		t.Helper()
		if help, err := parseArgs(fs, args); help || err != nil {
			t.Fatal(help, err)
		}
		l, err := sf.load(root)
		if err != nil {
			t.Fatal(err)
		}
		b, err := openBackend(backend.Dataform, root, l)
		if err != nil {
			t.Fatal(err)
		}
		return b.(dataform.Backend)
	}
	for _, tt := range []struct {
		args []string
		want dataform.Names
	}{
		{nil, dataform.Names{SchemaSuffix: "ashish"}},
		{[]string{"--schema-suffix", "ci", "--table-prefix", "tmp"}, dataform.Names{SchemaSuffix: "ci", TablePrefix: "tmp"}},
		{[]string{"--schema-suffix", ""}, dataform.Names{}},
	} {
		fs, _, sf, _ := tuiFlagSet()
		client := open(fs, sf, append([]string{"--dataform-bin", bin}, tt.args...))
		if client.Names != tt.want {
			t.Errorf("%v: client names %+v, want %+v", tt.args, client.Names, tt.want)
		}
		sfs, _, ssf, _ := serveFlagSet()
		server := open(sfs, ssf, serveArgs(root, client)[1:])
		if server.Names != client.Names || backend.EngineName(server) != backend.EngineName(client) {
			t.Errorf("%v: serve is %s %+v, client dials %s %+v", tt.args, backend.EngineName(server), server.Names, backend.EngineName(client), client.Names)
		}
	}
}

// xf web opens a Dataform project whose CLI isn't on PATH, which the TUI
// refuses, and the xf serve it starts listens where it dials, also once
// the CLI is installed.
func TestWebOpensDataformWithoutTheCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(dataform.BinEnv, "")
	t.Setenv("PATH", t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "workflow_settings.yaml"), []byte("defaultProject: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tfs, _, tsf, _ := tuiFlagSet()
	if help, err := parseArgs(tfs, nil); help || err != nil {
		t.Fatal(help, err)
	}
	l, err := tsf.load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openBackend(backend.Dataform, root, l); !errors.Is(err, dataform.ErrNotOnPath) {
		t.Fatalf("openBackend: %v, want ErrNotOnPath", err)
	}
	client, err := openWebBackend(backend.Dataform, root, l)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := client.(dataform.Backend); !ok || d.Bin != "" {
		t.Fatalf("openWebBackend = %#v, want a dataform.Backend with no Bin", client)
	}
	t.Setenv("PATH", filepath.Dir(testutil.FakeCLI(t, "dataform"))) // installed since
	fs, _, sf, onPath := serveFlagSet()
	if help, err := parseArgs(fs, serveArgs(root, client)[1:]); help || err != nil {
		t.Fatal(help, err)
	}
	sl, err := sf.load(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := serveBackend(backend.Dataform, root, sl, *onPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := backend.EngineName(server), backend.EngineName(client); got != want {
		t.Errorf("serve is %s, client dials %s", got, want)
	}
}
