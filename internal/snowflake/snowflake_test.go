package snowflake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

func strPtr(s string) *string { return &s }
func int64Ptr(n int64) *int64 { return &n }

func fakeServer(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fn))
	t.Cleanup(srv.Close)
	c := New(Config{
		Account:   "acme-prod",
		Host:      srv.URL,
		Warehouse: "COMPUTE_WH",
		Database:  "ANALYTICS",
		Schema:    "PUBLIC",
		Role:      "TRANSFORMER",
		Token:     staticToken("pat-secret", TokenPAT),
		HTTP:      srv.Client(),
	})
	c.poll = time.Millisecond
	return c, srv
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestAccountHostAndSnowsightURL(t *testing.T) {
	if got := AccountHost("acme_corp-prod"); got != "https://acme-corp-prod.snowflakecomputing.com" {
		t.Fatalf("AccountHost = %q", got)
	}
	if got := AccountHost("xy12345.us-east-1.aws"); got != "https://xy12345.us-east-1.aws.snowflakecomputing.com" {
		t.Fatalf("AccountHost locator = %q", got)
	}
	if got := SnowsightURL("acme-prod", "shop", "marts", "fct_orders"); got != "https://app.snowflake.com/acme/prod/#/data/databases/SHOP/schemas/MARTS/table/FCT_ORDERS" {
		t.Fatalf("SnowsightURL = %q", got)
	}
	if got := SnowsightURL("xy12345.us-east-1", "shop", "marts", "fct_orders"); got != "" {
		t.Fatalf("SnowsightURL for legacy locator = %q, want empty", got)
	}
}

func TestCheckAndLineOffset(t *testing.T) {
	var seenReq map[string]any
	var seenType string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenType = r.Header.Get("X-Snowflake-Authorization-Token-Type")
		_ = json.NewDecoder(r.Body).Decode(&seenReq)
		sql, _ := seenReq["statement"].(string)
		if strings.Contains(sql, "bad_col") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":     "000904",
				"sqlState": "42000",
				"message":  "SQL compilation error: error line 3 at position 7 invalid identifier 'BAD_COL'",
			})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Handle: "h-1",
			Meta: &resultSet{
				RowType: []rowType{
					{Name: "ORDER_ID", Type: "fixed", Precision: int64Ptr(38), Scale: int64Ptr(0), Nullable: false},
					{Name: "STATUS", Type: "text", Length: int64Ptr(16777216), Nullable: true},
					{Name: "AMOUNT", Type: "real", Nullable: true},
					{Name: "CREATED_AT", Type: "timestamp_ntz", Scale: int64Ptr(9), Nullable: true},
				},
			},
		})
	})

	res, err := c.Check(context.Background(), "SHOP", "", "SELECT order_id, status, amount, created_at FROM orders;")
	if err != nil {
		t.Fatal(err)
	}
	if seenType != string(TokenPAT) {
		t.Fatalf("token type header = %q, want %q", seenType, TokenPAT)
	}
	if seenReq["warehouse"] != "COMPUTE_WH" || seenReq["database"] != "SHOP" || seenReq["role"] != "TRANSFORMER" {
		t.Fatalf("request body = %#v", seenReq)
	}
	if len(res.Schema) != 4 {
		t.Fatalf("schema len = %d, want 4", len(res.Schema))
	}
	if res.Schema[0].Name != "ORDER_ID" || res.Schema[0].Type != "NUMBER(38,0)" || !res.Schema[0].Required {
		t.Fatalf("col[0] = %+v", res.Schema[0])
	}
	if res.Schema[1].Type != "VARCHAR(16777216)" || res.Schema[2].Type != "FLOAT" || res.Schema[3].Type != "TIMESTAMP_NTZ(9)" {
		t.Fatalf("schema = %+v", res.Schema)
	}

	_, err = c.Check(context.Background(), "SHOP", "", "SELECT\n  bad_col\nFROM orders")
	var apiErr *warehouse.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *warehouse.APIError", err)
	}
	if !strings.Contains(apiErr.Message, "line 2 at position 7") {
		t.Fatalf("line offset not adjusted: %q", apiErr.Message)
	}
}

