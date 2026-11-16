package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// fakeWorkspace answers the calls a Client makes, keeping the statements it
// was sent.
type fakeWorkspace struct {
	mu         sync.Mutex
	statements []string
	// answer is a statement's answer by its SQL's first word; pending
	// statements answer PENDING this many polls first.
	answer  func(sql string) string
	pending int
	polls   int
	state   string
}

func (f *fakeWorkspace) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/2.0/sql/statements", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Statement string `json:"statement"`
			Warehouse string `json:"warehouse_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Warehouse != "wh1" {
			t.Errorf("warehouse_id = %q", req.Warehouse)
		}
		f.mu.Lock()
		f.statements = append(f.statements, req.Statement)
		pending := f.pending > 0
		f.mu.Unlock()
		if pending {
			_, _ = io.WriteString(w, `{"statement_id":"s1","status":{"state":"PENDING"}}`)
			return
		}
		_, _ = io.WriteString(w, f.answer(req.Statement))
	})
	mux.HandleFunc("GET /api/2.0/sql/statements/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.polls++
		f.pending--
		pending := f.pending > 0
		last := f.statements[len(f.statements)-1]
		f.mu.Unlock()
		if pending {
			_, _ = io.WriteString(w, `{"statement_id":"s1","status":{"state":"RUNNING"}}`)
			return
		}
		_, _ = io.WriteString(w, f.answer(last))
	})
	mux.HandleFunc("GET /api/2.0/sql/warehouses/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"`+r.PathValue("id")+`","name":"Starter","state":"`+f.state+`","cluster_size":"2X-Small"}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/warehouses", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"warehouses":[{"id":"wh1","name":"Starter","state":"STOPPED","cluster_size":"2X-Small","enable_serverless_compute":true},{"id":"wh2","name":"Big","state":"RUNNING"}]}`)
	})
	mux.HandleFunc("GET /api/2.1/unity-catalog/tables/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "workspace.shop.orders" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error_code":"TABLE_DOES_NOT_EXIST","message":"Table 'workspace.shop.nope' does not exist."}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"orders","table_type":"MANAGED","comment":"One row an order","created_at":1767225600000,"updated_at":1767312000000,
			"columns":[{"name":"order_id","type_text":"bigint","nullable":false,"position":0},
			{"name":"day","type_text":"date","nullable":true,"position":1,"partition_index":0},
			{"name":"lines","type_text":"array<struct<sku:string,qty:int>>","comment":"what was bought","nullable":true,"position":2}]}`)
	})
	mux.HandleFunc("GET /api/2.1/unity-catalog/schemas", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_token") == "" {
			_, _ = io.WriteString(w, `{"schemas":[{"name":"a"}],"next_page_token":"p2"}`)
			return
		}
		_, _ = io.WriteString(w, `{"schemas":[{"name":"b"}]}`)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dapi-test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error_code":"UNAUTHENTICATED","message":"Invalid access token."}`)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func newTestClient(t *testing.T, f *fakeWorkspace) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c := New(Config{Host: srv.URL, Warehouse: "wh1", Catalog: "workspace", Token: static("dapi-test"), HTTP: srv.Client()})
	c.poll = time.Millisecond
	return c
}

const describeAnswer = `{"statement_id":"s1","status":{"state":"SUCCEEDED"},
	"manifest":{"schema":{"columns":[{"name":"col_name","type_text":"STRING"},{"name":"data_type","type_text":"STRING"},{"name":"comment","type_text":"STRING"}]}},
	"result":{"data_array":[["order_id","bigint",null],["total","decimal(10,2)","the total"]]}}`

func TestCheckDescribesTheQuery(t *testing.T) {
	f := &fakeWorkspace{answer: func(string) string { return describeAnswer }, pending: 2}
	c := newTestClient(t, f)
	res, err := c.Check(context.Background(), "ignored", "ignored", "select 1 as order_id;\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.statements) != 1 || f.statements[0] != "DESCRIBE QUERY select 1 as order_id" {
		t.Errorf("statements = %q", f.statements)
	}
	if f.polls < 2 {
		t.Errorf("polled %d times, want the statement waited on", f.polls)
	}
	want := []warehouse.Column{{Name: "order_id", Type: "bigint"}, {Name: "total", Type: "decimal(10,2)", Description: "the total"}}
	if len(res.Schema) != 2 || res.Schema[0].Name != want[0].Name || res.Schema[1].Type != want[1].Type || res.Schema[1].Description != want[1].Description {
		t.Errorf("schema = %+v", res.Schema)
	}
	if len(res.Tables) != 0 || res.Bytes != 0 {
		t.Errorf("tables %v, bytes %d: a Databricks check reads neither", res.Tables, res.Bytes)
	}
}

