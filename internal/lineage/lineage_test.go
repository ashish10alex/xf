package lineage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// The first tests port the extension's "Column lineage from Data Lineage
// API links" suite (src/test/suite/columnLineageTrace.test.ts).

func link(source, sourceField, target, targetField string, types ...string) Link {
	l := Link{
		Source: &EntityReference{FullyQualifiedName: "bigquery:" + source},
		Target: &EntityReference{FullyQualifiedName: "bigquery:" + target},
	}
	if sourceField != "" {
		l.Source.Field = []string{sourceField}
	}
	if targetField != "" {
		l.Target.Field = []string{targetField}
	}
	for _, t := range types {
		l.DependencyInfo = append(l.DependencyInfo, struct {
			DependencyType string `json:"dependencyType"`
		}{t})
	}
	return l
}

func TestFieldAndFQN(t *testing.T) {
	if got := Field("PLAYER_ID"); !reflect.DeepEqual(got, []string{"player_id"}) {
		t.Errorf("Field = %v", got)
	}
	if got := FQN("p.mart.player_stats"); got != "bigquery:p.mart.player_stats" {
		t.Errorf("FQN = %q", got)
	}
}

func TestColumnLinksKeepsOnePerColumnPreferringACopy(t *testing.T) {
	links := ColumnLinksFromAPI([]Link{
		link("p.mart.player_stats", "player_id", "p.rpt.top_scorers", "player_id", "OTHER"),
		link("p.mart.player_stats", "player_id", "p.rpt.top_scorers", "player_id", "EXACT_COPY", "OTHER"),
		link("p.mart.player_stats", "player_id", "p.rpt.league_table", "goals_rank", "DEPENDENCY_TYPE_UNSPECIFIED"),
		link("p.mart.player_stats", "player_id", "p.rpt.no_field", "", "OTHER"),
	})
	want := []ColumnLink{
		{Table: "p.rpt.top_scorers", Column: "player_id", DependencyType: ExactCopy},
		{Table: "p.rpt.league_table", Column: "goals_rank", DependencyType: Other},
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("got %+v", links)
	}
}

func TestUntrackedReaders(t *testing.T) {
	tracked := map[string]bool{"p.rpt.league_table": true}
	got := UntrackedReaders(context.Background(),
		[]string{"p.rpt.top_scorers", "p.rpt.league_table", "p.rpt.season_summary"},
		map[string]bool{"p.rpt.top_scorers": true},
		func(_ context.Context, table string) bool { return tracked[table] }, nil)
	if !reflect.DeepEqual(got, []string{"p.rpt.season_summary"}) {
		t.Errorf("got %v", got)
	}
}

func TestUntrackedReadersFoldDevRuns(t *testing.T) {
	// A dev run of an action whose prod run has a link reads nothing more,
	// and the other way round; another action's dev run is still checked.
	dev := map[string]string{"dev.rpt_ashish.top_scorers": "p.rpt.top_scorers", "dev.rpt_ashish.daily": "p.rpt.daily"}
	toProd := func(t string) string {
		if p, ok := dev[t]; ok {
			return p
		}
		return t
	}
	got := UntrackedReaders(context.Background(),
		[]string{"dev.rpt_ashish.top_scorers", "p.rpt.daily", "dev.rpt_ashish.other"},
		map[string]bool{"p.rpt.top_scorers": true, "dev.rpt_ashish.daily": true},
		func(context.Context, string) bool { return false }, toProd)
	if !reflect.DeepEqual(got, []string{"dev.rpt_ashish.other"}) {
		t.Errorf("got %v", got)
	}
}

func TestTablesFromAPIDedupes(t *testing.T) {
	got := TablesFromAPI([]Link{
		link("p.mart.player_stats", "", "p.rpt.top_scorers", ""),
		link("p.mart.player_stats", "", "p.rpt.top_scorers", ""),
		link("p.mart.player_stats", "", "p.rpt.match_events", ""),
	})
	if !reflect.DeepEqual(got, []string{"p.rpt.top_scorers", "p.rpt.match_events"}) {
		t.Errorf("got %v", got)
	}
}

