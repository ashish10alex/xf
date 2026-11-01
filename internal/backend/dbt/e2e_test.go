package dbt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// e2eBins are the dbt binaries to compile the examples with: dbt on PATH,
// and XF_DBT_CORE_BIN (a dbt-core 1.x) when set. It runs only with
// XF_E2E=1.
func e2eBins(t *testing.T) map[string]string {
	t.Helper()
	if os.Getenv("XF_E2E") == "" {
		t.Skip("set XF_E2E=1 to run against the real dbt CLI")
	}
	bins := map[string]string{}
	if bin, err := exec.LookPath("dbt"); err == nil {
		bins["path"] = bin
	}
	if bin := os.Getenv("XF_DBT_CORE_BIN"); bin != "" {
		bins["core"] = bin
	}
	if len(bins) == 0 {
		t.Skip("no dbt on PATH and no XF_DBT_CORE_BIN")
	}
	return bins
}

func example(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestE2ECompileExample(t *testing.T) {
	for label, bin := range e2eBins(t) {
		t.Run(label, func(t *testing.T) {
			b := New(bin, "", func(string) string { return t.TempDir() }, false)
			g, err := b.Compile(context.Background(), example(t, "dbt"))
			if err != nil {
				t.Fatal(err)
			}
			if g.HasErrors() {
				t.Fatalf("errors: %+v", g.Errors)
			}
			_, version := b.version(context.Background())
			t.Logf("%s: %d actions", version, len(g.Actions))
			fct := g.Actions["alex-personal-dev-01.xf_example.fct_orders"]
			if fct == nil || fct.Kind != project.KindIncremental {
				t.Fatalf("fct_orders = %+v", fct)
			}
			if g.Notice != "" || strings.Contains(fct.Query, "{{") || !strings.Contains(fct.Query, "`alex-personal-dev-01`.`xf_example`.`stg_orders`") {
				t.Errorf("want compiled SQL and no notice: notice %q, query %q", g.Notice, fct.Query)
			}
		})
	}
}

// A target names the schemas, so the actions' IDs and SQL follow it.
func TestE2ECompileTarget(t *testing.T) {
	for label, bin := range e2eBins(t) {
		t.Run(label, func(t *testing.T) {
			b := New(bin, "ci", func(string) string { return t.TempDir() }, false)
			g, err := b.Compile(context.Background(), example(t, "dbt"))
			if err != nil {
				t.Fatal(err)
			}
			if g.HasErrors() {
				t.Fatalf("errors: %+v", g.Errors)
			}
			fct := g.Actions["alex-personal-dev-01.xf_example_ci.fct_orders"]
			if fct == nil {
				t.Fatalf("no fct_orders in xf_example_ci among %d actions", len(g.Actions))
			}
			if !strings.Contains(fct.Query, "`alex-personal-dev-01`.`xf_example_ci`.`stg_orders`") {
				t.Errorf("query doesn't read the ci schema: %q", fct.Query)
			}
		})
	}
}

func TestE2ECompileWithHooks(t *testing.T) {
	for label, bin := range e2eBins(t) {
		t.Run(label, func(t *testing.T) {
			b := New(bin, "", func(string) string { return t.TempDir() }, false)
			g, err := b.Compile(context.Background(), example(t, "dbt-hooks"))
			if err != nil {
				t.Fatal(err)
			}
			if g.HasErrors() {
				t.Fatalf("errors: %+v", g.Errors)
			}
			amounts := g.Actions["alex-personal-dev-01.xf_example.order_amounts"]
			if amounts == nil {
				t.Fatal("no order_amounts")
			}
			major, _ := b.version(context.Background())
			parsedOnly := strings.Contains(amounts.Query, "{{")
			if major == 1 && (parsedOnly || g.Notice != "") {
				t.Errorf("dbt-core compiles without running hooks: notice %q, query %q", g.Notice, amounts.Query)
			}
			if major != 1 && (!parsedOnly || !strings.Contains(g.Notice, "has 2 (in dbt_project.yml)")) {
				t.Errorf("dbt v2 should only parse: notice %q, query %q", g.Notice, amounts.Query)
			}
		})
	}
}

func TestE2ECompileBroken(t *testing.T) {
	for label, bin := range e2eBins(t) {
		t.Run(label, func(t *testing.T) {
			b := New(bin, "", func(string) string { return t.TempDir() }, false)
			g, err := b.Compile(context.Background(), example(t, "dbt-broken"))
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Errors) == 0 {
				t.Fatal("no errors")
			}
			e := g.Errors[0]
			if e.FileName != "models/order_totals.sql" || !strings.Contains(e.Message, "order_lines") {
				t.Errorf("error = %+v", e)
			}
		})
	}
}

