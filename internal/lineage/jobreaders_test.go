package lineage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

type fakeExec struct {
	mu             sync.Mutex
	starts         []warehouse.QueryOptions
	startErr       error
	jobErr         string
	pages          map[string][]*warehouse.Page
	defaultProject string
	calls          atomic.Int32
}

func (f *fakeExec) StartQuery(_ context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.starts = append(f.starts, o)
	f.mu.Unlock()
	if f.startErr != nil {
		return warehouse.JobRef{}, f.startErr
	}
	return warehouse.JobRef{Project: o.Project, Location: o.Location, ID: o.JobPrefix + "test1"}, nil
}

func (f *fakeExec) QueryJob(_ context.Context, _ warehouse.JobRef) (*warehouse.QueryStatus, error) {
	return &warehouse.QueryStatus{State: "DONE", Error: f.jobErr}, nil
}

func (f *fakeExec) Rows(_ context.Context, ref warehouse.JobRef, token string, _ int) (*warehouse.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ps := f.pages[ref.Project]
	if len(ps) == 0 {
		return &warehouse.Page{Complete: true}, nil
	}
	if token == "" {
		return ps[0], nil
	}
	if len(ps) > 1 {
		return ps[1], nil
	}
	return &warehouse.Page{Complete: true}, nil
}

func (f *fakeExec) CancelQuery(_ context.Context, _ warehouse.JobRef) error { return nil }
func (f *fakeExec) DefaultProject() string                                  { return f.defaultProject }

type stubSearcher struct {
	links []Link
	err   error
}

func (s stubSearcher) SearchLinks(_ context.Context, _ SearchRequest) ([]Link, error) {
	return s.links, s.err
}

func fakeTables(locations map[string]string) *Tables {
	return NewTables(func(_ context.Context, project, dataset, table string) (*warehouse.Table, error) {
		key := project + "." + dataset + "." + table
		loc := locations[key]
		if loc == "" {
			loc = "EU"
		}
		return &warehouse.Table{Location: loc}, nil
	})
}