func TestTempTablesAreLeftOut(t *testing.T) {
	scriptTemp := "p._script0f722b7f7e49b6fa.df_5c096741839c6473eb_table_temp"
	emptyTemp := "p.daily.df_5c096741839c6473eb_table_empty"
	if !IsTempTable(scriptTemp) || !IsTempTable(emptyTemp) {
		t.Error("temp tables not spotted")
	}
	if IsTempTable("p.daily.0100_DAILY_ACTUALS") || IsTempTable("p.daily.df_metrics") {
		t.Error("a table only starting with df_ is kept")
	}
	got := ColumnLinksFromAPI([]Link{
		link("p.mart.orders", "order_id", scriptTemp, "order_id", "EXACT_COPY"),
		link("p.mart.orders", "order_id", "p.rpt.daily", "order_id", "EXACT_COPY"),
	})
	if !reflect.DeepEqual(got, []ColumnLink{{Table: "p.rpt.daily", Column: "order_id", DependencyType: ExactCopy}}) {
		t.Errorf("column links %+v", got)
	}
	if got := TablesFromAPI([]Link{link("p.mart.orders", "", emptyTemp, ""), link("p.mart.orders", "", "p.rpt.daily", "")}); !reflect.DeepEqual(got, []string{"p.rpt.daily"}) {
		t.Errorf("tables %v", got)
	}
}

