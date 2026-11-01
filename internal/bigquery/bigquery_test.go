package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bigquery", "table.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTable(t *testing.T) {
	var gotPath, gotQuery, gotQuota string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotQuota = r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("X-Goog-User-Project")
		_, _ = w.Write(fixture(t))
	}))
	defer srv.Close()

	c := newClient(srv.Client(), srv.URL, "quota-proj")
	tbl, err := c.Table(context.Background(), "demo-project", "analytics", "dev_report")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bigquery/v2/projects/demo-project/datasets/analytics/tables/dev_report" {
		t.Errorf("path %s", gotPath)
	}
	if gotQuery != "fields=type%2Cdescription%2Clocation%2ClastModifiedTime%2Cschema%2CtimePartitioning%28field%29%2CrangePartitioning%28field%29%2Cview%28query%29%2CmaterializedView%28query%29" {
		t.Errorf("query %s", gotQuery)
	}
	if gotQuota != "quota-proj" {
		t.Errorf("quota project header %q", gotQuota)
	}
	if tbl.Type != "TABLE" || tbl.Location != "EU" || !strings.HasPrefix(tbl.Description, "Daily report") {
		t.Errorf("table %+v", tbl)
	}
	if want := time.Date(2026, 9, 30, 8, 54, 57, 0, time.UTC); !tbl.LastModified.Equal(want) {
		t.Errorf("last modified %v, want %v", tbl.LastModified, want)
	}
	if len(tbl.Schema) != 3 || tbl.Schema[0].Mode != "REQUIRED" || len(tbl.Schema[2].Fields) != 2 || tbl.Schema[2].Fields[1].Mode != "REPEATED" {
		t.Errorf("schema %+v", tbl.Schema)
	}
}

func TestTableWithoutQuotaProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["X-Goog-User-Project"]; ok {
			t.Error("sent a quota project header without a quota project")
		}
		_, _ = w.Write([]byte(`{"type":"VIEW","view":{"query":"SELECT 1"}}`))
	}))
	defer srv.Close()
	tbl, err := newClient(srv.Client(), srv.URL, "").Table(context.Background(), "p", "d", "t")
	if err != nil || tbl.Type != "VIEW" || tbl.ViewQuery != "SELECT 1" || !tbl.LastModified.IsZero() {
		t.Fatalf("got %+v, %v", tbl, err)
	}
}

func TestTableErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantAuth bool
		wantMsg  string
	}{
		{"not found", 404, `{"error":{"code":404,"message":"Not found: Table p:d.t was not found in location EU"}}`, false, "Not found: Table p:d.t"},
		{"no permission", 403, `{"error":{"code":403,"message":"Access Denied: Table p:d.t"}}`, false, "Access Denied"},
		{"unauthorized", 401, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`, true, "invalid authentication"},
		{"not json", 502, `<html>bad gateway</html>`, false, "Bad Gateway (HTTP 502)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := newClient(srv.Client(), srv.URL, "").Table(context.Background(), "p", "d", "t")
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) || errors.Is(err, ErrAuth) != tt.wantAuth {
				t.Fatalf("got %v", err)
			}
		})
	}
}

type failingTokens struct{}

func (failingTokens) Token() (*oauth2.Token, error) {
	return nil, &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "reauth related error (invalid_rapt)"}
}

func TestTableTokenRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent without a token")
	}))
	defer srv.Close()
	httpClient := oauth2.NewClient(context.Background(), failingTokens{})
	_, err := newClient(httpClient, srv.URL, "").Table(context.Background(), "p", "d", "t")
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "invalid_rapt") {
		t.Fatalf("got %v", err)
	}
}

func TestTableCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := newClient(srv.Client(), srv.URL, "").Table(ctx, "p", "d", "t")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestNewClientWithoutCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := NewClient(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("got %v", err)
	}
}

func TestDryRun(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"statistics":{"totalBytesProcessed":"1288490189","query":{"totalBytesProcessedAccuracy":"UPPER_BOUND","statementType":"SELECT","schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"tags","type":"STRING","mode":"REPEATED"}]}}}}`))
	}))
	defer srv.Close()

	d, err := newClient(srv.Client(), srv.URL, "").DryRun(context.Background(), "demo-project", "EU", "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/bigquery/v2/projects/demo-project/jobs" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	cfg, _ := gotBody["configuration"].(map[string]any)
	q, _ := cfg["query"].(map[string]any)
	ref, _ := gotBody["jobReference"].(map[string]any)
	if cfg["dryRun"] != true || q["query"] != "select 1" || q["useLegacySql"] != false || ref["projectId"] != "demo-project" || ref["location"] != "EU" {
		t.Errorf("request body %v", gotBody)
	}
	if d.Bytes != 1288490189 || d.Accuracy != AccuracyUpperBound || d.StatementType != "SELECT" {
		t.Errorf("dry run %+v", d)
	}
	if len(d.Schema) != 2 || d.Schema[0].Name != "id" || d.Schema[0].Type != "INTEGER" || d.Schema[1].Mode != "REPEATED" {
		t.Errorf("schema %+v", d.Schema)
	}
	if got := d.CostUSD(); got < 0.00732 || got > 0.00733 { // 1.2 GiB at $6.25/TiB
		t.Errorf("cost %v", got)
	}
}

func TestDryRunQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Unrecognized name: foo at [3:5]"}}`))
	}))
	defer srv.Close()
	_, err := newClient(srv.Client(), srv.URL, "").DryRun(context.Background(), "p", "", "select foo")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "Unrecognized name: foo at [3:5]" || apiErr.Status != 400 {
		t.Fatalf("got %v", err)
	}
}

