package bigquery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListDatasetsAndTablesPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/datasets") && r.URL.Query().Get("pageToken") == "":
			_, _ = w.Write([]byte(`{"nextPageToken":"n2","datasets":[{"datasetReference":{"datasetId":"a"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/datasets"):
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"datasetId":"b"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/tables"):
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"tableId":"t"},"type":"VIEW"}]}`))
		}
	}))
	defer srv.Close()
	c := NewClientWithHTTP(srv.Client(), srv.URL, "")
	ds, err := c.ListDatasets(context.Background(), "p")
	if err != nil || len(ds) != 2 || ds[0] != "a" || ds[1] != "b" {
		t.Errorf("datasets = %v, %v, want both pages", ds, err)
	}
	ts, err := c.ListTables(context.Background(), "p", "a")
	if err != nil || len(ts) != 1 || ts[0].ID != "t" || ts[0].Type != "VIEW" {
		t.Errorf("tables = %+v, %v", ts, err)
	}
}

func TestDescribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"VIEW","description":"d","schema":{"fields":[{"name":"r","type":"RANGE","rangeElementType":{"type":"DATE"}}]},"rangePartitioning":{"field":"x"}}`))
	}))
	defer srv.Close()
	d, err := NewClientWithHTTP(srv.Client(), srv.URL, "").Describe(context.Background(), "p", "d", "t")
	if err != nil {
		t.Fatal(err)
	}
	// A view has neither rows nor bytes, which are not zero rows.
	if d.NumRows != -1 || d.NumBytes != -1 || d.PartitionType != "RANGE" || d.PartitionField != "x" || d.Schema[0].RangeElementType.Type != "DATE" {
		t.Errorf("detail = %+v", d)
	}
}