func TestSearchLinksPagesAndExplainsErrors(t *testing.T) {
	var bodies []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "denied") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if body["pageToken"] == nil {
			_, _ = w.Write([]byte(`{"links":[{"source":{"fullyQualifiedName":"bigquery:p.a.b"},"target":{"fullyQualifiedName":"bigquery:p.a.c"}}],"nextPageToken":"t2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"links":[{"source":{"fullyQualifiedName":"bigquery:p.a.b"},"target":{"fullyQualifiedName":"bigquery:p.a.d"}}]}`))
	}))
	defer srv.Close()
	c := NewClientWith(srv.Client(), srv.URL, "")
	links, err := c.SearchLinks(context.Background(), SearchRequest{Parent: "projects/p/locations/eu", Source: &EntityReference{FullyQualifiedName: "bigquery:p.a.b", Field: []string{"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := TablesFromAPI(links); !reflect.DeepEqual(got, []string{"p.a.c", "p.a.d"}) {
		t.Errorf("tables %v", got)
	}
	if paths[0] != "/v1/projects/p/locations/eu:searchLinks" {
		t.Errorf("path %s", paths[0])
	}
	src, _ := bodies[0]["source"].(map[string]any)
	if bodies[0]["pageSize"] != float64(pageSize) || src["fullyQualifiedName"] != "bigquery:p.a.b" || bodies[1]["pageToken"] != "t2" {
		t.Errorf("bodies %v", bodies)
	}
	_, err = c.SearchLinks(context.Background(), SearchRequest{Parent: "projects/denied/locations/eu"})
	if err == nil || !strings.Contains(err.Error(), "no access to Data Lineage in denied") {
		t.Errorf("403: %v", err)
	}
}

// fakeSearch answers searches from links by source (table, or table#field)
// and probes by target table#field, counting calls.
type fakeSearch struct {
	mu      sync.Mutex
	links   map[string][]Link
	targets map[string]int
	// failProbes is how many probes fail, as rate-limited ones would.
	failProbes int
	calls      []string
}

func (f *fakeSearch) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSearch) SearchLinks(_ context.Context, req SearchRequest) ([]Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Target != nil {
		key := strings.TrimPrefix(req.Target.FullyQualifiedName, "bigquery:") + "#" + strings.Join(req.Target.Field, "")
		f.calls = append(f.calls, "probe "+key)
		if f.failProbes > 0 {
			f.failProbes--
			return nil, errors.New("quota exceeded")
		}
		return make([]Link, f.targets[key]), nil
	}
	key := strings.TrimPrefix(req.Source.FullyQualifiedName, "bigquery:")
	if len(req.Source.Field) > 0 {
		key += "#" + req.Source.Field[0]
	}
	f.calls = append(f.calls, "search "+req.Parent+" "+key)
	return f.links[key], nil
}

func TestSourceFindsReadersInTheTablesRegion(t *testing.T) {
	g, err := dataform.ParseCompileJSON([]byte(`{"tables":[
		{"type":"incremental","target":{"database":"p","schema":"mart","name":"events"}},
		{"type":"table","target":{"database":"p","schema":"rpt","name":"daily"}}
	],"assertions":[{"target":{"database":"p","schema":"chk","name":"orders_nonNull"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	tables := NewTables(func(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
		switch dataset + "." + table {
		case "mart.orders":
			return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "Discount", Type: "NUMERIC"}}}, nil
		case "rpt.daily":
			return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "Discount_Total", Type: "NUMERIC"}, {Name: "day", Type: "DATE"}}}, nil
		case "ml.scores":
			return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}}, nil
		case "fin.margins":
			return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "a"}}}, nil
		}
		return nil, &bigquery.APIError{Status: http.StatusNotFound, Message: "not found"}
	})
	search := &fakeSearch{
		links: map[string][]Link{
			"p.mart.orders#discount": {
				link("p.mart.orders", "discount", "p.rpt.daily", "discount_total", "OTHER"),
				link("p.mart.orders", "discount", "p.chk.orders_nonNull", "discount", "OTHER"),
			},
			"p.mart.orders": {
				link("p.mart.orders", "", "p.rpt.daily", ""),
				link("p.mart.orders", "", "p.mart.events", ""),
				link("p.mart.orders", "", "p.ml.scores", ""),
				link("p.mart.orders", "", "p.fin.margins", ""),
			},
		},
		// fin.margins has column lineage (its first column has an upstream
		// link), so it doesn't read discount; ml.scores has none.
		targets: map[string]int{"p.fin.margins#a": 2},
	}
	s := NewSource(search, tables, g, nil)
	ctx := context.Background()

	links, err := s.ColumnLinks(ctx, "p.mart.orders", "Discount")
	if err != nil {
		t.Fatal(err)
	}
	want := []ColumnLink{
		{Table: "p.rpt.daily", Column: "Discount_Total", DependencyType: Other},
		{Table: "p.chk.orders_nonNull", Column: "discount", DependencyType: Other, Assertion: true},
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("column links %+v", links)
	}
	if calls := search.snapshot(); !slices.Contains(calls, "search projects/p/locations/eu p.mart.orders#discount") {
		t.Errorf("calls %v", calls)
	}

	only, err := s.TableOnlyReaders(ctx, "p.mart.orders", map[string]bool{"p.rpt.daily": true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(only, []ColumnLink{{Table: "p.mart.events", DependencyType: TableOnly}, {Table: "p.ml.scores", DependencyType: TableOnly}}) {
		t.Errorf("table-only readers %+v", only)
	}
	for _, c := range search.snapshot() {
		if strings.Contains(c, "p.mart.events") {
			t.Errorf("an incremental reader is never probed: %v", c)
		}
	}

	all, err := s.TableReaders(ctx, "p.mart.orders")
	if err != nil || len(all) != 4 {
		t.Errorf("table readers %+v, %v", all, err)
	}
	n := 0
	for _, c := range search.snapshot() {
		if c == "search projects/p/locations/eu p.mart.orders" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("readers of a table are searched once, got %d", n)
	}

	if _, err := s.ColumnLinks(ctx, "p.gone.table", "x"); err == nil || !strings.Contains(err.Error(), "couldn't read p.gone.table") {
		t.Errorf("missing table: %v", err)
	}
}

func TestTablesRetryAFailedReadButNotAMissingTable(t *testing.T) {
	var mu sync.Mutex
	reads := map[string]int{}
	tables := NewTables(func(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
		mu.Lock()
		defer mu.Unlock()
		reads[table]++
		switch {
		case table == "gone":
			return nil, &bigquery.APIError{Status: http.StatusNotFound, Message: "not found"}
		case reads[table] == 1:
			return nil, &bigquery.APIError{Status: http.StatusTooManyRequests, Message: "rate limited"}
		}
		return &bigquery.Table{Schema: []bigquery.Field{{Name: "a"}}}, nil
	})
	ctx := context.Background()
	if _, err := tables.Schema(ctx, "p.d.flaky"); err == nil {
		t.Fatal("the first read fails")
	}
	if fields, err := tables.Schema(ctx, "p.d.flaky"); err != nil || len(fields) != 1 {
		t.Errorf("a rate-limited read is tried again: %v %v", fields, err)
	}
	for range 2 {
		if fields, err := tables.Schema(ctx, "p.d.gone"); err != nil || fields != nil {
			t.Errorf("missing table: %v %v", fields, err)
		}
	}
	if reads["flaky"] != 2 || reads["gone"] != 1 {
		t.Errorf("reads %v", reads)
	}
}

func TestSourceProbesAgainAfterAFailedProbe(t *testing.T) {
	tables := NewTables(func(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
		return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "a"}}}, nil
	})
	search := &fakeSearch{targets: map[string]int{"p.fin.margins#a": 1}, failProbes: 1}
	s := NewSource(search, tables, &project.Graph{}, nil)
	ctx := context.Background()
	if s.hasColumnLineage(ctx, "p.fin.margins") {
		t.Error("a failed probe finds no lineage")
	}
	if !s.hasColumnLineage(ctx, "p.fin.margins") {
		t.Error("a failed probe is asked again")
	}
	s.hasColumnLineage(ctx, "p.fin.margins")
	n := 0
	for _, c := range search.snapshot() {
		if c == "probe p.fin.margins#a" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("a successful probe is kept: %d probes", n)
	}
}