func TestDryRunWithoutStatistics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"statistics":{"query":{"totalBytesProcessedAccuracy":"UNKNOWN","statementType":"SCRIPT"}}}`))
	}))
	defer srv.Close()
	d, err := newClient(srv.Client(), srv.URL, "").DryRun(context.Background(), "p", "", "declare x int64; select 1")
	if err != nil || d.Bytes != 0 || d.Accuracy != AccuracyUnknown {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestJob(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.Query()
		_, _ = w.Write([]byte(`{"jobReference": {"projectId": "demo-project", "jobId": "job_1", "location": "europe-west2"},
			"statistics": {"startTime": "1791194400000", "endTime": "1791194403200", "totalBytesProcessed": "3221225472", "totalSlotMs": "64000",
				"query": {"totalBytesBilled": "3222274048", "totalBytesProcessed": "3221225472"}}}`))
	}))
	defer srv.Close()

	c := newClient(srv.Client(), srv.URL, "")
	j, err := c.Job(context.Background(), "demo-project", "EU", "job_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bigquery/v2/projects/demo-project/jobs/job_1" || gotQuery["location"][0] != "EU" || !strings.Contains(gotQuery["fields"][0], "totalBytesBilled") {
		t.Errorf("asked %s %v", gotPath, gotQuery)
	}
	start := time.UnixMilli(1791194400000).UTC()
	want := Job{Location: "europe-west2", BytesBilled: 3222274048, BytesProcessed: 3 << 30, SlotMs: 64000, Started: start, Ended: start.Add(3200 * time.Millisecond)}
	if *j != want {
		t.Errorf("job %+v, want %+v", *j, want)
	}
	if got := j.CostUSD(); got < 0.0183 || got > 0.0184 {
		t.Errorf("cost $%.5f, want 3.001 GiB at $6.25/TiB", got)
	}
}

// TestJobWithoutStatistics: a job that billed nothing (a cached or failed
// one) has no query statistics, and no location is sent when none is known.
func TestJobWithoutStatistics(t *testing.T) {
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"statistics": {"startTime": "1791194400000"}}`))
	}))
	defer srv.Close()
	j, err := newClient(srv.Client(), srv.URL, "").Job(context.Background(), "demo-project", "", "job_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := gotQuery["location"]; sent {
		t.Errorf("sent a location: %v", gotQuery)
	}
	if j.BytesBilled != 0 || j.CostUSD() != 0 || !j.Ended.IsZero() || j.Started.IsZero() {
		t.Errorf("job %+v", *j)
	}
}

func TestTablePartition(t *testing.T) {
	for body, want := range map[string]*Partition{
		`{"type":"TABLE"}`: nil,
		`{"type":"TABLE","timePartitioning":{"type":"DAY","field":"event_date"}}`: {Column: "event_date"},
		`{"type":"TABLE","timePartitioning":{"type":"DAY"}}`:                      {}, // by ingestion time
		`{"type":"TABLE","rangePartitioning":{"field":"customer_id"}}`:            {Column: "customer_id"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		tbl, err := newClient(srv.Client(), srv.URL, "").Table(context.Background(), "p", "d", "t")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := tbl.Partition; (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("%s: partition %+v, want %+v", body, got, want)
		}
	}
}

func TestDryRunListsTheTablesItReads(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("fields")
		_, _ = w.Write([]byte(`{"statistics":{"totalBytesProcessed":"10","query":{"totalBytesProcessedAccuracy":"PRECISE","statementType":"SELECT",
			"referencedTables":[{"projectId":"acme","datasetId":"raw","tableId":"events"}]}}}`))
	}))
	defer srv.Close()
	d, err := newClient(srv.Client(), srv.URL, "").DryRun(context.Background(), "acme", "", "select 1 from raw.events")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "referencedTables") {
		t.Errorf("fields %s", gotQuery)
	}
	if len(d.Tables) != 1 || d.Tables[0] != (TableRef{Project: "acme", Dataset: "raw", Table: "events"}) {
		t.Errorf("tables %+v", d.Tables)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 512: "512 B", 1024: "1.00 KiB", 1288490189: "1.20 GiB", 1 << 40: "1.00 TiB", 1 << 60: "1024.00 PiB"} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestActiveJobs: the caller's pending and running jobs since a time, page
// after page.
func TestActiveJobs(t *testing.T) {
	var asked []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query())
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = w.Write([]byte(`{"nextPageToken": "p2", "jobs": [{"jobReference": {"jobId": "dataform-xf-ab12-1", "location": "EU"},
				"configuration": {"query": {"query": "create or replace table x as select 1"}},
				"statistics": {"creationTime": "1791194400000", "startTime": "1791194401000"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jobs": [{"jobReference": {"jobId": "load_1"}, "statistics": {"creationTime": "1791194400000"}}]}`))
	}))
	defer srv.Close()
	since := time.UnixMilli(1791194300000)
	jobs, err := newClient(srv.Client(), srv.URL, "").ActiveJobs(context.Background(), "demo-project", since)
	if err != nil {
		t.Fatal(err)
	}
	want := []ActiveJob{
		{ID: "dataform-xf-ab12-1", Location: "EU", Query: "create or replace table x as select 1", Started: time.UnixMilli(1791194401000).UTC()},
		{ID: "load_1", Started: time.UnixMilli(1791194400000).UTC()},
	}
	if !slices.Equal(jobs, want) {
		t.Errorf("jobs %+v", jobs)
	}
	q := asked[0]
	if len(asked) != 2 || asked[1].Get("pageToken") != "p2" || q.Get("minCreationTime") != "1791194300000" || q.Get("projection") != "full" ||
		!slices.Equal(q["stateFilter"], []string{"pending", "running"}) || q.Has("allUsers") {
		t.Errorf("asked %v", asked)
	}
}
