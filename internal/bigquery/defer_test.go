package bigquery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeferCalls(t *testing.T) {
	var got []string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/tables/there"):
			_, _ = w.Write([]byte(`{"id":"x","type":"VIEW","labels":{"dataform_tools_proxy":"true"},"lastModifiedTime":"1000","view":{"query":"SELECT 1"}}`))
		case strings.HasSuffix(r.URL.Path, "/tables/denied"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Access Denied"}}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &body)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/tables"):
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"tableId":"a"},"type":"VIEW","labels":{"k":"v"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Not found"}}`))
		}
	}))
	defer srv.Close()
	c := newClient(srv.Client(), srv.URL, "")
	ctx := context.Background()

	if ok, err := c.Exists(ctx, "p", "d", "there", false); !ok || err != nil {
		t.Errorf("there: %v %v", ok, err)
	}
	if ok, err := c.Exists(ctx, "p", "d", "gone", false); ok || err != nil {
		t.Errorf("gone: %v %v", ok, err)
	}
	if _, err := c.Exists(ctx, "p", "d", "denied", false); !IsForbidden(err) {
		t.Errorf("denied: %v", err)
	}
	v, err := c.View(ctx, "p", "d", "there")
	if err != nil || v.Labels["dataform_tools_proxy"] != "true" || v.Query != "SELECT 1" || v.LastModified.UnixMilli() != 1000 {
		t.Errorf("view %+v %v", v, err)
	}
	if v, err := c.View(ctx, "p", "d", "gone"); v != nil || err != nil {
		t.Errorf("a missing view is nil: %+v %v", v, err)
	}
	ts, err := c.ListTablesLabelled(ctx, "p", "d")
	if err != nil || len(ts) != 1 || ts[0].Labels["k"] != "v" {
		t.Errorf("tables %+v %v", ts, err)
	}
	if err := c.CreateView(ctx, ViewSpec{Project: "p", Dataset: "d", Table: "t", Query: "SELECT 2", Labels: map[string]string{"l": "true"}}); err != nil {
		t.Fatal(err)
	}
	if body["view"].(map[string]any)["useLegacySql"] != false || body["tableReference"].(map[string]any)["tableId"] != "t" {
		t.Errorf("create body %v", body)
	}
	if err := c.DeleteTable(ctx, "p", "d", "t"); err != nil {
		t.Errorf("a 204 delete is no error: %v", err)
	}
}
