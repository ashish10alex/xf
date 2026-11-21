package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

func TestImpactHelp(t *testing.T) {
	fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
	_ = addSettingFlags(fs, config.KeyDataformBin, config.KeyDefaultBranch)
	_ = fs.Bool("pushed", false, "compare the branch pushed to origin (what an API run compiles) instead of the working tree")
	_ = fs.String("fail-on", "", "exit 1 when a change of this `severity` or worse is found: critical or warning")
	output := fs.String("output", "", "write the Markdown to `path` instead of stdout")
	fs.StringVar(output, "o", "", "")

	var b bytes.Buffer
	fs.SetOutput(&b)
	commandUsage(fs, "xf impact [flags]", "test summary")
	fs.Usage()
	help := b.String()

	if !strings.Contains(help, "--output path") {
		t.Errorf("help missing --output path, got:\n%s", help)
	}
	if !strings.Contains(help, "write the Markdown to path instead of stdout") {
		t.Errorf("help missing description for --output, got:\n%s", help)
	}
	if strings.Contains(help, "\n  --o ") || strings.Contains(help, "\n  --o\n") {
		t.Errorf("help should not display alias --o, got:\n%s", help)
	}
}

func TestImpactFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--output", "impact.md"}, "impact.md"},
		{[]string{"-output", "impact.md"}, "impact.md"},
		{[]string{"--output=impact.md"}, "impact.md"},
		{[]string{"-o", "impact.md"}, "impact.md"},
		{[]string{"-o=impact.md"}, "impact.md"},
		{[]string{}, ""},
	}
	for _, tc := range tests {
		fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
		_ = addSettingFlags(fs, config.KeyDataformBin, config.KeyDefaultBranch)
		_ = fs.Bool("pushed", false, "")
		_ = fs.String("fail-on", "", "")
		output := fs.String("output", "", "write the Markdown to `path` instead of stdout")
		fs.StringVar(output, "o", "", "")

		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("Parse(%v) failed: %v", tc.args, err)
		}
		if *output != tc.want {
			t.Errorf("Parse(%v) output = %q, want %q", tc.args, *output, tc.want)
		}
	}
}

func TestWriteReport(t *testing.T) {
	report := &impact.Report{
		Comparison: &impact.Comparison{
			HeadRef:   "feat",
			BaseRef:   "main",
			MergeBase: "abcdef123456",
		},
	}
	wantMd := impact.Markdown(report) + "\n"
	wantJSON, _ := impact.JSON(report)

	t.Run("text on stdout", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "", report, false, false, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got, want := buf.String(), impact.Text(report, false, false); got != want {
			t.Errorf("stdout got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("coloured text on stdout", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "", report, false, true, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if !strings.Contains(buf.String(), "\x1b[") {
			t.Errorf("no colour in %q", buf.String())
		}
	})

	t.Run("text on stdout and Markdown in the file, in new folders", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "sub", "dir", "impact.md")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, false, true, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got, want := buf.String(), impact.Text(report, true, false); got != want {
			t.Errorf("stdout got:\n%q\nwant:\n%q", got, want)
		}
		if data, err := os.ReadFile(outPath); err != nil || string(data) != wantMd {
			t.Errorf("file got %q (%v), want %q", data, err, wantMd)
		}
	})

	t.Run("JSON on stdout and in the file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "impact.json")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, true, true, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if data, err := os.ReadFile(outPath); err != nil || string(data) != string(wantJSON) || buf.String() != string(wantJSON) {
			t.Errorf("file got %q (%v), stdout %q; want %q in both", data, err, buf.String(), wantJSON)
		}
	})
}

func TestRunsToActionsCompilesPushedHeadSHA(t *testing.T) {
	// With HeadPushed, runsToActions compiles res.HeadSHA via optSource
	// (never the dirty working tree root) and maps prod targets back to g.
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "orders.sqlx"), []byte("SELECT 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "orders.sqlx")
	git("commit", "-q", "-m", "init")
	sha := git("rev-parse", "HEAD")

	// Dirty the working tree: the snapshot compiled from HeadSHA must not see it.
	if err := os.WriteFile(filepath.Join(repo, "orders.sqlx"), []byte("DIRTY"), 0o644); err != nil {
		t.Fatal(err)
	}

	devTarget := project.Target{Database: "dev", Schema: "marts", Name: "orders"}
	g := project.NewGraph([]*project.Action{{ID: devTarget.ID(), Target: devTarget, CanonicalTarget: devTarget, Kind: project.KindTable}})
	prodJSON := []byte(`{"tables":[{"type":"table","fileName":"orders.sqlx","target":{"database":"prod","schema":"marts","name":"orders"},"canonicalTarget":{"database":"dev","schema":"marts","name":"orders"},"query":"SELECT 1"}]}`)

	var gotKey, gotOptions string
	var gotArgs []string
	optSource := func(key, options string, args []string) *changes.Source {
		gotKey, gotOptions, gotArgs = key, options, args
		return &changes.Source{
			Root: repo,
			Key:  "test",
			Compile: func(_ context.Context, dir string) (*project.Graph, error) {
				b, err := os.ReadFile(filepath.Join(dir, "orders.sqlx"))
				if err != nil {
					return nil, err
				}
				if string(b) != "SELECT 1" {
					t.Errorf("compiled working tree instead of HeadSHA snapshot: %q", b)
				}
				return &project.Graph{JSON: prodJSON}, nil
			},
		}
	}
	res := &changes.Result{Head: changes.HeadPushed, HeadSHA: sha}
	m, err := runsToActions(context.Background(), "nonexistent-dataform-bin", repo, config.KeyProdCompilerOptions, "--database-suffix=prod", g, res, optSource, func(impact.Progress) {})
	if err != nil {
		t.Fatalf("runsToActions: %v", err)
	}
	if gotKey != config.KeyProdCompilerOptions || gotOptions != "--database-suffix=prod" || len(gotArgs) != 1 {
		t.Errorf("optSource args = %q %q %v", gotKey, gotOptions, gotArgs)
	}
	if got := m["prod.marts.orders"]; got != "dev.marts.orders" {
		t.Errorf("DevToProd map = %v, want prod.marts.orders -> dev.marts.orders", m)
	}

	// HeadPushed with an empty HeadSHA must fail rather than silently compiling the working tree.
	if _, err := runsToActions(context.Background(), "nonexistent-dataform-bin", repo, config.KeyProdCompilerOptions, "--database-suffix=prod", g, &changes.Result{Head: changes.HeadPushed}, optSource, func(impact.Progress) {}); err == nil {
		t.Error("expected error when HeadPushed has empty HeadSHA")
	}
}

func TestImpactRefusesBadFlagsBeforeLookingForAProject(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--fail-on", "minor"},
		{"--web", "--fail-on", "unknown"},
		{"--web", "--fail-on-unchecked"},
		{"--web", "--json"},
		{"--color", "sometimes"},
		{"--lineage-projects", "bad/parent?x"},
		{"stray"},
	} {
		if code := runImpact(args); code != impactFailed {
			t.Errorf("xf impact %v exited %d, want %d", args, code, impactFailed)
		}
	}
	if code := runImpact([]string{"--help"}); code != impactOK {
		t.Errorf("xf impact --help exited %d", code)
	}
}
