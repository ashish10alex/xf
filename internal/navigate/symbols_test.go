package navigate

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// tree renders symbols as "detail name" lines, children indented.
func tree(symbols []Symbol, indent string) []string {
	var out []string
	for _, s := range symbols {
		out = append(out, indent+s.Kind.Detail()+" "+s.Name)
		out = append(out, tree(s.Children, indent+"  ")...)
	}
	return out
}

// The fixtures and expectations come from vscode-dataform-tools'
// getDocumentSymbols tests; refs are named by their call on one line.
func TestDocumentSymbols(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string
	}{
		{"document_symbols.sqlx", []string{
			"cte PLAYERS",
			`  ref ref("PLAYERS")`,
			"cte PLAYER_VALUATIONS",
			`  ref ref("PLAYER_VALUATIONS")`,
			"cte GAMES",
			`  ref ref("football_data", "GAMES")`,
			"cte GAME_EVENTS",
			`  ref ref("football_data", "GAME_EVENTS")`,
			"cte MORE_GAME_EVENTS",
			`  ref ref("drawingfire-b72a8", "football_data", "GAME_EVENTS")`,
			"bq_table raw-project.raw-dataset.raw-table",
		}},
		{"cte_symbols.sqlx", []string{
			"cte pre_cte",
			`  ref ref("PLAYERS")`,
			"cte quoted cte",
			`  ref ref("GAMES")`,
			"cte outer_cte",
			"cte inner_cte",
			"  bq_table raw-project.raw-dataset.raw-table",
			"cte inc_cte",
			`ref ref("GAME_EVENTS")`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			src, err := os.ReadFile(testdata(t, "symbols", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			got := tree(DocumentSymbols("/p/definitions/x.sqlx", src), "")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestDocumentSymbolRanges(t *testing.T) {
	src, err := os.ReadFile(testdata(t, "symbols", "cte_symbols.sqlx"))
	if err != nil {
		t.Fatal(err)
	}
	syms := DocumentSymbols("/p/definitions/x.sqlx", src)
	quoted := syms[1]
	if got := string(src[quoted.SelStart:quoted.SelEnd]); got != "quoted cte" {
		t.Errorf("CTE selection %q", got)
	}
	if body := string(src[quoted.Start:quoted.End]); !strings.HasPrefix(body, "`quoted cte` AS (") || !strings.HasSuffix(body, ")") {
		t.Errorf("CTE range %q", body)
	}
	ref := quoted.Children[0]
	if got := string(src[ref.SelStart:ref.SelEnd]); got != "GAMES" {
		t.Errorf("ref selection %q, want the first argument", got)
	}
	if got := string(src[ref.Start:ref.End]); got != `ref("GAMES")` {
		t.Errorf("ref range %q", got)
	}
}

func TestDocumentSymbolsSkipsJSFiles(t *testing.T) {
	if got := DocumentSymbols("/p/includes/x.js", []byte(`const a = ref("x");`)); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestReferencesFirst(t *testing.T) {
	src, err := os.ReadFile(testdata(t, "symbols", "cte_symbols.sqlx"))
	if err != nil {
		t.Fatal(err)
	}
	got := tree(ReferencesFirst(DocumentSymbols("/p/definitions/x.sqlx", src)), "")
	want := []string{
		`ref ref("PLAYERS")`,
		`ref ref("GAMES")`,
		"bq_table raw-project.raw-dataset.raw-table",
		`ref ref("GAME_EVENTS")`,
		"cte pre_cte",
		"cte quoted cte",
		"cte outer_cte",
		"cte inner_cte",
		"cte inc_cte",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
