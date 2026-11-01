package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/cost"
)

func TestCostRefusesBadFlagsBeforeLookingForAProject(t *testing.T) {
	t.Chdir(t.TempDir()) // no project here: getting that far would also exit 2, with another message
	for _, args := range [][]string{
		{"--fail-over", "lots"},
		{"--fail-over", "0"},
		{"--usd-per-tib", "0"},
		{"--usd-per-tib", "cheap"},
		{"--color", "sometimes"},
		{"--no-such-flag"},
		{"stray"},
	} {
		if code := runCost(args); code != costFailed {
			t.Errorf("xf cost %v exited %d, want %d", args, code, costFailed)
		}
	}
	if code := runCost([]string{"--help"}); code != costOK {
		t.Errorf("xf cost --help exited %d", code)
	}
}

func TestUseColor(t *testing.T) {
	// A file is no terminal: auto leaves it plain, so a redirect or a CI
	// log gets no escapes.
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	for _, tc := range []struct {
		when string
		vars map[string]string
		want bool
	}{
		{"auto", nil, false},
		{"always", map[string]string{"NO_COLOR": "1"}, true}, // asked for by name
		{"never", nil, false},
	} {
		if got, err := useColor(tc.when, f, env(tc.vars)); err != nil || got != tc.want {
			t.Errorf("useColor(%q, %v) = %v, %v; want %v", tc.when, tc.vars, got, err, tc.want)
		}
	}
	if _, err := useColor("sometimes", f, env(nil)); err == nil {
		t.Error("--color sometimes was taken")
	}
}

func TestWriteCostReport(t *testing.T) {
	report := &cost.Report{Actions: []cost.Action{{Name: "p.d.orders", Type: "table", Estimate: &cost.Estimate{Bytes: 1 << 30}}}}
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		asJSON bool
		want   func() []byte
	}{
		{"sub/cost.md", false, func() []byte { return []byte(cost.Markdown(report, cost.Options{})) }},
		{"sub/cost.json", true, func() []byte { data, _ := cost.JSON(report, cost.Options{}); return data }},
	} {
		path := filepath.Join(dir, tc.name)
		if err := writeCostReport(path, report, cost.Options{}, tc.asJSON); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != string(tc.want()) {
			t.Errorf("%s (json %v) got %q, %v; want %q", tc.name, tc.asJSON, got, err, tc.want())
		}
	}
}
