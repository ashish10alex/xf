package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/bqfuncs"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

func TestScratchHeader(t *testing.T) {
	m := scratchMeta{From: "mart.orders", Project: "acme", Location: "EU"}
	got, rest := parseScratch(m.header() + "SELECT 1\n")
	if got != m || rest != "SELECT 1\n" {
		t.Errorf("parse = %+v %q, want %+v and the SQL", got, rest, m)
	}
	// A file without the header, as an edit in another editor can leave.
	if got, rest := parseScratch("SELECT 2"); got != (scratchMeta{}) || rest != "SELECT 2" {
		t.Errorf("headerless = %+v %q", got, rest)
	}
	if got, _ := parseScratch(scratchMeta{Project: "p"}.header()); got.Project != "p" || got.From != "" {
		t.Errorf("no model = %+v", got)
	}
}

func TestSelectColumns(t *testing.T) {
	got := selectColumns("p", "d", "t", []bigquery.Field{{Name: "id"}, {Name: "order"}, {Name: "my col"}, {Name: "amount"}})
	want := "SELECT\n  id,\n  `order`,\n  `my col`,\n  amount\nFROM `p.d.t`\n"
	if got != want {
		t.Errorf("select =\n%s\nwant\n%s", got, want)
	}
}

func TestScratchIDsAreSafe(t *testing.T) {
	for _, id := range []string{"../x", "scratch_../../etc", "scratch_", "scratch_ABC123", "scratch_abc123/../x", ""} {
		if scratchIDRe.MatchString(id) {
			t.Errorf("%q passes as a scratch id", id)
		}
	}
	if !scratchIDRe.MatchString(newScratchID()) {
		t.Errorf("made id %q fails its own check", newScratchID())
	}
}

