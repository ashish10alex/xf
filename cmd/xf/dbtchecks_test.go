package main

import (
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
)

func TestUnsetDbtProdTargetOnlyWhenNothingPicksATarget(t *testing.T) {
	set := func(keys ...string) config.Loaded {
		l := config.Loaded{Sources: map[string]string{}}
		for _, k := range keys {
			l.Sources[k] = "project"
		}
		return l
	}
	none := func(string) string { return "" }
	env := func(k string) string {
		if k == "DBT_TARGET" {
			return "ci"
		}
		return ""
	}
	cases := []struct {
		name     string
		settings config.Loaded
		getenv   func(string) string
		want     string
	}{
		{"nothing", set(), none, config.KeyDbtProdTarget},
		{"dbtProdTarget", set(config.KeyDbtProdTarget), none, ""},
		{"dbtTarget", set(config.KeyDbtTarget), none, ""},
		{"$DBT_TARGET", set(), env, ""},
	}
	for _, c := range cases {
		if got := unsetDbtProdTarget(c.settings, c.getenv); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDbtTargetsCompileOtherTargetsApart(t *testing.T) {
	targets := newDbtTargets(dbtBackendForTest("dev"), t.TempDir(), nil)
	if targets.other("") || targets.other("dev") || !targets.other("prod") {
		t.Error("only another target than xf's needs a compile of its own")
	}
	if targets.backend("dev") != targets.b || targets.backend("") != targets.b {
		t.Error("xf's own target should use xf's backend")
	}
	prod := targets.backend("prod")
	if prod.Target != "prod" || prod.Bin != targets.b.Bin || prod.Variant() == targets.b.Variant() || targets.backend("prod") != prod {
		t.Errorf("prod backend %+v", prod)
	}
}

func dbtBackendForTest(target string) *dbt.Backend {
	return dbt.New("/usr/bin/true", target, func(root string) string { return root }, false)
}