func TestJobHistoryBatchedDiscoveryAndFiltering(t *testing.T) {
	exec := &fakeExec{
		pages: map[string][]*warehouse.Page{
			"acme-bi": {
				{
					Complete: true,
					Rows: [][]any{
						// 1. Looker explicitly reading customer_tier
						{
							"acme-prod.marts.fct_orders",
							"looker-prod@acme.iam.gserviceaccount.com",
							"840",
							"2026-11-20T12:00:00Z",
							`-- Looker Query Context '{"explore":"orders","dashboard_id":"412"}'
SELECT order_id, customer_tier FROM acme-prod.marts.fct_orders`,
							"[]",
							"bquxjob_looker_1",
						},
						// 2. Tableau SA running TWO dashboards: one reads total_usd (unrelated), one reads customer_tier
						{
							"acme-prod.marts.fct_orders",
							"tableau-prod@acme.iam.gserviceaccount.com",
							"500",
							"2026-11-19T12:00:00Z",
							"SELECT order_id, total_usd FROM acme-prod.marts.fct_orders",
							"[]",
							"bquxjob_tab_1",
						},
						{
							"acme-prod.marts.fct_orders",
							"tableau-prod@acme.iam.gserviceaccount.com",
							"120",
							"2026-11-21T08:00:00Z",
							"SELECT order_id, customer_tier FROM acme-prod.marts.fct_orders",
							"[]",
							"bquxjob_tab_2",
						},
						// 3. Analyst running SELECT * (TableOnly)
						{
							"acme-prod.marts.fct_orders",
							"alice@acme.com",
							"3",
							"2026-11-18T09:00:00Z",
							"SELECT * FROM acme-prod.marts.fct_orders LIMIT 10",
							"[]",
							"bquxjob_alice_1",
						},
						// 3b. Analyst running multi-table JOIN touching customer_tier (ambiguous -> TableOnly)
						{
							"acme-prod.marts.fct_orders",
							"carol@acme.com",
							"7",
							"2026-11-19T11:00:00Z",
							"SELECT u.customer_tier FROM acme-prod.marts.fct_orders o JOIN acme-prod.marts.dim_users u ON o.user_id = u.user_id",
							"[]",
							"bquxjob_carol_1",
						},
						// 4. Looker Studio reading only unrelated column (must NOT match customer_tier)
						{
							"acme-prod.marts.fct_orders",
							"bob@acme.com",
							"90",
							"2026-11-20T09:00:00Z",
							"SELECT order_id FROM acme-prod.marts.fct_orders",
							`[{"key":"requestor","value":"looker_studio"}]`,
							"bquxjob_ls_1",
						},
						// 5. Own xf query & dbt test query (must be filtered out completely)
						{
							"acme-prod.marts.fct_orders",
							"dev@acme.com",
							"5",
							"2026-11-21T09:00:00Z",
							"SELECT customer_tier FROM acme-prod.marts.fct_orders",
							`[{"key":"app","value":"xf"}]`,
							"xf_scratch_1234",
						},
						{
							"acme-prod.marts.fct_orders",
							"dbt-ci@acme.iam.gserviceaccount.com",
							"20",
							"2026-11-21T09:00:00Z",
							`/* {"app": "dbt", "node_id": "test.pkg.not_null"} */ SELECT customer_tier FROM acme-prod.marts.fct_orders`,
							"[]",
							"bquxjob_dbt_1",
						},
					},
				},
			},
		},
	}

	tables := fakeTables(map[string]string{"acme-prod.marts.fct_orders": "EU"})
	src := NewSource(stubSearcher{}, tables, nil, nil).
		WithJobHistory(exec, JobHistoryConfig{
			Projects:       []string{"acme-bi"},
			Days:           14,
			MaxBytesBilled: 10 << 30,
		}, NewJobHistoryCache())

	ctx := context.Background()
	src.Preload(ctx, []string{"acme-prod.marts.fct_orders"})

	links, err := src.ColumnLinks(ctx, "acme-prod.marts.fct_orders", "customer_tier")
	if err != nil {
		t.Fatalf("ColumnLinks error: %v", err)
	}
	if len(links) != 4 {
		t.Fatalf("got %d links, want 4 (Looker, Tableau, Alice SELECT*, Carol JOIN): %+v", len(links), links)
	}

	byTool := map[string]ColumnLink{}
	for _, l := range links {
		if l.Consumer == nil {
			t.Fatalf("expected Consumer on link %+v", l)
		}
		key := l.Consumer.Tool
		if key == "" {
			key = l.Consumer.User
		}
		byTool[key] = l
	}

	looker := byTool["Looker"]
	if looker.DependencyType != Other || looker.Consumer.Asset != "explore orders, dashboard 412" || looker.Consumer.Queries != 840 {
		t.Errorf("unexpected Looker link: %+v (%+v)", looker, looker.Consumer)
	}
	tableau := byTool["Tableau"]
	// Only the 120-query dashboard reads customer_tier (the 500-query one only reads total_usd).
	if tableau.DependencyType != Other || tableau.Consumer.Queries != 120 || tableau.Consumer.LastSeen != "2026-11-21T08:00:00Z" {
		t.Errorf("unexpected Tableau link: %+v (%+v)", tableau, tableau.Consumer)
	}
	alice := byTool["alice@acme.com"]
	if alice.DependencyType != TableOnly || alice.Consumer.Queries != 3 {
		t.Errorf("unexpected Alice SELECT* link: %+v (%+v)", alice, alice.Consumer)
	}
	carol := byTool["carol@acme.com"]
	if carol.DependencyType != TableOnly || carol.Consumer.Queries != 7 {
		t.Errorf("multi-table JOIN should downgrade to TableOnly, got: %+v (%+v)", carol, carol.Consumer)
	}

	// TableReaders (for deleted table) MUST include Looker Studio too (5 consumers total), still excluding xf & dbt.
	tLinks, err := src.TableReaders(ctx, "acme-prod.marts.fct_orders")
	if err != nil {
		t.Fatalf("TableReaders error: %v", err)
	}
	if len(tLinks) != 5 {
		t.Fatalf("got %d table readers, want 5 (including Looker Studio and Carol): %+v", len(tLinks), tLinks)
	}

	// Verify only 1 BigQuery job was started, with xf_impact_ prefix, app=xf label, and maxBytesBilled.
	if exec.calls.Load() != 1 {
		t.Errorf("StartQuery called %d times, want 1 batched call", exec.calls.Load())
	}
	if len(exec.starts) != 1 || exec.starts[0].JobPrefix != "xf_impact_" || exec.starts[0].MaximumBytesBilled != 10<<30 || exec.starts[0].Labels["app"] != "xf" {
		t.Errorf("unexpected StartQuery opts: %+v", exec.starts)
	}
	if !strings.Contains(exec.starts[0].SQL, "`acme-bi.region-eu.INFORMATION_SCHEMA.JOBS_BY_PROJECT`") {
		t.Errorf("unexpected SQL view:\n%s", exec.starts[0].SQL)
	}
}

