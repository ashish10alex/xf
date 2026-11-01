package backend

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
)

func TestDetect(t *testing.T) {
	dir := func(files ...string) string {
		d := t.TempDir()
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(d, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	tests := []struct {
		name   string
		dir    string
		want   string
		got    string
		errHas string
		noProj bool
	}{
		{name: "dataform v3", dir: dir("workflow_settings.yaml"), got: Dataform},
		{name: "dataform v2", dir: dir("dataform.json"), got: Dataform},
		{name: "dbt", dir: dir("dbt_project.yml"), got: DBT},
		{name: "both", dir: dir("workflow_settings.yaml", "dbt_project.yml"), errHas: "pass --backend dataform or --backend dbt"},
		{name: "both, dbt asked for", dir: dir("workflow_settings.yaml", "dbt_project.yml"), want: DBT, got: DBT},
		{name: "asked for the other tool", dir: dir("dbt_project.yml"), want: Dataform, errHas: "has no workflow_settings.yaml or dataform.json", noProj: true},
		{name: "unknown backend", dir: dir("dbt_project.yml"), want: "sqlmesh", errHas: `unknown backend "sqlmesh"`},
		{name: "neither", dir: dir("README.md"), errHas: "pass --dir", noProj: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect(tt.dir, tt.want)
			if tt.errHas == "" {
				if err != nil || got != tt.got {
					t.Fatalf("Detect = %q, %v; want %q", got, err, tt.got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errHas) {
				t.Fatalf("Detect = %q, %v; want an error containing %q", got, err, tt.errHas)
			}
			if errors.Is(err, ErrNoProject) != tt.noProj {
				t.Errorf("errors.Is(err, ErrNoProject) = %v, want %v", !tt.noProj, tt.noProj)
			}
		})
	}
}

func TestDetectIgnoresParents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dbt_project.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "models")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(sub, ""); !errors.Is(err, ErrNoProject) {
		t.Fatalf("Detect(models/) = %v, want ErrNoProject", err)
	}
}

func TestEngineName(t *testing.T) {
	df := EngineName(dataform.Backend{Bin: "dataform"})
	if !strings.HasPrefix(df, "dataform-") || df == EngineName(dataform.Backend{Bin: "/opt/dataform"}) {
		t.Errorf("dataform engines don't differ by binary: %q", df)
	}
	dev := EngineName(dbt.New("dbt", "dev", nil, false))
	if !strings.HasPrefix(dev, "dbt-") || dev == EngineName(dbt.New("dbt", "prod", nil, false)) || dev == EngineName(dbt.New("dbt", "dev", nil, true)) {
		t.Errorf("dbt engines don't differ by target and hooks: %q", dev)
	}
}
