package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/project"
)

func TestNormalizeAndInferScratchWarehouse(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"bigquery", project.BigQuery, false},
		{"BQ", project.BigQuery, false},
		{"databricks", project.Databricks, false},
		{"dbx", project.Databricks, false},
		{"redshift", "", true},
	} {
		got, err := normalizeScratchWarehouse(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("normalizeScratchWarehouse(%q) = (%q, %v), want (%q, err=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}

	fs := flag.NewFlagSet("xf scratch", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	if got := inferScratchWarehouse(sf, "", ""); got != "" {
		t.Errorf("bare infer = %q, want empty", got)
	}
	if got := inferScratchWarehouse(sf, "my-proj", ""); got != project.BigQuery {
		t.Errorf("with --project infer = %q, want bigquery", got)
	}
	if got := inferScratchWarehouse(sf, "", "EU"); got != project.BigQuery {
		t.Errorf("with --location infer = %q, want bigquery", got)
	}
	if err := fs.Parse([]string{"--databricks-profile", "dev"}); err != nil {
		t.Fatal(err)
	}
	if got := inferScratchWarehouse(sf, "", ""); got != project.Databricks {
		t.Errorf("with --databricks-profile infer = %q, want databricks", got)
	}
}

func TestStandaloneDatabricksProfilesAndPersistence(t *testing.T) {
	tmp := t.TempDir()
	dbxDir := filepath.Join(tmp, "tmp", "databricks")
	cfgFile := filepath.Join(tmp, "databrickscfg")
	noEnv := func(string) string { return "" }

	// Single profile with no DEFAULT auto-resolves unambiguously.
	if err := os.WriteFile(cfgFile, []byte("[DEFAULT]\n\n[solo]\nhost = dbc-solo.cloud.databricks.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sd := &standaloneDatabricks{dir: dbxDir, configFile: cfgFile, getenv: noEnv}
	sd.state = sd.loadState()
	sd.rebuildLocked()
	if sd.profile != "solo" || sd.currentClient().Host() != "https://dbc-solo.cloud.databricks.com" {
		t.Fatalf("single profile resolve = (%q, %q)", sd.profile, sd.currentClient().Host())
	}

	// Multiple profiles with no DEFAULT leave host empty until one is picked.
	if err := os.WriteFile(cfgFile, []byte("[dev]\nhost = dbc-dev.cloud.databricks.com\n\n[prod]\nhost = dbc-prod.cloud.databricks.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sd2 := &standaloneDatabricks{dir: dbxDir, configFile: cfgFile, getenv: noEnv}
	sd2.state = sd2.loadState()
	sd2.rebuildLocked()
	if sd2.currentClient().Host() != "" {
		t.Fatalf("ambiguous profiles should not auto-pick, got host %q", sd2.currentClient().Host())
	}

	// Picking profile and SQL warehouse persists in connection.json per profile.
	w := sd2.webDatabricks()
	if _, err := w.ChooseProfile("dev"); err != nil {
		t.Fatal(err)
	}
	if w.Host() != "https://dbc-dev.cloud.databricks.com" {
		t.Fatalf("host after ChooseProfile(dev) = %q", w.Host())
	}
	file, err := w.Choose("wh-dev")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != standaloneDbxFile {
		t.Fatalf("saved to %q, want %s", file, standaloneDbxFile)
	}
	if w.WarehouseID() != "wh-dev" {
		t.Fatalf("warehouse = %q, want wh-dev", w.WarehouseID())
	}

	// Switching to prod clears warehouse until prod's is picked; switching back
	// to dev restores dev's warehouse.
	if _, err := w.ChooseProfile("prod"); err != nil {
		t.Fatal(err)
	}
	if w.WarehouseID() != "" {
		t.Fatalf("prod warehouse = %q, want empty", w.WarehouseID())
	}
	if _, err := w.Choose("wh-prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ChooseProfile("dev"); err != nil {
		t.Fatal(err)
	}
	if w.WarehouseID() != "wh-dev" {
		t.Fatalf("restored dev warehouse = %q, want wh-dev", w.WarehouseID())
	}

	// Fresh instance reloads persisted profile and warehouse from disk.
	sd3 := &standaloneDatabricks{dir: dbxDir, configFile: cfgFile, getenv: noEnv}
	sd3.state = sd3.loadState()
	sd3.rebuildLocked()
	if sd3.profile != "dev" || sd3.currentClient().Warehouse() != "wh-dev" {
		t.Fatalf("reloaded = (%q, %q), want (dev, wh-dev)", sd3.profile, sd3.currentClient().Warehouse())
	}

	// cfgWarehouse from config.json applies only when no profile override was set.
	sdCfg := &standaloneDatabricks{dir: filepath.Join(tmp, "tmp", "dbx-cfg"), configFile: cfgFile, getenv: noEnv, cfgProfile: "dev", cfgWarehouse: "wh-cfg"}
	sdCfg.state = sdCfg.loadState()
	sdCfg.rebuildLocked()
	if sdCfg.currentClient().Warehouse() != "wh-cfg" {
		t.Fatalf("cfgWarehouse fallback = %q, want wh-cfg", sdCfg.currentClient().Warehouse())
	}
	sdFlag := &standaloneDatabricks{dir: filepath.Join(tmp, "tmp", "dbx-flag"), configFile: cfgFile, getenv: noEnv, flagProfile: "prod", cfgProfile: "dev", cfgWarehouse: "wh-cfg"}
	sdFlag.state = sdFlag.loadState()
	sdFlag.rebuildLocked()
	if sdFlag.currentClient().Warehouse() != "" {
		t.Fatalf("flagProfile should not inherit cfgWarehouse, got %q", sdFlag.currentClient().Warehouse())
	}
}

func TestStandaloneScratchLoadsUserEditorSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("DATABRICKS_HOST", "")
	t.Setenv("DATABRICKS_TOKEN", "")
	t.Setenv("DATABRICKS_CONFIG_FILE", filepath.Join(home, "none"))
	cfgPath := filepath.Join(home, "cfg", "xf", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"vimMode": true, "wrapLines": true, "vimNextHunk": "]c", "sqlfluffBin": "/custom/sqlfluff"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("xf scratch", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	for _, wh := range []string{project.BigQuery, project.Databricks} {
		p, err := standaloneScratch(sf, wh, "", "", true)
		if err != nil {
			t.Fatalf("standaloneScratch(%s): %v", wh, err)
		}
		if !p.VimMode || !p.WrapLines || p.SqlfluffBin != "/custom/sqlfluff" || p.VimKeys[config.KeyVimNextHunk] != "]c" {
			t.Errorf("standaloneScratch(%s): VimMode=%v WrapLines=%v Sqlfluff=%q VimKeys=%v", wh, p.VimMode, p.WrapLines, p.SqlfluffBin, p.VimKeys)
		}
	}
}