func TestJobHistoryAutoAndOrgModes(t *testing.T) {
	exec := &fakeExec{defaultProject: "quota-proj"}
	tables := fakeTables(map[string]string{"acme-prod.marts.orders": "US"})

	// auto mode expands to own project + extraProjects
	srcAuto := NewSource(stubSearcher{}, tables, nil, nil).
		WithProjects("acme-bi").
		WithJobHistory(exec, JobHistoryConfig{Projects: []string{"auto"}, Days: 7}, nil)
	_, err := srcAuto.TableReaders(context.Background(), "acme-prod.marts.orders")
	if err != nil {
		t.Fatalf("auto mode error: %v", err)
	}
	if exec.calls.Load() != 2 {
		t.Fatalf("auto mode made %d queries, want 2 (acme-prod and acme-bi)", exec.calls.Load())
	}

	// org mode queries JOBS_BY_ORGANIZATION once, taking precedence over project entries so counts never double.
	execOrg := &fakeExec{defaultProject: "quota-proj"}
	srcOrg := NewSource(stubSearcher{}, tables, nil, nil).
		WithJobHistory(execOrg, JobHistoryConfig{Projects: []string{"org", "acme-bi"}, Days: 14}, nil)
	_, err = srcOrg.TableReaders(context.Background(), "acme-prod.marts.orders")
	if err != nil {
		t.Fatalf("org mode error: %v", err)
	}
	if execOrg.calls.Load() != 1 {
		t.Fatalf("org mode made %d queries, want 1 (org takes precedence over acme-bi)", execOrg.calls.Load())
	}
	if !strings.Contains(execOrg.starts[0].SQL, "`region-us.INFORMATION_SCHEMA.JOBS_BY_ORGANIZATION`") {
		t.Errorf("org SQL missing JOBS_BY_ORGANIZATION:\n%s", execOrg.starts[0].SQL)
	}

	// Regional dataset location casing is preserved (not uppercased to EUROPE-WEST2).
	regTables := fakeTables(map[string]string{"acme-prod.marts.orders": "europe-west2"})
	execReg := &fakeExec{}
	srcReg := NewSource(stubSearcher{}, regTables, nil, nil).
		WithJobHistory(execReg, JobHistoryConfig{Projects: []string{"acme-bi"}, Days: 7}, nil)
	_, err = srcReg.TableReaders(context.Background(), "acme-prod.marts.orders")
	if err != nil {
		t.Fatalf("regional mode error: %v", err)
	}
	if execReg.starts[0].Location != "europe-west2" {
		t.Errorf("Location = %q, want europe-west2 preserved", execReg.starts[0].Location)
	}
}

func TestJobHistoryErrorsAreReportWarningsNotColumnErrors(t *testing.T) {
	tables := fakeTables(map[string]string{"acme-prod.marts.orders": "EU"})

	// 403 permission error preserves Dataplex links, returns nil column error, and records report-level warning.
	dpLink := Link{
		Target: &EntityReference{FullyQualifiedName: "bigquery:acme-prod.marts.downstream", Field: []string{"customer_tier"}},
	}
	exec403 := &fakeExec{startErr: &warehouse.APIError{Status: http.StatusForbidden, Message: "Access Denied"}}
	src := NewSource(stubSearcher{links: []Link{dpLink}}, tables, nil, nil).
		WithJobHistory(exec403, JobHistoryConfig{Projects: []string{"acme-bi"}}, nil)

	links, err := src.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil {
		t.Fatalf("expected nil column error on optional query-history 403, got %v", err)
	}
	if len(links) != 1 || links[0].Table != "acme-prod.marts.downstream" {
		t.Fatalf("expected Dataplex link preserved on 403, got %+v", links)
	}
	warns := src.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], "roles/bigquery.resourceViewer") {
		t.Errorf("expected resourceViewer warning, got %v", warns)
	}

	// bytesBilledLimitExceeded produces actionable cap warning without failing clean columns.
	execCap := &fakeExec{jobErr: "Query exceeded limit for bytes billed: 10737418240. 625000000000 or higher required."}
	srcCap := NewSource(stubSearcher{}, tables, nil, nil).
		WithJobHistory(execCap, JobHistoryConfig{Projects: []string{"acme-bi"}, MaxBytesBilled: 10 << 30}, nil)
	_, err = srcCap.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil {
		t.Fatalf("expected nil column error on scan cap hit, got %v", err)
	}
	if w := srcCap.Warnings(); len(w) != 1 || !strings.Contains(w[0], "queryHistoryMaxGB") {
		t.Errorf("expected queryHistoryMaxGB warning, got %v", w)
	}

	// bytesBilledLimitExceeded reason string variant also caught.
	execCap2 := &fakeExec{startErr: errors.New("bytesBilledLimitExceeded")}
	srcCap2 := NewSource(stubSearcher{}, tables, nil, nil).
		WithJobHistory(execCap2, JobHistoryConfig{Projects: []string{"acme-bi"}, MaxBytesBilled: 10 << 30}, nil)
	_, err = srcCap2.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil {
		t.Fatalf("expected nil column error, got %v", err)
	}
	if w := srcCap2.Warnings(); len(w) != 1 || !strings.Contains(w[0], "queryHistoryMaxGB") {
		t.Errorf("expected queryHistoryMaxGB warning for reason string, got %v", w)
	}
}