// fakeScratchBQ answers a dry run, a query job and its rows.
func fakeScratchBQ(t *testing.T) (*bigquery.Client, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/datasets"):
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"datasetId":"sales"}},{"datasetReference":{"datasetId":"raw"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/tables"):
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"tableId":"orders"},"type":"TABLE"},{"tableReference":{"tableId":"v_orders"},"type":"VIEW"}]}`))
		case strings.Contains(r.URL.Path, "/tables/gone"):
			http.Error(w, `{"error":{"message":"Not found: Table"}}`, 404)
		case strings.Contains(r.URL.Path, "/tables/"):
			bytes := "100"
			if strings.HasSuffix(r.URL.Path, "/bigres") {
				bytes = "60129542144"
			}
			_, _ = w.Write([]byte(`{"type":"TABLE","description":"Orders, one row each","location":"EU","numRows":"12","numBytes":"` + bytes + `","creationTime":"1704164645000","lastModifiedTime":"1704164645000",
				"schema":{"fields":[{"name":"id","type":"INTEGER","description":"the id"},{"name":"tags","type":"STRING","mode":"REPEATED"},{"name":"customer","type":"RECORD","fields":[{"name":"name","type":"STRING"}]}]},
				"timePartitioning":{"field":"day","type":"DAY"},"clustering":{"fields":["id"]}}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs"):
			var req struct {
				Configuration struct {
					DryRun bool
					Query  struct {
						Query           string
						QueryParameters []struct {
							Name           string
							ParameterValue struct{ Value string }
						}
					}
				}
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &req)
			if !req.Configuration.DryRun {
				calls = append(calls, "JOB "+string(raw))
			}
			if req.Configuration.DryRun {
				bytes, stmt := "1000", "SELECT"
				switch {
				case strings.Contains(req.Configuration.Query.Query, "BIG"):
					bytes = "60129542144" // 56 GiB
				case strings.HasPrefix(req.Configuration.Query.Query, "DELETE"):
					stmt = "DELETE"
				}
				_, _ = w.Write([]byte(`{"statistics":{"totalBytesProcessed":"` + bytes + `","query":{"statementType":"` + stmt + `","totalBytesProcessedAccuracy":"PRECISE","schema":{"fields":[{"name":"a","type":"INTEGER"}]}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"jobReference":{"location":"EU"}}`))
		case strings.Contains(r.URL.Path, "/queries/expired"):
			http.Error(w, `{"error":{"code":404,"message":"Not found: Table p:_anon.anon_x was not found in location EU"}}`, 404)
		case strings.Contains(r.URL.Path, "/queries/"):
			_, _ = w.Write([]byte(`{"jobComplete":true,"totalRows":"2","schema":{"fields":[{"name":"a","type":"INTEGER"},{"name":"b","type":"STRING"}]},"rows":[{"f":[{"v":"1"},{"v":"x,y"}]},{"f":[{"v":"2"},{"v":null}]}]}`))
		case strings.Contains(r.URL.Path, "/jobs/"):
			table := "anon"
			if strings.Contains(r.URL.Path, "bigjob") {
				table = "bigres"
			}
			_, _ = w.Write([]byte(`{"status":{"state":"DONE"},"configuration":{"query":{"destinationTable":{"projectId":"p","datasetId":"_anon","tableId":"` + table + `"}}},"statistics":{"query":{"statementType":"SELECT","totalBytesBilled":"10485760"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return bigquery.NewClientWithHTTP(srv.Client(), srv.URL, "quota-proj"), &calls
}

func newScratchTest(t *testing.T, deps frontend.Deps, bq *bigquery.Client) (string, *httptest.Server, *http.Client) {
	t.Helper()
	root := cmpOr(deps.Root, t.TempDir())
	deps.Root = root
	if deps.Engine == nil {
		deps.Engine = newFakeEngine(testGraph())
	}
	if deps.DryRun == nil {
		deps.DryRun = func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
			return bq.DryRun(ctx, project, location, sql)
		}
	}
	_, ts, c := newTestProject(t, &Project{Deps: deps, BigQuery: func(context.Context) (*bigquery.Client, error) { return bq, nil }, ScratchWarnGB: 1, ScratchConfirmGB: 50})
	return root, ts, c
}

func TestScratchCreateEditAndDelete(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	root, ts, c := newScratchTest(t, frontend.Deps{
		Table: func(ctx context.Context, p, d, tb string) (*bigquery.Table, error) {
			return &bigquery.Table{Location: "EU", Schema: []bigquery.Field{{Name: "id"}, {Name: "amount"}}}, nil
		},
	}, bq)

	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"id": "p.mart.orders", "form": "columns"}, &f)
	if f.SQL != "SELECT\n  id,\n  amount\nFROM `p.mart.orders`\n" || f.Project != "p" || f.Location != "EU" || f.From != "mart.orders" {
		t.Errorf("columns scratch = %+v", f)
	}
	path := filepath.Join(root, ".xf", "tmp", f.ID+".sql")
	if data, err := os.ReadFile(path); err != nil || !strings.HasPrefix(string(data), "-- xf scratch: from mart.orders") {
		t.Errorf("file = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".xf", "tmp", ".gitignore")); err != nil || !strings.Contains(string(data), "*") {
		t.Errorf(".xf/tmp/.gitignore = %q, %v: scratches would reach git", data, err)
	}

	var compiled scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"id": "p.mart.orders", "form": "compiled"}, &compiled)
	if compiled.SQL != "SELECT id\nFROM `p.raw.orders`\n" {
		t.Errorf("compiled scratch SQL = %q", compiled.SQL)
	}

	// Saving keeps the header.
	postJSON(t, c, ts.URL+"/api/scratch/save", map[string]any{"id": f.ID, "sql": "SELECT 42"}, nil)
	var again scratchFile
	getJSON(t, c, ts.URL+"/api/scratch/file?id="+f.ID, &again)
	if again.SQL != "SELECT 42" || again.Project != "p" || again.From != "mart.orders" {
		t.Errorf("after save = %+v", again)
	}

	var list []scratchView
	getJSON(t, c, ts.URL+"/api/scratch", &list)
	if len(list) != 2 {
		t.Fatalf("list = %+v, want both scratches", list)
	}

	postJSON(t, c, ts.URL+"/api/scratch/delete", map[string]any{"id": f.ID}, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("deleted scratch is still there: %v", err)
	}

	// A path is no scratch id.
	res, err := c.Post(ts.URL+"/api/scratch/delete", "application/json", strings.NewReader(`{"id":"../config"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("deleting ../config succeeded")
	}
}

func TestHasOutputTable(t *testing.T) {
	tgt := project.Target{Database: "p", Schema: "d", Name: "t"}
	for _, tt := range []struct {
		name string
		a    project.Action
		want bool
	}{
		{"table", project.Action{Kind: project.KindTable, Target: tgt}, true},
		{"operation with output", project.Action{Kind: project.KindOperation, HasOutput: true, Target: tgt}, true},
		{"operation without output", project.Action{Kind: project.KindOperation, Target: tgt}, false},
	} {
		if got := hasOutputTable(&tt.a); got != tt.want {
			t.Errorf("%s: hasOutputTable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// An operation that declares an output opens in BigQuery with u, like a
// table; one without an output has nothing to open.
func TestRefOfConsoleURL(t *testing.T) {
	tgt := project.Target{Database: "p", Schema: "d", Name: "t"}
	for _, tt := range []struct {
		name string
		a    project.Action
		want string
	}{
		{"table", project.Action{Kind: project.KindTable, Target: tgt}, tgt.ConsoleURL()},
		{"operation with output", project.Action{Kind: project.KindOperation, HasOutput: true, Target: tgt}, tgt.ConsoleURL()},
		{"operation without output", project.Action{Kind: project.KindOperation, Target: tgt}, ""},
	} {
		if got := refOf(&tt.a).ConsoleURL; got != tt.want {
			t.Errorf("%s: consoleUrl = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestScratchColumnsNeedTheTable(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{
		Table: func(ctx context.Context, p, d, tb string) (*bigquery.Table, error) {
			return nil, &bigquery.APIError{Status: 404, Message: "Not found"}
		},
	}, bq)
	res, err := c.Post(ts.URL+"/api/scratch/create", "application/json", strings.NewReader(`{"id":"p.mart.orders","form":"columns"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "does not exist yet") {
		t.Errorf("columns of an unbuilt table: %s %s", res.Status, body)
	}
}

func TestScratchDryRunGuards(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"id": "p.mart.orders", "form": "compiled"}, &f)
	dry := func(sql string) scratchDryView {
		var v scratchDryView
		postJSON(t, c, ts.URL+"/api/scratch/dryrun", map[string]any{"id": f.ID, "sql": sql}, &v)
		return v
	}
	small := dry("SELECT 1")
	if !small.OK || small.Bytes != 1000 || small.Writes || small.WarnBytes != 1<<30 || small.ConfirmBytes != 50<<30 {
		t.Errorf("small = %+v, want 1000 bytes and the configured thresholds", small)
	}
	if big := dry("SELECT BIG"); big.Bytes < big.ConfirmBytes || big.Cost != "$0.342" {
		t.Errorf("big = %+v, want a size over the confirm threshold, costing $0.342", big)
	}
	if del := dry("DELETE FROM t WHERE TRUE"); !del.Writes || del.StatementType != "DELETE" {
		t.Errorf("delete = %+v, want a write", del)
	}
	if empty := dry("  "); empty.OK || empty.Error == "" {
		t.Errorf("empty = %+v", empty)
	}
}

func TestScratchRunAndExport(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"id": "p.mart.orders", "form": "compiled"}, &f)

	var ref bigquery.JobRef
	postJSON(t, c, ts.URL+"/api/scratch/run", map[string]any{"id": f.ID, "sql": "SELECT 1"}, &ref)
	if ref.Project != "p" || ref.Location != "EU" && ref.Location != "" || !strings.HasPrefix(ref.ID, "xf_scratch_") {
		t.Errorf("job = %+v", ref)
	}
	q := url.Values{"project": {ref.Project}, "location": {ref.Location}, "id": {ref.ID}}.Encode()

	var job jobView
	getJSON(t, c, ts.URL+"/api/scratch/job?"+q, &job)
	if job.State != "DONE" || job.BytesBilled != 10485760 || job.StatementType != "SELECT" || job.Cost != "$0.000" {
		t.Errorf("job view = %+v, cost %q", job.QueryStatus, job.Cost)
	}
	var page bigquery.Page
	getJSON(t, c, ts.URL+"/api/scratch/rows?"+q, &page)
	if page.Total != 2 || len(page.Rows) != 2 || page.Rows[0][1] != "x,y" || page.Rows[1][1] != nil {
		t.Errorf("rows = %+v", page)
	}

	res, err := c.Get(ts.URL + "/api/scratch/csv?" + q + "&name=my%22file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "a,b\n1,\"x,y\"\n2,\n" {
		t.Errorf("csv = %s %q", res.Status, body)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="myfile.csv"`) || strings.Contains(cd, `my"`) {
		t.Errorf("Content-Disposition = %q, want the name without its quote", cd)
	}
}

func TestScratchMetaIsCachedAndCleared(t *testing.T) {
	bq, calls := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	count := func(suffix string) int {
		n := 0
		for _, c := range *calls {
			if strings.HasSuffix(c, suffix) {
				n++
			}
		}
		return n
	}
	var ds []string
	getJSON(t, c, ts.URL+"/api/scratch/meta/datasets?project=p", &ds)
	getJSON(t, c, ts.URL+"/api/scratch/meta/datasets?project=p", &ds)
	if len(ds) != 2 || ds[0] != "sales" || count("/datasets") != 1 {
		t.Errorf("datasets = %v after %d calls, want 2 and one call", ds, count("/datasets"))
	}
	var ts2 []bigquery.TableListing
	getJSON(t, c, ts.URL+"/api/scratch/meta/tables?project=p&dataset=sales", &ts2)
	if len(ts2) != 2 || ts2[1].Type != "VIEW" {
		t.Errorf("tables = %+v", ts2)
	}
	var card tableCard
	getJSON(t, c, ts.URL+"/api/scratch/meta/table?project=p&dataset=mart&table=orders", &card)
	getJSON(t, c, ts.URL+"/api/scratch/meta/table?project=p&dataset=mart&table=orders", &card)
	d := card.Detail
	if d == nil || d.Description != "Orders, one row each" || d.NumRows != 12 || d.NumBytes != 100 || d.PartitionField != "day" || d.PartitionType != "DAY" || len(d.ClusterFields) != 1 || len(d.Schema) != 3 {
		t.Errorf("card = %+v", card)
	}
	// p.mart.orders is the test graph's table: its model rides along.
	if card.Model == nil || card.Model.Kind == "" || card.Model.Name != "mart.orders" {
		t.Errorf("model = %+v, want the project's action for the table", card.Model)
	}
	if n := count("/tables/orders"); n != 1 {
		t.Errorf("table fetched %d times, want once", n)
	}
	// A hover asks fresh: the table is fetched again, kept or not.
	getJSON(t, c, ts.URL+"/api/scratch/meta/table?project=p&dataset=mart&table=orders&fresh=1", &card)
	if n := count("/tables/orders"); n != 2 {
		t.Errorf("table fetched %d times after a fresh ask, want twice", n)
	}
	postJSON(t, c, ts.URL+"/api/scratch/meta/refresh", nil, nil)
	getJSON(t, c, ts.URL+"/api/scratch/meta/table?project=p&dataset=mart&table=orders", &card)
	if n := count("/tables/orders"); n != 3 {
		t.Errorf("table fetched %d times after a refresh, want three times", n)
	}
	// A table that is not there still has its model's card.
	var gone tableCard
	getJSON(t, c, ts.URL+"/api/scratch/meta/table?project=p&dataset=mart&table=gone", &gone)
	if gone.Detail != nil || !strings.Contains(gone.Error, "doesn't exist") {
		t.Errorf("missing table card = %+v", gone)
	}
	var models []modelRef
	getJSON(t, c, ts.URL+"/api/scratch/meta/models", &models)
	names := []string{}
	for _, m := range models {
		names = append(names, m.Dataset+"."+m.Name)
	}
	if strings.Join(names, " ") != "mart.customers mart.orders raw.orders" {
		t.Errorf("models = %v", names)
	}
}

func TestScratchFunctions(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var fns []bqfuncs.Func
	getJSON(t, c, ts.URL+"/api/scratch/functions", &fns)
	var trunc *bqfuncs.Func
	for i := range fns {
		if fns[i].Name == "DATE_TRUNC" {
			trunc = &fns[i]
		}
	}
	if len(fns) < 200 || trunc == nil || trunc.Snippet == "" || len(trunc.Signatures) == 0 {
		t.Fatalf("%d functions, DATE_TRUNC = %+v", len(fns), trunc)
	}
	// A signature of no params is an empty list, which the page reads as one.
	var raw []map[string]any
	getJSON(t, c, ts.URL+"/api/scratch/functions", &raw)
	for _, f := range raw {
		sigs, _ := f["signatures"].([]any)
		for _, s := range sigs {
			if _, ok := s.(map[string]any)["params"].([]any); !ok {
				t.Errorf("%s: params is %v, not a list", f["name"], s.(map[string]any)["params"])
			}
		}
	}
}

func TestClearsMeta(t *testing.T) {
	for st, want := range map[string]bool{"CREATE_TABLE_AS_SELECT": true, "ALTER_TABLE": true, "DROP_VIEW": true, "SCRIPT": true, "SELECT": false, "INSERT": false, "": false} {
		if clearsMeta(st) != want {
			t.Errorf("clearsMeta(%q) = %v", st, !want)
		}
	}
}

func TestFilterSQL(t *testing.T) {
	schema := []bigquery.Field{{Name: "country", Type: "STRING"}, {Name: "tags", Type: "STRING", Mode: "REPEATED"}, {Name: "blob", Type: "BYTES"}, {Name: "s", Type: "RECORD"}, {Name: "n", Type: "INTEGER"}}
	sql, params, err := filterSQL(bigquery.TableRef{Project: "p", Dataset: "_d", Table: "t"}, schema,
		[]filterTerm{{"country", "uk"}, {"tags", "x"}, {"blob", "y"}, {"s", "z"}, {"n", ""}, {"n", "5"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM `p._d.t` WHERE STRPOS(LOWER(SAFE_CAST(`country` AS STRING)), LOWER(@f0)) > 0 AND STRPOS(LOWER(TO_JSON_STRING(`tags`)), LOWER(@f1)) > 0 AND STRPOS(LOWER(TO_BASE64(`blob`)), LOWER(@f2)) > 0 AND STRPOS(LOWER(TO_JSON_STRING(`s`)), LOWER(@f3)) > 0 AND STRPOS(LOWER(SAFE_CAST(`n` AS STRING)), LOWER(@f4)) > 0"
	if sql != want {
		t.Errorf("sql =\n%s\nwant\n%s", sql, want)
	}
	if params["f0"] != "uk" || params["f4"] != "5" || len(params) != 5 {
		t.Errorf("params = %v", params)
	}
	// Text is never SQL, and a column is only one the result has.
	if _, _, err := filterSQL(bigquery.TableRef{}, schema, []filterTerm{{"nope", "x"}}, nil); err == nil {
		t.Error("a filter on a missing column passed")
	}
	if _, _, err := filterSQL(bigquery.TableRef{}, schema, []filterTerm{{"country", ""}}, nil); err == nil {
		t.Error("no filter text passed")
	}
	sql, params, _ = filterSQL(bigquery.TableRef{Project: "p", Dataset: "d", Table: "t"}, schema, []filterTerm{{"country", "'; DROP TABLE x; --"}}, nil)
	if strings.Contains(sql, "DROP") || params["f0"] != "'; DROP TABLE x; --" {
		t.Errorf("filter text reached the SQL: %s", sql)
	}
}

func TestFilterSQLSorts(t *testing.T) {
	schema := []bigquery.Field{{Name: "country", Type: "STRING"}, {Name: "tags", Type: "STRING", Mode: "REPEATED"}, {Name: "s", Type: "RECORD"}, {Name: "b", Type: "BYTES"}}
	tbl := bigquery.TableRef{Project: "p", Dataset: "d", Table: "t"}
	got, params, err := filterSQL(tbl, schema, nil, []sortTerm{{"country", true}})
	if err != nil || got != "SELECT * FROM `p.d.t` ORDER BY `country` DESC" || len(params) != 0 {
		t.Errorf("sort only = %q %v %v", got, params, err)
	}
	got, _, err = filterSQL(tbl, schema, []filterTerm{{"country", "uk"}}, []sortTerm{{"b", false}})
	if err != nil || !strings.HasSuffix(got, "> 0 ORDER BY `b`") || !strings.Contains(got, "WHERE") {
		t.Errorf("filter and sort = %q %v", got, err)
	}
	for _, col := range []string{"tags", "s", "nope"} {
		if _, _, err := filterSQL(tbl, schema, nil, []sortTerm{{col, false}}); err == nil {
			t.Errorf("sorting by %s passed", col)
		}
	}
	if _, _, err := filterSQL(tbl, schema, nil, nil); err == nil {
		t.Error("nothing to do passed")
	}
}

func TestScratchSearchAllRows(t *testing.T) {
	bq, calls := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	post := func(job string, confirm bool) filterView {
		var v filterView
		postJSON(t, c, ts.URL+"/api/scratch/filter", filterRequest{Project: "p", Location: "EU", ID: job, Confirm: confirm, Filters: []filterTerm{{"id", "5"}}}, &v)
		return v
	}
	v := post("job1", false)
	if v.NeedsConfirm || v.Job == nil || v.Bytes != 100 {
		t.Fatalf("small result = %+v, want it to run", v)
	}
	var sent string
	for _, c := range *calls {
		if strings.HasPrefix(c, "JOB ") {
			sent = c
		}
	}
	if !strings.Contains(sent, "FROM `p._anon.anon`") || !strings.Contains(sent, `"parameterMode":"NAMED"`) || !strings.Contains(sent, `"value":"5"`) || !strings.Contains(sent, `"xf-scratch":"true"`) {
		t.Errorf("job sent = %s", sent)
	}
	// A table over the confirm size asks first, and runs once told to.
	big := post("bigjob1", false)
	if !big.NeedsConfirm || big.Job != nil {
		t.Errorf("big result = %+v, want a question", big)
	}
	if big = post("bigjob1", true); big.NeedsConfirm || big.Job == nil {
		t.Errorf("confirmed = %+v, want it to run", big)
	}
	res, err := c.Post(ts.URL+"/api/scratch/filter", "application/json", strings.NewReader(`{"project":"p","id":"j","filters":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("no filters: %s, want a bad request", res.Status)
	}
}

func TestEmptyScratchTargetAndDefaults(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	root, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var def scratchDefaultsView
	getJSON(t, c, ts.URL+"/api/scratch/defaults", &def)
	if def.Project != "quota-proj" || def.Standalone {
		t.Errorf("defaults = %+v, want the credentials' quota project", def)
	}
	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"form": "empty", "location": "EU"}, &f)
	if f.SQL != "" || f.From != "" || f.Project != "quota-proj" || f.Location != "EU" {
		t.Errorf("empty scratch = %+v", f)
	}
	if _, err := os.Stat(filepath.Join(root, ".xf", "tmp", f.ID+".sql")); err != nil {
		t.Error(err)
	}
	postJSON(t, c, ts.URL+"/api/scratch/save", map[string]any{"id": f.ID, "sql": "SELECT 1"}, nil)
	postJSON(t, c, ts.URL+"/api/scratch/target", map[string]any{"id": f.ID, "project": "other", "location": "US"}, nil)
	var again scratchFile
	getJSON(t, c, ts.URL+"/api/scratch/file?id="+f.ID, &again)
	if again.Project != "other" || again.Location != "US" || again.SQL != "SELECT 1" {
		t.Errorf("after target = %+v, want the new target and the same SQL", again)
	}
	// A scratch not yet in a file dry-runs in the project it is given.
	var d scratchDryView
	postJSON(t, c, ts.URL+"/api/scratch/dryrun", map[string]any{"sql": "SELECT 2", "project": "p", "location": "EU"}, &d)
	if !d.OK {
		t.Errorf("draft dry run = %+v", d)
	}
}

func TestStandaloneScratchFolder(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	dir := filepath.Join(t.TempDir(), "xf", "tmp")
	s, ts, c := newTestProject(t, &Project{
		Deps:       frontend.Deps{Root: filepath.Dir(dir), Engine: newFakeEngine(project.NewGraph(nil)), DryRun: bq.DryRun},
		BigQuery:   func(context.Context) (*bigquery.Client, error) { return bq, nil },
		ScratchDir: dir, ScratchProject: "flag-proj", ScratchLocation: "EU", Standalone: true, StartScratch: true,
	})
	var def scratchDefaultsView
	getJSON(t, c, ts.URL+"/api/scratch/defaults", &def)
	if def.Project != "flag-proj" || def.Location != "EU" || !def.Standalone {
		t.Errorf("defaults = %+v, want the flags", def)
	}
	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"form": "empty"}, &f)
	if _, err := os.Stat(filepath.Join(dir, f.ID+".sql")); err != nil {
		t.Errorf("scratch is not in the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		t.Error("xf's own config folder got a .gitignore")
	}
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if !boot.Scratch.Standalone || !boot.Scratch.Start || !boot.Caps.Scratch {
		t.Errorf("boot scratch = %+v caps %+v", boot.Scratch, boot.Caps)
	}
}
