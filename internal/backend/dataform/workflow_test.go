package dataform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadWorkflowNames(t *testing.T) {
	root := t.TempDir()
	yaml := "defaultProject: acme-dev # dev\n" +
		"defaultLocation: EU\n" +
		"defaultDataset: \"analytics\"\n" +
		"datasetSuffix: 'qa'\n" +
		"vars:\n  defaultProject: nested\n"
	if err := os.WriteFile(filepath.Join(root, SettingsFile), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWorkflowNames(root)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkflowNames{Project: "acme-dev", Dataset: "analytics", SchemaSuffix: "qa"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