func TestCatalogMetadataWakesNoWarehouse(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, hasWH := req["warehouse"]; hasWH {
			t.Errorf("metadata query sent warehouse=%v: %s", req["warehouse"], req["statement"])
		}
		sql, _ := req["statement"].(string)
		switch {
		case strings.HasPrefix(sql, "SHOW WAREHOUSES LIKE"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}, {Name: "state"}, {Name: "size"}, {Name: "type"}}},
				Data: [][]*string{{strPtr("COMPUTE_WH"), strPtr("SUSPENDED"), strPtr("X-Small"), strPtr("STANDARD")}},
			})
		case sql == "SHOW WAREHOUSES":
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}, {Name: "state"}, {Name: "size"}, {Name: "type"}}},
				Data: [][]*string{
					{strPtr("COMPUTE_WH"), strPtr("SUSPENDED"), strPtr("X-Small"), strPtr("STANDARD")},
					{strPtr("BI_WH"), strPtr("STARTED"), strPtr("Medium"), strPtr("STANDARD")},
				},
			})
		case sql == "SHOW DATABASES":
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}}},
				Data: [][]*string{{strPtr("ANALYTICS")}, {strPtr("SNOWFLAKE_SAMPLE_DATA")}},
			})
		case strings.HasPrefix(sql, "SHOW SCHEMAS"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}}},
				Data: [][]*string{{strPtr("MARTS")}, {strPtr("STAGING")}},
			})
		case strings.HasPrefix(sql, "SHOW OBJECTS LIKE"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}, {Name: "kind"}, {Name: "rows"}, {Name: "bytes"}, {Name: "comment"}, {Name: "cluster_by"}}},
				Data: [][]*string{{strPtr("FCT_ORDERS"), strPtr("TABLE"), strPtr("4200"), strPtr("81920"), strPtr("Orders fact"), strPtr("LINEAR(ORDER_DATE)")}},
			})
		case strings.HasPrefix(sql, "SHOW OBJECTS IN SCHEMA"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}, {Name: "kind"}}},
				Data: [][]*string{{strPtr("FCT_ORDERS"), strPtr("TABLE")}, {strPtr("V_ORDERS"), strPtr("VIEW")}},
			})
		case strings.Contains(sql, "MISSING"):
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":     "002003",
				"sqlState": "42S02",
				"message":  "Table 'ANALYTICS.MARTS.MISSING' does not exist or not authorized.",
			})
		case strings.HasPrefix(sql, "DESCRIBE TABLE"):
			if strings.Contains(sql, `"fct_orders"`) || strings.Contains(sql, `"xf_example"`) {
				t.Errorf("plain lowercase dbt identifiers must stay unquoted, got: %s", sql)
			}
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "name"}, {Name: "type"}, {Name: "kind"}, {Name: "null?"}, {Name: "comment"}}},
				Data: [][]*string{
					{strPtr("ORDER_ID"), strPtr("NUMBER(38,0)"), strPtr("COLUMN"), strPtr("N"), strPtr("PK")},
					{strPtr("STATUS"), strPtr("VARCHAR(16777216)"), strPtr("COLUMN"), strPtr("Y"), strPtr("")},
				},
			})
		}
	})

	ctx := context.Background()
	up, err := c.Running(ctx)
	if err != nil || up {
		t.Fatalf("Running = (%v, %v), want (false, nil)", up, err)
	}
	whs, err := c.Warehouses(ctx)
	if err != nil || len(whs) != 2 || whs[1].Name != "BI_WH" || whs[1].State != "STARTED" {
		t.Fatalf("Warehouses = %+v, err=%v", whs, err)
	}
	dbs, err := c.ListProjects(ctx)
	if err != nil || len(dbs) != 2 {
		t.Fatalf("ListProjects = %v, err=%v", dbs, err)
	}
	schs, err := c.ListDatasets(ctx, "analytics")
	if err != nil || len(schs) != 2 {
		t.Fatalf("ListDatasets = %v, err=%v", schs, err)
	}
	tbls, err := c.ListTables(ctx, "analytics", "xf_example")
	if err != nil || len(tbls) != 2 || tbls[1].Type != "VIEW" {
		t.Fatalf("ListTables = %+v, err=%v", tbls, err)
	}
	// Lowercase dbt names work unquoted (Snowflake folds them to uppercase).
	desc, err := c.Describe(ctx, "analytics", "xf_example", "fct_orders")
	if err != nil || desc.NumRows != 4200 || desc.NumBytes != 81920 || len(desc.Schema) != 2 || !desc.Schema[0].Required || len(desc.ClusterFields) != 1 {
		t.Fatalf("Describe = %+v, err=%v", desc, err)
	}
	_, err = c.Table(ctx, "ANALYTICS", "MARTS", "MISSING")
	if !warehouse.IsNotFound(err) {
		t.Fatalf("Table missing err = %v, want IsNotFound", err)
	}
}

