package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
)

func TestProdProject(t *testing.T) {
	for options, want := range map[string]string{
		"--default-database=acme-prod":                   "acme-prod",
		"--default-database acme-prod --vars=a=b":        "acme-prod",
		"--vars=env=prod":                                "",
		"--default-database=acme --database-suffix=prod": "",
	} {
		got, unknown := prodProject(options)
		if got != want || (want == "" && unknown == "") {
			t.Errorf("prodProject(%q) = %q, %q", options, got, unknown)
		}
	}
}

func TestDefaultProjectAndCore(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p, unknown := defaultProject(root, config.Loaded{}); p != "" || unknown == "" {
		t.Errorf("no settings: %q, %q", p, unknown)
	}
	write("workflow_settings.yaml", "defaultProject: acme-dev\ndefaultLocation: EU\n")
	if p, _ := defaultProject(root, config.Loaded{}); p != "acme-dev" {
		t.Errorf("defaultProject = %q", p)
	}
	write("workflow_settings.yaml", "defaultProject: acme\nprojectSuffix: dev\n")
	if p, unknown := defaultProject(root, config.Loaded{}); p != "" || unknown == "" {
		t.Errorf("with a suffix: %q, %q", p, unknown)
	}

	if needed, _ := dataformCore(root); needed {
		t.Error("no package.json needs core")
	}
	write("package.json", `{"dependencies":{"@dataform/core":"^3.0.50"}}`)
	if needed, v := dataformCore(root); !needed || v != "" {
		t.Errorf("not installed: %v %q", needed, v)
	}
	write("node_modules/@dataform/core/package.json", `{"version":"3.0.50"}`)
	if needed, v := dataformCore(root); !needed || v != "3.0.50" {
		t.Errorf("installed: %v %q", needed, v)
	}
}