// Run Changed on a feature branch of the example: dbt's state:modified
// against a parse of the merge-base, with xf's reasons.
func TestE2EChanged(t *testing.T) {
	for label, bin := range e2eBins(t) {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "dbt")
			git := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			if err := os.CopyFS(root, os.DirFS(example(t, "dbt"))); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{"target", "logs"} {
				_ = os.RemoveAll(filepath.Join(root, d))
			}
			git("init", "-q", "-b", "main")
			git("-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false", "add", ".")
			git("-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")
			git("checkout", "-q", "-b", "feat")
			// Check the files out again, as a clone would: with
			// core.autocrlf (Windows' default) that gives them CRLF, as the
			// base snapshot (git archive) has, rather than the copies' LF.
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			git("checkout", "--", ".")

			edit := func(file string, f func(string) string) {
				t.Helper()
				p := filepath.Join(root, file)
				data, err := os.ReadFile(p)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				// Edit as an editor would, keeping the file's line endings.
				text := string(data)
				crlf := strings.Contains(text, "\r\n")
				text = f(strings.ReplaceAll(text, "\r\n", "\n"))
				if crlf {
					text = strings.ReplaceAll(text, "\n", "\r\n")
				}
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			edit("models/marts/fct_orders.sql", func(s string) string { return s + "\n-- edited\n" })
			edit("macros/cents_to_dollars.sql", func(s string) string { return strings.Replace(s, ", 2)", ", 3)", 1) })
			edit("models/marts/new_model.sql", func(string) string { return "select 1 as x\n" })
			edit("models/intermediate/int_customer_countries.sql", func(s string) string { return s + "\n-- edited\n" })
			edit("dbt_project.yml", func(s string) string {
				return strings.Replace(s, "+schema: reporting", "+schema: reporting\n      +labels: {team: finance}", 1)
			})
			edit("models/reporting/_exposures.yml", func(s string) string { return strings.Replace(s, "      - ref('daily_revenue')\n", "", 1) })
			if err := os.Remove(filepath.Join(root, "models/marts/daily_revenue.sql")); err != nil {
				t.Fatal(err)
			}

			b := New(bin, "", func(string) string { return filepath.Join(dir, "artifacts") }, false)
			s := &changes.Source{
				Root: root, Key: "e2e", Mirror: Mirror,
				Base: func(ctx context.Context, snap string) ([]byte, error) { return b.Base(ctx, root, snap) },
				Diff: func(ctx context.Context, base, _ []byte) (changes.Diff, error) { return b.Changed(ctx, root, base) },
			}
			res, err := s.Changes(context.Background(), changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree, Compile: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range res.Changed {
				got = append(got, c.FileName+" "+c.Type+" "+c.ReasonsText())
			}
			want := []string{
				// It inlines int_customer_countries, an ephemeral model.
				"models/marts/customer_segments_v2.sql table upstream",
				"models/marts/fct_orders.sql incremental sql",
				"models/marts/new_model.sql table new",
				"models/reporting/revenue_report.sql table config",
				"models/staging/stg_payments.sql view upstream",
			}
			if !slices.Equal(got, want) {
				t.Errorf("changed:\n got %q\nwant %q", got, want)
			}
			if len(res.Deleted) != 1 || res.Deleted[0].FileName != "models/marts/daily_revenue.sql" {
				t.Errorf("deleted = %+v", res.Deleted)
			}
			if _, err := os.Stat(filepath.Join(root, "target")); err == nil {
				t.Error("Run Changed wrote the project's target/")
			}
		})
	}
}
