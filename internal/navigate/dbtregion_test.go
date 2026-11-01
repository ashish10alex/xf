package navigate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func TestDbtRegion(t *testing.T) {
	src := "{{ config(materialized='table') }}\n\nselect a, {{- x -}} b\n{# a {{ comment }} #}\nfrom t {% raw %}{{ kept }}{% endraw %} where 1"
	var got []string
	for _, p := range DbtRegion([]byte(src)).Pieces {
		if src[p.Start:p.Start+len(p.Text)] != p.Text {
			t.Errorf("piece %q isn't at %d", p.Text, p.Start)
		}
		got = append(got, p.Text)
	}
	want := []string{"select a,", "b", "from t", "{{ kept }}", "where 1"}
	if !slices.Equal(got, want) {
		t.Errorf("pieces %q, want %q", got, want)
	}
	// A piece's End is the tag after it, past the trimmed whitespace.
	if p := DbtRegion([]byte(src)).Pieces[0]; src[p.End:p.End+3] != "{{-" {
		t.Errorf("first piece ends at %q", src[p.End:])
	}
}

func TestAlignSkipping(t *testing.T) {
	src := "select\n  o.id,\n  sum(p.amount) as amount\nfrom {{ ref('o') }} as o\n{% if is_incremental() %}\nwhere o.day > (select max(day) from {{ this }})\n{% endif %}\ngroup by 1"
	compiled := "with __dbt__cte__e as (\nselect 1\n)\nselect\n  o.id,\n  sum(p.amount) as amount\nfrom `p`.`d`.`o` as o\n\ngroup by 1"
	m := DbtRegion([]byte(src)).AlignSkipping(compiled)
	var placed []string
	for _, s := range m.spans {
		placed = append(placed, s.piece.Text)
	}
	// The if block's pieces are dropped, the ")" after it too: there is no
	// room for it before "group by".
	if want := []string{"select\n  o.id,\n  sum(p.amount) as amount\nfrom", "as o", "group by 1"}; !slices.Equal(placed, want) {
		t.Fatalf("placed %q, want %q", placed, want)
	}
	at := func(compiledText string, nth int) int {
		i := -1
		for range nth {
			i += 1 + strings.Index(compiled[i+1:], compiledText)
		}
		return i
	}
	for _, c := range []struct {
		compiled string
		nth      int
		source   string // what the source has at the offset it maps to
	}{
		{"amount", 1, "amount) as amount"},
		{"group", 1, "group by 1"},
		// In a ref's value: the tag.
		{"`p`.`d`", 1, "{{ ref('o') }}"},
		// A bare select in an ephemeral model's CTE goes on the first piece.
		{"select 1", 1, "select\n  o.id"},
	} {
		off, ok := m.Source(at(c.compiled, c.nth))
		if !ok || !strings.HasPrefix(src[off:], c.source) {
			t.Errorf("%q maps to %d (%q), want %q", c.compiled, off, src[min(off, len(src)):], c.source)
		}
	}

	// A piece repeated by a {% for %} is placed once; what follows still is.
	loop := DbtRegion([]byte("select {% for c in cols %}{{ c }}, {% endfor %} 1 from t"))
	m = loop.AlignSkipping("select a, b, c,  1 from t")
	if len(m.spans) != 3 || m.spans[2].piece.Text != "1 from t" {
		t.Errorf("loop spans %+v", m.spans)
	}
}

// The examples' models, compiled by dbt Fusion and by dbt-core: every piece
// of literal SQL lines up, but the two in fct_orders' {% if
// is_incremental() %} block when it compiled to nothing.
func TestDbtRegionsAlignWithExamples(t *testing.T) {
	root := repoPath(t, "examples/dbt")
	for _, fixture := range []string{"dbt-v2.json", "dbt-core.json"} {
		t.Run(fixture, func(t *testing.T) {
			g := crlfGraph(t, "testdata/examples/"+fixture, true)
			checked := 0
			for _, a := range g.Actions {
				if !strings.HasSuffix(a.FileName, ".sql") || a.Query == "" || a.SQL != project.SQLCompiled || strings.HasPrefix(a.FileName, "macros/") {
					continue
				}
				src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.FileName)))
				if err != nil {
					t.Fatal(err)
				}
				r := DbtRegion(src)
				m := r.AlignSkipping(a.Query)
				want := len(r.Pieces)
				if a.FileName == "models/marts/fct_orders.sql" && !strings.Contains(a.Query, "where o.order_date") {
					want -= 2
				}
				if len(m.spans) != want {
					t.Errorf("%s: %d of %d pieces line up", a.FileName, len(m.spans), len(r.Pieces))
				}
				for _, s := range m.spans {
					if a.Query[s.compStart:s.compEnd] != s.piece.Text {
						t.Errorf("%s: %q placed on %q", a.FileName, s.piece.Text, a.Query[s.compStart:s.compEnd])
					}
				}
				checked++
			}
			if checked < 8 {
				t.Errorf("only %d files checked", checked)
			}
		})
	}
}
