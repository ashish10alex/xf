package impact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
)

func TestExternalConsumerImpactRenderingAndSeverity(t *testing.T) {
	looker := &lineage.Consumer{
		Tool:     "Looker",
		Asset:    "explore orders, dashboard 412",
		User:     "looker-prod@acme.iam.gserviceaccount.com",
		Queries:  840,
		Days:     14,
		LastSeen: "2026-11-20T10:00:00Z",
	}
	tableau := &lineage.Consumer{
		Tool:    "Tableau",
		User:    "tableau-prod@acme.iam.gserviceaccount.com",
		Queries: 240,
		Days:    14,
	}
	analyst := &lineage.Consumer{
		User:    "alice.smith@acme.com",
		Queries: 5,
		Days:    14,
	}

	rctx := ReaderContext{
		ResolveFile: func(string) string { return "" },
		ToProd:      func(s string) string { return "wrong." + s }, // must NOT mangle external consumer IDs
	}

	// Intentionally pass a colliding link.Table to verify AnnotateReader always uses Consumer.ID().
	links := []lineage.ColumnLink{
		{Table: "acme.marts.fct_orders", DependencyType: lineage.TableOnly, Consumer: analyst},
		{Table: "acme.marts.fct_orders", DependencyType: lineage.Other, Consumer: tableau},
		{Table: "acme.marts.fct_orders", DependencyType: lineage.Other, Consumer: looker},
	}

	readers := ImpactReaders(links, "acme.marts.fct_orders", "customer_tier", rctx)
	if len(readers) != 3 {
		t.Fatalf("got %d readers, want 3 (each consumer must use Consumer.ID(), not queried table)", len(readers))
	}
	// Confirmed column readers (Other) rank ahead of SELECT * (TableOnly),
	// and higher query counts rank first within the same dependency type.
	if readers[0].Consumer.Tool != "Looker" {
		t.Errorf("readers[0] = %+v, want Looker first (highest queries)", readers[0])
	}
	if readers[1].Consumer.Tool != "Tableau" {
		t.Errorf("readers[1] = %+v, want Tableau second", readers[1])
	}
	if readers[2].Consumer.User != "alice.smith@acme.com" {
		t.Errorf("readers[2] = %+v, want analyst third (TableOnly)", readers[2])
	}
	if strings.HasPrefix(readers[0].Table, "wrong.") {
		t.Errorf("ToProd mutated external consumer table ID: %q", readers[0].Table)
	}

	rep := &Report{
		Comparison:   &Comparison{HeadRef: "feat/drop-tier", BaseRef: "origin/main", MergeBase: "abcdef1234"},
		ChangedCount: 1,
		Against:      []string{"acme"},
		AtRisk: []Table{{
			Table:    "acme.marts.fct_orders",
			FileName: "definitions/marts/fct_orders.sqlx",
			Type:     "table",
			Columns: []Column{{
				Column:  "customer_tier",
				Change:  ColumnChange{Kind: Dropped},
				Readers: readers,
			}},
		}},
	}

	worst, ok := Worst(rep)
	if !ok || worst != SeverityCritical {
		t.Fatalf("Worst = (%v, %v), want (SeverityCritical, true)", worst, ok)
	}

	md := Markdown(rep)
	for _, want := range []string{
		"Looker (explore orders, dashboard 412)",
		"queries column",
		"840 queries in 14d · last 2026-11-20 · looker-prod@acme.iam.gserviceaccount.com",
		"Tableau (tableau-prod@acme.iam.gserviceaccount.com)",
		"240 queries in 14d",
		"alice.smith@acme.com",
		"may read",
		"External readers come from recent SELECT queries in warehouse query history.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "#412") {
		t.Errorf("Markdown contains #412 which autolinks to GitHub issues:\n%s", md)
	}
	if strings.Contains(md, "`smith@acme.com`") || strings.Contains(md, "`iam.gserviceaccount.com`") {
		t.Errorf("Markdown mangled consumer email as table path:\n%s", md)
	}

	txt := Text(rep, false, false)
	if !strings.Contains(txt, "Looker (explore orders, dashboard 412)") {
		t.Errorf("Text missing Looker label:\n%s", txt)
	}

	raw, err := JSON(rep)
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	if !strings.Contains(string(raw), `"tool": "Looker"`) {
		t.Errorf("JSON missing consumer metadata:\n%s", string(raw))
	}

	pd := pageData(rep)
	if len(pd.Rows) != 1 || len(pd.Rows[0].Affected) != 3 {
		t.Fatalf("unexpected web rows: %+v", pd.Rows)
	}
	if !strings.Contains(pd.Rows[0].Affected[0].HTML, "Looker (explore orders, dashboard 412)") {
		t.Errorf("web HTML missing Looker label: %q", pd.Rows[0].Affected[0].HTML)
	}
}

