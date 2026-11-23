package databricks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/lineage"
)

func TestDatabricksExternalReadersFromRestHistory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/lineage-tracking/table-lineage", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"downstreams":[]}`)
	})
	mux.HandleFunc("GET /api/2.0/lineage-tracking/column-lineage", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"downstream_cols":[]}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/history/queries", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"res": [
				{
					"query_id": "q1",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "bi@acme.com",
					"channel_used": {"name": "Microsoft Power BI"},
					"query_start_time_ms": 1795262400000,
					"query_text": "SELECT order_id, customer_tier FROM main.shop.orders WHERE status = 'ok'"
				},
				{
					"query_id": "q2",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "bi@acme.com",
					"channel_used": {"name": "Microsoft Power BI"},
					"query_start_time_ms": 1795266000000,
					"query_text": "SELECT order_id, customer_tier FROM main.shop.orders WHERE status = 'new'"
				},
				{
					"query_id": "q3",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "tableau@acme.com",
					"channel_used": {"name": "Tableau"},
					"query_start_time_ms": 1795262400000,
					"query_text": "SELECT * FROM shop.orders LIMIT 10"
				},
				{
					"query_id": "q4",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "dbt-ci@acme.com",
					"query_start_time_ms": 1795262400000,
					"query_text": "/* {\"app\": \"dbt\", \"node_id\": \"test.shop.not_null\"} */ SELECT customer_tier FROM main.shop.orders"
				},
				{
					"query_id": "q5",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "analyst@acme.com",
					"query_start_time_ms": 1795262400000,
					"query_text": "SELECT order_id FROM main.shop.orders"
				},
				{
					"query_id": "q6",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "unrelated@acme.com",
					"query_start_time_ms": 1795262400000,
					"query_text": "SELECT orders, customer_tier FROM other.billing.invoices"
				},
				{
					"query_id": "q7",
					"status": "FINISHED",
					"statement_type": "SELECT",
					"user_name": "joiner@acme.com",
					"query_start_time_ms": 1795262400000,
					"query_text": "SELECT o.order_id, c.customer_tier FROM main.shop.orders o JOIN main.shop.customers c ON o.cust_id = c.id"
				}
			],
			"has_next_page": false
		}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})
	r := NewReaders(c, nil, nil).WithExternalReaders(14)
	r.now = func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) }

	ctx := context.Background()
	r.Preload(ctx, []string{"main.shop.orders"})

	links, err := r.ColumnLinks(ctx, "main.shop.orders", "customer_tier")
	if err != nil {
		t.Fatalf("ColumnLinks error: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3 (Power BI confirmed + Tableau SELECT* + joiner probable; excluding unrelated table with bare 'orders' column): %+v", len(links), links)
	}
	byKey := map[string]lineage.ColumnLink{}
	for _, l := range links {
		if l.Consumer == nil {
			t.Fatalf("expected Consumer on %+v", l)
		}
		k := l.Consumer.Tool
		if k == "" {
			k = l.Consumer.User
		}
		byKey[k] = l
	}
	pbi := byKey["Power BI"]
	if pbi.DependencyType != lineage.Other || pbi.Consumer.Queries != 2 || pbi.Consumer.SampleJobID != "q2" {
		t.Errorf("Power BI link = %+v (%+v), want Other with 2 queries and newest sampleJobId q2", pbi, pbi.Consumer)
	}
	// httptest uses http://, so SampleJobURL stays empty (only https:// links are emitted).
	if pbi.Consumer.SampleJobURL != "" {
		t.Errorf("Power BI SampleJobURL = %q, want empty on http:// test server", pbi.Consumer.SampleJobURL)
	}
	tab := byKey["Tableau"]
	if tab.DependencyType != lineage.TableOnly || tab.Consumer.Queries != 1 {
		t.Errorf("Tableau link = %+v (%+v), want TableOnly with 1 query", tab, tab.Consumer)
	}
	joiner := byKey["joiner@acme.com"]
	if joiner.DependencyType != lineage.TableOnly {
		t.Errorf("JOIN query link = %+v, want TableOnly (Warning) rather than confirmed critical", joiner)
	}
	if _, leaked := byKey["unrelated@acme.com"]; leaked {
		t.Error("bare column 'orders' in unrelated table falsely matched main.shop.orders")
	}

	// TableReaders includes analyst reading order_id as well (4 external consumers, dbt & unrelated still filtered).
	all, err := r.TableReaders(ctx, "main.shop.orders")
	if err != nil || len(all) != 4 {
		t.Fatalf("TableReaders = %+v, err=%v (want 4)", all, err)
	}
	if len(r.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %v", r.Warnings())
	}
}

func TestDatabricksExternalReadersErrorIsReportWarningOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/lineage-tracking/column-lineage", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"downstream_cols":[{"name":"discount","catalog_name":"main","schema_name":"shop","table_name":"daily"}]}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/history/queries", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})
	r := NewReaders(c, nil, nil).WithExternalReaders(14)

	links, err := r.ColumnLinks(context.Background(), "main.shop.orders", "discount")
	if err != nil {
		t.Fatalf("expected nil error when optional Query History returns 403, got %v", err)
	}
	if len(links) != 1 || links[0].Table != "main.shop.daily" {
		t.Fatalf("expected Unity Catalog link preserved, got %+v", links)
	}
	if w := r.Warnings(); len(w) != 1 || !strings.Contains(w[0], "Databricks Query History unavailable") {
		t.Errorf("expected Query History warning, got %v", w)
	}
}
