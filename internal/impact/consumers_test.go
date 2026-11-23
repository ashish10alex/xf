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
	if !ok || worst != SeverityWarning {
		t.Fatalf("Worst = (%v, %v), want (SeverityWarning, true) for external-only readers", worst, ok)
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

func TestExternalConsumerSeverityIsReviewWarning(t *testing.T) {
	confirmed := []Reader{{
		Table:          "external:looker:explore-orders",
		DependencyType: lineage.Other,
		Consumer:       &lineage.Consumer{Tool: "Looker", Asset: "explore orders", Queries: 840, Days: 14},
	}}
	starOnly := []Reader{{
		Table:          "external:query:alice@acme.com",
		DependencyType: lineage.TableOnly,
		Consumer:       &lineage.Consumer{User: "alice@acme.com", Queries: 2, Days: 14},
	}}
	// External query-history consumers (confirmed or SELECT *) always grade as
	// Warning (REVIEW) on drops and deletions so historical queries don't
	// permanently wedge a --fail-on critical gate after downstream tables are fixed.
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, confirmed, ""); got != SeverityWarning {
		t.Errorf("Dropped with confirmed external consumer = %v, want SeverityWarning", got)
	}
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, starOnly, ""); got != SeverityWarning {
		t.Errorf("Dropped with SELECT* external consumer = %v, want SeverityWarning", got)
	}
	if got := ImpactSeverity(deletedChange, confirmed, ""); got != SeverityWarning {
		t.Errorf("Deleted table with external-only consumer = %v, want SeverityWarning", got)
	}
	// Mixed open lineage table reader + external consumer stays Critical until
	// the lineage reader is settled, then drops cleanly to Warning.
	mixed := []Reader{
		confirmed[0],
		{Table: "acme.marts.inc_table", DependencyType: lineage.ExactCopy},
	}
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, mixed, ""); got != SeverityCritical {
		t.Errorf("Dropped with open lineage + external consumer = %v, want SeverityCritical", got)
	}
	mixed[1].ProbablyUpdated = true
	if got := ImpactSeverity(ColumnChange{Kind: Dropped}, mixed, ""); got != SeverityWarning {
		t.Errorf("Dropped with settled lineage + external consumer = %v, want SeverityWarning", got)
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

func TestConsumerJobLinkAcrossMarkdownWebAndText(t *testing.T) {
	jobURL := lineage.BigQueryJobURL("acme-bi", "EU", "job_xyz")
	cons := &lineage.Consumer{
		Tool:         "Tableau",
		User:         "mm@acme.com",
		Queries:      8,
		Days:         1,
		LastSeen:     "2026-11-23T12:11:15Z",
		SampleJobID:  "job_xyz",
		SampleJobURL: jobURL,
	}
	rep := &Report{
		AtRisk: []Table{{
			Table: "acme-prod.marts.fct_orders",
			Columns: []Column{{
				Column:  "platform",
				Change:  ColumnChange{Kind: Dropped},
				Readers: []Reader{{Table: cons.ID(), DependencyType: lineage.Other, Consumer: cons}},
			}},
		}},
	}

	// PR Markdown links "last 2026-11-23" to the job URL.
	md := Markdown(rep)
	wantLink := "[last 2026-11-23](" + jobURL + ")"
	if !strings.Contains(md, wantLink) {
		t.Errorf("Markdown missing linked date %q:\n%s", wantLink, md)
	}

	// xf web PageData renders a safe target=_blank <a> with escaped &amp; query params.
	pg := pageData(rep)
	wantHTML := `<a href="https://console.cloud.google.com/bigquery?project=acme-bi&amp;j=bq:EU:job_xyz&amp;page=queryresults" target="_blank" rel="noopener noreferrer">last 2026-11-23</a>`
	if len(pg.Rows) != 1 || len(pg.Rows[0].Affected) != 1 || !strings.Contains(pg.Rows[0].Affected[0].HTML, wantHTML) {
		t.Errorf("PageData affected HTML missing link %q: %+v", wantHTML, pg.Rows)
	}
	if len(pg.Readers) != 1 || !strings.Contains(pg.Readers[0].Note, wantHTML) {
		t.Errorf("PageData reader note missing link %q: %+v", wantHTML, pg.Readers)
	}

	// Terminal Text keeps the reader line clean and puts "↗ <url>" on the sub-line.
	txt := Text(rep, false, false)
	if strings.Contains(txt, "[last 2026-11-23]") {
		t.Errorf("Text leaked raw Markdown link:\n%s", txt)
	}
	if !strings.Contains(txt, "last 2026-11-23") || !strings.Contains(txt, "↗ "+jobURL) {
		t.Errorf("Text missing plain date or job URL sub-line:\n%s", txt)
	}

	// Plain ReaderNote (used by GitHub annotations) stays link-free.
	if note := ReaderNote(Reader{Consumer: cons}); strings.Contains(note, "https://") {
		t.Errorf("ReaderNote should stay plain text, got %q", note)
	}

	// Unsafe URLs must never become clickable <a> tags in InlineHTML.
	if got := InlineHTML(`[last 2026-11-23](javascript:alert(1)) and [last 2026-11-23](http://evil.example/x)`); strings.Contains(got, "<a ") {
		t.Errorf("InlineHTML created link for unsafe URL: %q", got)
	}
	// Links inside backticks stay literal <code> (never clickable <a> or ↗ in Text).
	codeLink := "`[last 2026-11-23](https://evil.example/x)`"
	if got := InlineHTML(codeLink); strings.Contains(got, "<a ") || !strings.Contains(got, "<code>") {
		t.Errorf("InlineHTML turned backticked link into <a>: %q", got)
	}
	if _, extracted := plainWithLink(codeLink); extracted != "" {
		t.Errorf("plainWithLink extracted URL from backticked code span: %q", extracted)
	}
	// Bold wrapping a job link renders <strong> around <a> cleanly.
	boldLink := InlineHTML("**Tableau · [last 2026-11-23](" + jobURL + ")**")
	if !strings.HasPrefix(boldLink, "<strong>") || !strings.HasSuffix(boldLink, "</strong>") || !strings.Contains(boldLink, wantHTML) {
		t.Errorf("InlineHTML failed to keep bold around link: %q", boldLink)
	}
	// Unicode format characters (Cf, e.g. RLO U+202E) are rejected by SafeJobURL.
	if got := lineage.SafeJobURL("https://console.cloud.google.com/bigquery?j=\u202ebq"); got != "" {
		t.Errorf("SafeJobURL accepted U+202E format char: %q", got)
	}
}