func TestJobHistoryTruncationWarningAndCacheExtraIsolation(t *testing.T) {
	// Exceeding maxJobHistoryPerTable rows for one table records a truncation warning.
	rows := make([][]any, maxJobHistoryPerTable+1)
	for i := range rows {
		rows[i] = []any{
			"acme-prod.marts.orders",
			"user@acme.com",
			"1",
			"2026-11-23T11:00:00Z",
			"SELECT customer_tier FROM acme-prod.marts.orders",
			"[]",
			"job",
		}
	}
	exec := &fakeExec{pages: map[string][]*warehouse.Page{"acme-bi": {{Complete: true, Rows: rows}}}}
	tables := fakeTables(map[string]string{"acme-prod.marts.orders": "EU"})
	src := NewSource(stubSearcher{}, tables, nil, nil).
		WithJobHistory(exec, JobHistoryConfig{Projects: []string{"acme-bi"}}, nil)
	_, _ = src.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if w := src.Warnings(); len(w) != 1 || !strings.Contains(w[0], "100-query-group cap") {
		t.Errorf("expected truncation warning, got %v", w)
	}

	// Shared cache with "auto" must distinguish different extraProjects lists.
	cache := NewJobHistoryCache()
	exec2 := &fakeExec{}
	s1 := NewSource(stubSearcher{}, tables, nil, nil).WithProjects("bi-a").WithJobHistory(exec2, JobHistoryConfig{Projects: []string{"auto"}}, cache)
	_, _ = s1.TableReaders(context.Background(), "acme-prod.marts.orders")
	c1 := exec2.calls.Load()

	s2 := NewSource(stubSearcher{}, tables, nil, nil).WithProjects("bi-b").WithJobHistory(exec2, JobHistoryConfig{Projects: []string{"auto"}}, cache)
	_, _ = s2.TableReaders(context.Background(), "acme-prod.marts.orders")
	if exec2.calls.Load() == c1 {
		t.Error("expected cache miss when auto mode extraProjects changed from bi-a to bi-b")
	}
}

func TestJobHistoryTTLCache(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	cache := NewJobHistoryCache()
	exec := &fakeExec{
		pages: map[string][]*warehouse.Page{
			"acme-bi": {{
				Complete: true,
				Rows: [][]any{{
					"acme-prod.marts.orders", "alice@acme.com", "2", "2026-11-23T11:00:00Z",
					"SELECT customer_tier FROM acme-prod.marts.orders", "[]", "job1",
				}},
			}},
		},
	}
	tables := fakeTables(map[string]string{"acme-prod.marts.orders": "EU"})
	cfg := JobHistoryConfig{
		Projects: []string{"acme-bi"},
		Days:     14,
		CacheTTL: 15 * time.Minute,
		Now:      func() time.Time { return now },
	}

	// First Source misses cache and runs 1 query.
	src1 := NewSource(stubSearcher{}, tables, nil, nil).WithJobHistory(exec, cfg, cache)
	links, err := src1.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil || len(links) != 1 {
		t.Fatalf("first call: links=%+v err=%v", links, err)
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", exec.calls.Load())
	}

	// Second Source within TTL hits cache -> still 1 query total.
	now = now.Add(5 * time.Minute)
	src2 := NewSource(stubSearcher{}, tables, nil, nil).WithJobHistory(exec, cfg, cache)
	links, err = src2.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil || len(links) != 1 {
		t.Fatalf("cached call: links=%+v err=%v", links, err)
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("expected cache hit, calls = %d", exec.calls.Load())
	}

	// Past TTL -> runs fresh query.
	now = now.Add(20 * time.Minute)
	src3 := NewSource(stubSearcher{}, tables, nil, nil).WithJobHistory(exec, cfg, cache)
	_, err = src3.ColumnLinks(context.Background(), "acme-prod.marts.orders", "customer_tier")
	if err != nil {
		t.Fatalf("post-TTL call err=%v", err)
	}
	if exec.calls.Load() != 2 {
		t.Fatalf("expected fresh query after TTL expiry, calls = %d", exec.calls.Load())
	}
}