func TestSQLIdentQuoting(t *testing.T) {
	cases := map[string]string{
		"fct_orders": "fct_orders",
		"ANALYTICS":  "ANALYTICS",
		"tbl$1":      "tbl$1",
		"order":      `"order"`,
		"select":     `"select"`,
		"my-schema":  `"my-schema"`,
		"a b":        `"a b"`,
		"123abc":     `"123abc"`,
		`a"b`:        `"a""b"`,
	}
	for in, want := range cases {
		if got := sqlIdent(in); got != want {
			t.Errorf("sqlIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := relationSQL("analytics", "xf_example", "fct_orders"); got != "analytics.xf_example.fct_orders" {
		t.Errorf("relationSQL(lowercase) = %q", got)
	}
	if got := relationSQL("my-db", "order", "fct_orders"); got != `"my-db"."order".fct_orders` {
		t.Errorf("relationSQL(special) = %q", got)
	}
}

func TestPollingCancelOnContextDone(t *testing.T) {
	cancelled := false
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel") {
			cancelled = true
			writeJSONResp(w, http.StatusOK, map[string]any{"code": "090001"})
			return
		}
		writeJSONResp(w, http.StatusAccepted, statementResp{Handle: "poll-99"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Check(ctx, "SHOP", "", "SELECT 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if !cancelled {
		t.Fatal("expected cancel call for poll-99 on context done")
	}
}

func TestAdjustLineOffsetMultipleLocations(t *testing.T) {
	msg := "SQL compilation error: error line 4 at position 2 near line 10 at position 15"
	got := adjustLineOffset(msg, 1)
	want := "SQL compilation error: error line 3 at position 2 near line 9 at position 15"
	if got != want {
		t.Fatalf("adjustLineOffset = %q, want %q", got, want)
	}
}

func TestAuth401IsErrAuth(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, http.StatusUnauthorized, map[string]any{"message": "JWT token is invalid"})
	})
	_, err := c.ListProjects(context.Background())
	if !errors.Is(err, ErrAuth) || !errors.Is(err, warehouse.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth / warehouse.ErrAuth", err)
	}
}

func TestResolveConnectionsTOML(t *testing.T) {
	dir := t.TempDir()
	connFile := filepath.Join(dir, "connections.toml")
	cfgFile := filepath.Join(dir, "config.toml")
	err := os.WriteFile(connFile, []byte(`
[dev]
account = "acme-dev"
user = "alice"
warehouse = "DEV_WH"
database = "SHOP_DEV"
token = "pat-dev"

[prod]
account = "acme-prod"
user = "svc_ci"
warehouse = "PROD_WH"
role = "CI_ROLE"
token = "pat-prod"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(cfgFile, []byte(`default_connection_name = "dev"`+"\n"), 0o644)

	noEnv := func(string) string { return "" }
	list := ListConnections(connFile, noEnv)
	if len(list) != 2 || list[0].Name != "dev" || list[1].Account != "acme-prod" {
		t.Fatalf("ListConnections = %+v", list)
	}

	// Matching dbt target account picks [prod].
	conn := Resolve(Setup{Account: "acme-prod", ConnectionsFile: connFile, Getenv: noEnv})
	if conn.ConnectionName != "prod" || conn.Warehouse != "PROD_WH" || conn.Role != "CI_ROLE" {
		t.Fatalf("Resolve(account match) = %+v", conn)
	}
	tok, err := conn.Token(context.Background())
	if err != nil || tok.Value != "pat-prod" || tok.Type != TokenPAT {
		t.Fatalf("Token = %+v, err=%v", tok, err)
	}

	// Unspecified account uses default_connection_name from config.toml.
	conn2 := Resolve(Setup{ConnectionsFile: connFile, Getenv: noEnv})
	if conn2.ConnectionName != "dev" || conn2.Account != "acme-dev" || conn2.Database != "SHOP_DEV" {
		t.Fatalf("Resolve(default) = %+v", conn2)
	}

	// $SNOWFLAKE_TOKEN overrides connections.toml.
	conn3 := Resolve(Setup{
		ConnectionsFile: connFile,
		Getenv: func(k string) string {
			if k == "SNOWFLAKE_TOKEN" {
				return "env-pat"
			}
			return ""
		},
	})
	tok3, _ := conn3.Token(context.Background())
	if tok3.Value != "env-pat" || conn3.TokenFrom != "$SNOWFLAKE_TOKEN" {
		t.Fatalf("env override = %+v, from=%q", tok3, conn3.TokenFrom)
	}

	// Target naming an unknown account must NOT inherit default section's credentials for a different account.
	conn4 := Resolve(Setup{Account: "other-org", ConnectionsFile: connFile, Getenv: noEnv})
	if conn4.ConnectionName != "" || conn4.Token != nil || conn4.Account != "other-org" {
		t.Fatalf("cross-account leak in Resolve: %+v", conn4)
	}

	// Bare [connections] header is ignored and [connections.<name>] in config.toml is read.
	_ = os.WriteFile(cfgFile, []byte("default_connection_name = \"dev\"\n[connections]\n[connections.staging]\naccount = \"acme-staging\"\ntoken = \"pat-stg\"\n[cli.logs]\nlevel = \"info\"\n"), 0o644)
	list2 := ListConnections(connFile, noEnv)
	if len(list2) != 3 || list2[2].Name != "staging" || list2[2].Account != "acme-staging" {
		t.Fatalf("ListConnections with config.toml = %+v", list2)
	}
}