func TestTableOnlyExternalConsumerSeverity(t *testing.T) {
	starOnly := []Reader{{
		Table:          "external:query:alice@acme.com",
		DependencyType: lineage.TableOnly,
		Consumer:       &lineage.Consumer{User: "alice@acme.com", Queries: 2, Days: 14},
	}}
	// Dropped column read only via SELECT * external consumer is Warning (review),
	// avoiding hard critical gate failures on ad-hoc SELECT * LIMIT 10.
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, starOnly, ""); got != SeverityWarning {
		t.Errorf("Dropped with SELECT* external consumer = %v, want SeverityWarning", got)
	}
	// Deleted table read by ANY external consumer is still Critical.
	if got := ImpactSeverity(deletedChange, starOnly, ""); got != SeverityCritical {
		t.Errorf("Deleted table with SELECT* external consumer = %v, want SeverityCritical", got)
	}
	// Normal non-consumer TableOnly reader on Dropped stays Critical.
	normalTableOnly := []Reader{{Table: "acme.marts.inc_table", DependencyType: lineage.TableOnly}}
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, normalTableOnly, ""); got != SeverityCritical {
		t.Errorf("Dropped with non-consumer TableOnly = %v, want SeverityCritical", got)
	}
}

func TestRankReadersConsumerDedupAndNormalParity(t *testing.T) {
	c1a := &lineage.Consumer{Tool: "Looker", Asset: "explore a", Queries: 40, Days: 14, LastSeen: "2026-11-18T00:00:00Z"}
	c1b := &lineage.Consumer{Tool: "Looker", Asset: "explore a", Queries: 40, Days: 14, LastSeen: "2026-11-18T00:00:00Z"}
	c2 := &lineage.Consumer{Tool: "Tableau", User: "tableau@acme.com", Queries: 10}
	readers := []Reader{
		{Table: c1a.ID(), DependencyType: lineage.TableOnly, Consumer: c1a},
		{Table: c1b.ID(), DependencyType: lineage.Other, Consumer: c1b},
		// Normal lineage duplicate must keep first-seen DependencyType (parity).
		{Table: "acme.marts.downstream", DependencyType: lineage.Other},
		{Table: "acme.marts.downstream", DependencyType: lineage.ExactCopy},
		{Table: c2.ID(), DependencyType: lineage.Other, Consumer: c2},
	}
	got := RankReaders(readers)
	if len(got) != 3 {
		t.Fatalf("got %d readers, want 3", len(got))
	}
	if got[0].Table != "acme.marts.downstream" || got[0].DependencyType != lineage.Other {
		t.Errorf("got[0] = %+v, want normal reader keeping first-seen Other", got[0])
	}
	// Deduplicated consumer upgrades TableOnly -> Other without double-counting Queries.
	if got[1].Consumer == nil || got[1].Consumer.Queries != 40 || got[1].DependencyType != lineage.Other {
		t.Errorf("got[1] deduped consumer = %+v (consumer=%+v)", got[1], got[1].Consumer)
	}
	if got[2].Consumer == nil || got[2].Consumer.Tool != "Tableau" {
		t.Errorf("got[2] = %+v, want Tableau", got[2])
	}
}

type nestedTestReaders struct{}

func (nestedTestReaders) ColumnLinks(_ context.Context, _, col string) ([]lineage.ColumnLink, error) {
	star := &lineage.Consumer{User: "alice@acme.com", Queries: 2, Days: 14}
	if col == "user.zip" {
		return []lineage.ColumnLink{{Table: star.ID(), DependencyType: lineage.TableOnly, Consumer: star}}, nil
	}
	if col == "user" {
		return []lineage.ColumnLink{
			{Table: "acme.marts.downstream", Column: "user_copy", DependencyType: lineage.ExactCopy},
			{Table: star.ID(), DependencyType: lineage.TableOnly, Consumer: star},
		}, nil
	}
	return nil, nil
}
func (nestedTestReaders) TableOnlyReaders(_ context.Context, _ string, _ map[string]bool) ([]lineage.ColumnLink, error) {
	return nil, nil
}
func (nestedTestReaders) TableReaders(_ context.Context, _ string) ([]lineage.ColumnLink, error) {
	return nil, nil
}

func TestNestedFallbackNotBlockedByConsumerAndPreservesTableOnly(t *testing.T) {
	links, err := columnReaders(context.Background(), nestedTestReaders{}, "acme.marts.orders", "user.zip")
	if err != nil {
		t.Fatal(err)
	}
	foundTable := false
	for _, l := range links {
		if l.Table == "acme.marts.downstream" {
			foundTable = true
			if l.DependencyType != lineage.Other {
				t.Errorf("parent table link dep = %v, want Other", l.DependencyType)
			}
		}
		if l.Consumer != nil && l.DependencyType != lineage.TableOnly {
			t.Errorf("SELECT* consumer was inflated to %v, want TableOnly", l.DependencyType)
		}
	}
	if !foundTable {
		t.Fatalf("nested parent Dataplex fallback was skipped when consumer link existed: %+v", links)
	}
}