// A query DESCRIBE QUERY fails is explained: EXPLAIN gives the planner's
// words.
func TestCheckExplainsAFailure(t *testing.T) {
	f := &fakeWorkspace{answer: func(sql string) string {
		if strings.HasPrefix(sql, "EXPLAIN ") {
			return `{"statement_id":"s2","status":{"state":"SUCCEEDED"},"result":{"data_array":[["Error occurred during query planning: \n[TABLE_OR_VIEW_NOT_FOUND] The table or view ` + "`nope`" + ` cannot be found."]]}}`
		}
		return `{"statement_id":"s1","status":{"state":"FAILED","error":{"error_code":"BAD_REQUEST","message":"[TABLE_OR_VIEW_NOT_FOUND] terse"}}}`
	}}
	c := newTestClient(t, f)
	_, err := c.Check(context.Background(), "", "", "select * from nope")
	api, ok := errors.AsType[*warehouse.APIError](err)
	if !ok || api.Message != "[TABLE_OR_VIEW_NOT_FOUND] The table or view `nope` cannot be found." {
		t.Errorf("err = %v", err)
	}
	if len(f.statements) != 2 || f.statements[1] != "EXPLAIN select * from nope" {
		t.Errorf("statements = %q", f.statements)
	}

	// EXPLAIN that doesn't say keeps DESCRIBE's words.
	f.answer = func(sql string) string {
		if strings.HasPrefix(sql, "EXPLAIN ") {
			return `{"statement_id":"s2","status":{"state":"SUCCEEDED"},"result":{"data_array":[["== Physical Plan ==\n..."]]}}`
		}
		return `{"statement_id":"s1","status":{"state":"FAILED","error":{"message":"[PARSE_SYNTAX_ERROR] near 'form'"}}}`
	}
	if _, err := c.Check(context.Background(), "", "", "select form x"); err == nil || err.Error() != "[PARSE_SYNTAX_ERROR] near 'form' (HTTP 400)" {
		t.Errorf("err = %v", err)
	}
}

func TestCheckNeedsAWarehouse(t *testing.T) {
	c := newTestClient(t, &fakeWorkspace{})
	c.SetWarehouse("")
	if _, err := c.Check(context.Background(), "", "", "select 1"); !errors.Is(err, ErrNoWarehouse) {
		t.Errorf("err = %v, want ErrNoWarehouse", err)
	}
}

func TestBadTokenIsErrAuth(t *testing.T) {
	c := newTestClient(t, &fakeWorkspace{})
	c.token = static("wrong")
	_, err := c.Table(context.Background(), "workspace", "shop", "orders")
	if !errors.Is(err, warehouse.ErrAuth) || !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "Databricks") {
		t.Errorf("err = %v, want Databricks' ErrAuth", err)
	}
}

func TestRunningAndWarehouses(t *testing.T) {
	f := &fakeWorkspace{state: "STOPPED"}
	c := newTestClient(t, f)
	if up, err := c.Running(context.Background()); err != nil || up {
		t.Errorf("Running = %v, %v; want a stopped warehouse", up, err)
	}
	f.state = "RUNNING"
	if up, err := c.Running(context.Background()); err != nil || !up {
		t.Errorf("Running = %v, %v; want running", up, err)
	}
	ws, err := c.Warehouses(context.Background())
	if err != nil || len(ws) != 2 || ws[0] != (SQLWarehouse{ID: "wh1", Name: "Starter", State: "STOPPED", Size: "2X-Small", Serverless: true}) {
		t.Errorf("Warehouses = %+v, %v", ws, err)
	}
}

