package dataform

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/project"
)

func TestNamesArgs(t *testing.T) {
	if got := (Names{}).Args(); got != nil {
		t.Errorf("no names: %q", got)
	}
	n := Names{TablePrefix: "tmp", SchemaSuffix: "ashish", DatabaseSuffix: "dev"}
	want := []string{"--database-suffix=dev", "--schema-suffix=ashish", "--table-prefix=tmp"}
	if got := n.Args(); !reflect.DeepEqual(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

// Names change the engine a backend's clients meet on, but no names keep
// the one a project had before there were any.
func TestVariantWithNames(t *testing.T) {
	plain := Backend{Bin: "/bin/dataform"}
	if plain.Variant() != (Backend{Bin: "/bin/dataform", Names: Names{}}).Variant() {
		t.Error("empty names changed the variant")
	}
	a := Backend{Bin: "/bin/dataform", Names: Names{SchemaSuffix: "a"}}
	b := Backend{Bin: "/bin/dataform", Names: Names{TablePrefix: "a"}}
	if a.Variant() == plain.Variant() || a.Variant() == b.Variant() {
		t.Errorf("variants plain %s, schema a %s, prefix a %s", plain.Variant(), a.Variant(), b.Variant())
	}
}

func TestRunCommandHasTheNames(t *testing.T) {
	b := Backend{Names: Names{SchemaSuffix: "ashish"}}
	got := b.RunCommand(t.TempDir(), project.RunOptions{Actions: []project.ActionID{"p.s.a"}})
	if !strings.HasSuffix(got, "run --actions p.s.a "+cmdrun.ShellJoin([]string{"--schema-suffix=ashish"})) {
		t.Errorf("RunCommand = %q", got)
	}
}

func TestSettingsKey(t *testing.T) {
	for key, want := range map[string]string{"tablePrefix": "namePrefix", "schemaSuffix": "datasetSuffix", "databaseSuffix": "projectSuffix", "editor": ""} {
		if got := SettingsKey(key); got != want {
			t.Errorf("SettingsKey(%s) = %q, want %q", key, got, want)
		}
	}
}