func TestTableFromUnityCatalog(t *testing.T) {
	c := newTestClient(t, &fakeWorkspace{})
	tbl, err := c.Table(context.Background(), "workspace", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Type != "MANAGED" || tbl.Description != "One row an order" || tbl.Partition == nil || tbl.Partition.Column != "day" {
		t.Errorf("table = %+v", tbl)
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !tbl.LastModified.Equal(want) {
		t.Errorf("last modified %v, want %v", tbl.LastModified, want)
	}
	s := tbl.Schema
	if len(s) != 3 || s[0].Type != "bigint" || !s[0].Required || s[1].Required || s[2].Type != "array<struct<sku:string,qty:int>>" || s[2].Description != "what was bought" {
		t.Errorf("schema = %+v", s)
	}
	_, err = c.Table(context.Background(), "workspace", "shop", "nope")
	if !warehouse.IsNotFound(err) {
		t.Errorf("missing table: %v, want a 404", err)
	}
	d, err := c.Describe(context.Background(), "workspace", "shop", "orders")
	if err != nil || d.NumRows != -1 || d.PartitionField != "day" || len(d.Schema) != 3 {
		t.Errorf("Describe = %+v, %v", d, err)
	}
	schemas, err := c.ListDatasets(context.Background(), "workspace")
	if err != nil || strings.Join(schemas, ",") != "a,b" {
		t.Errorf("ListDatasets = %v, %v", schemas, err)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "databrickscfg")
	if err := os.WriteFile(cfg, []byte("[DEFAULT]\nhost = https://other.cloud.databricks.com\ntoken = dapi-default\n\n; work\n[work]\nhost = https://dbc-1.cloud.databricks.com/\nauth_type = databricks-cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	var ran []string
	cli := func(_ context.Context, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return []byte(`{"access_token":"oauth-1","token_type":"Bearer","expiry":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`), nil
	}
	base := Setup{Host: "dbc-1.cloud.databricks.com", HTTPPath: "/sql/1.0/warehouses/abc123", Catalog: "workspace", Getenv: getenv, ConfigFile: cfg, CLI: cli}

	// The profile with the target's host gives an OAuth token, from the
	// CLI, asked once while it lasts.
	c := Resolve(base)
	if c.Host != "https://dbc-1.cloud.databricks.com" || c.Warehouse != "abc123" || c.Catalog != "workspace" || c.Profile != "work" {
		t.Errorf("connection = %+v", c)
	}
	for range 2 {
		if tok, err := c.Token(context.Background()); err != nil || tok != "oauth-1" {
			t.Errorf("token = %q, %v", tok, err)
		}
	}
	if len(ran) != 1 || ran[0] != "auth token --profile work" {
		t.Errorf("ran %q", ran)
	}

	// The setting's warehouse and profile win; DEFAULT's token is a PAT.
	s := base
	s.Warehouse, s.Profile = "/sql/1.0/warehouses/fromsetting", "DEFAULT"
	c = Resolve(s)
	if tok, _ := c.Token(context.Background()); c.Warehouse != "fromsetting" || c.Profile != "DEFAULT" || tok != "dapi-default" || c.TokenFrom != "~/.databrickscfg" {
		t.Errorf("connection = %+v, token %q", c, tok)
	}

	// $DATABRICKS_TOKEN, then the dbt target's, before any profile.
	s = base
	s.Token = "dapi-target"
	if tok, _ := Resolve(s).Token(context.Background()); tok != "dapi-target" {
		t.Errorf("token = %q, want the target's", tok)
	}
	env["DATABRICKS_TOKEN"] = "dapi-env"
	if tok, _ := Resolve(s).Token(context.Background()); tok != "dapi-env" {
		t.Errorf("token = %q, want $DATABRICKS_TOKEN", tok)
	}

	// A target that says it in Jinja, or names a cluster, names no
	// warehouse; a host xf can't read comes from the profile or the
	// environment.
	s = Setup{Host: "{{ env_var('X') }}", HTTPPath: "/sql/protocolv1/o/1/0123-abc", Getenv: getenv, ConfigFile: filepath.Join(dir, "none")}
	env["DATABRICKS_HOST"] = "dbc-env.cloud.databricks.com"
	if c := Resolve(s); c.Warehouse != "" || c.Host != "https://dbc-env.cloud.databricks.com" || c.Profile != "" {
		t.Errorf("connection = %+v", c)
	}
}

func TestCLIFailureIsErrAuth(t *testing.T) {
	c := Resolve(Setup{Host: "dbc-1", Getenv: func(string) string { return "" }, ConfigFile: "-",
		CLI: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("not logged in") }})
	if _, err := c.Token(context.Background()); !errors.Is(err, warehouse.ErrAuth) || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("err = %v", err)
	}
	if c.TokenFrom != "databricks auth token --host https://dbc-1" {
		t.Errorf("token from %q", c.TokenFrom)
	}
}
