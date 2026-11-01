package bigquery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestCell(t *testing.T) {
	rec := Field{Type: "RECORD", Fields: []Field{{Name: "a", Type: "INTEGER"}, {Name: "ts", Type: "TIMESTAMP"}, {Name: "xs", Type: "STRING", Mode: "REPEATED"}}}
	tests := []struct {
		name string
		f    Field
		v    string
		want any
	}{
		{"null", Field{Type: "STRING"}, `null`, nil},
		{"int64 keeps every digit", Field{Type: "INTEGER"}, `"9007199254740993"`, "9007199254740993"},
		{"bignumeric", Field{Type: "BIGNUMERIC"}, `"1234567890123456789012345678901234567.12345678901234567890123456789012345678"`, "1234567890123456789012345678901234567.12345678901234567890123456789012345678"},
		{"nan", Field{Type: "FLOAT"}, `"NaN"`, "NaN"},
		{"timestamp micros", Field{Type: "TIMESTAMP"}, `"1704164645123456"`, "2024-01-02 03:04:05.123456 UTC"},
		{"timestamp whole seconds", Field{Type: "TIMESTAMP"}, `"1704164645000000"`, "2024-01-02 03:04:05 UTC"},
		{"pre-epoch timestamp", Field{Type: "TIMESTAMP"}, `"-1"`, "1969-12-31 23:59:59.999999 UTC"},
		{"bytes", Field{Type: "BYTES"}, `"Ynl0ZXMA/w=="`, map[string]any{"b64": "Ynl0ZXMA/w==", "len": 7}},
		{"date and json and geography stay as sent", Field{Type: "JSON"}, `"{\"a\":[1,2]}"`, `{"a":[1,2]}`},
		{"array", Field{Type: "INTEGER", Mode: "REPEATED"}, `[{"v":"1"},{"v":null},{"v":"3"}]`, []any{"1", nil, "3"}},
		{"empty array", Field{Type: "INTEGER", Mode: "REPEATED"}, `[]`, []any{}},
		{"struct with a timestamp and an array", rec, `{"f":[{"v":"7"},{"v":"1704164645000000"},{"v":[{"v":"x"},{"v":"y"}]}]}`,
			[]any{"7", "2024-01-02 03:04:05 UTC", []any{"x", "y"}}},
		{"array of structs", Field{Type: "RECORD", Mode: "REPEATED", Fields: []Field{{Name: "k", Type: "STRING"}}}, `[{"v":{"f":[{"v":"k1"}]}}]`, []any{[]any{"k1"}}},
		{"range as a string", Field{Type: "RANGE", RangeElementType: &RangeElement{Type: "DATE"}}, `"[2024-01-01, UNBOUNDED)"`, "[2024-01-01, UNBOUNDED)"},
		{"range as bounds", Field{Type: "RANGE", RangeElementType: &RangeElement{Type: "TIMESTAMP"}}, `{"start":{"v":"1704164645000000"},"end":{"v":null}}`, []any{"2024-01-02 03:04:05 UTC", nil}},
		{"bare bool", Field{Type: "BOOLEAN"}, `true`, "true"},
	}
	for _, tt := range tests {
		if got := Cell(tt.f, raw(tt.v)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Cell = %#v, want %#v", tt.name, got, tt.want)
		}
	}
}

func TestCellText(t *testing.T) {
	for _, tt := range []struct {
		v    any
		want string
	}{
		{nil, ""}, {"a,b", "a,b"}, {map[string]any{"b64": "AAE=", "len": 2}, "AAE="},
		{[]any{"1", nil, []any{"x"}}, `["1",null,["x"]]`},
	} {
		if got := CellText(tt.v); got != tt.want {
			t.Errorf("CellText(%#v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

// fakeBigQuery serves the few calls a query makes.
func fakeBigQuery(t *testing.T, got *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = append(*got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs"):
			var req struct {
				JobReference  struct{ JobID, Location string }
				Configuration struct {
					Labels map[string]string
					Query  struct{ Query string }
				}
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Configuration.Labels["xf-scratch"] != "true" || req.Configuration.Query.Query != "SELECT 1" || !strings.HasPrefix(req.JobReference.JobID, "xf_scratch_") {
				http.Error(w, `{"error":{"message":"bad request"}}`, 400)
				return
			}
			_, _ = w.Write([]byte(`{"jobReference":{"location":"EU"}}`))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/queries/"):
			if r.URL.Query().Get("pageToken") == "p2" {
				_, _ = w.Write([]byte(`{"jobComplete":true,"totalRows":"3","schema":{"fields":[{"name":"x","type":"INTEGER"}]},"rows":[{"f":[{"v":"3"}]}]}`))
				return
			}
			if r.URL.Query().Get("formatOptions.useInt64Timestamp") != "true" {
				t.Error("timestamps are not asked for as integers")
			}
			_, _ = w.Write([]byte(`{"jobComplete":true,"totalRows":"3","pageToken":"p2","schema":{"fields":[{"name":"x","type":"INTEGER"}]},"rows":[{"f":[{"v":"1"}]},{"f":[{"v":"2"}]}]}`))
		case strings.Contains(r.URL.Path, "/jobs/") && r.URL.Query().Get("parentJobId") == "":
			_, _ = w.Write([]byte(`{"status":{"state":"DONE"},"statistics":{"startTime":"1000","endTime":"2500","numChildJobs":"2","query":{"cacheHit":false,"statementType":"SCRIPT","totalBytesProcessed":"10485760","totalBytesBilled":"20971520"}}}`))
		case r.URL.Query().Get("parentJobId") != "":
			_, _ = w.Write([]byte(`{"jobs":[
				{"jobReference":{"jobId":"child2"},"status":{"state":"DONE"},"statistics":{"query":{"statementType":"SELECT"}},"configuration":{"query":{"query":"SELECT * FROM t"}}},
				{"jobReference":{"jobId":"child1"},"status":{"state":"DONE"},"statistics":{"query":{"statementType":"UPDATE","numDmlAffectedRows":"2"}},"configuration":{"query":{"query":"UPDATE t SET a = 1"}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClientWithHTTP(srv.Client(), srv.URL, "")
}

func TestQueryLifecycle(t *testing.T) {
	var got []string
	c := fakeBigQuery(t, &got)
	ctx := context.Background()
	ref, err := c.StartQuery(ctx, QueryOptions{Project: "p", Location: "", SQL: "SELECT 1", Labels: map[string]string{"xf-scratch": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Location != "EU" || !strings.HasPrefix(ref.ID, "xf_scratch_") {
		t.Errorf("ref = %+v, want the location BigQuery chose", ref)
	}
	st, err := c.QueryJob(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "DONE" || st.StatementType != "SCRIPT" || st.BytesBilled != 20971520 || st.DMLRows != -1 || st.NumChild != 2 || st.Ended.Sub(st.Started).Milliseconds() != 1500 {
		t.Errorf("status = %+v", st)
	}
	kids, err := c.ChildJobs(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 2 || kids[0].ID != "child1" || kids[0].DMLRows != 2 || kids[1].ID != "child2" || !kids[1].HasRows || kids[0].HasRows {
		t.Errorf("children = %+v, want the statements in the order they ran", kids)
	}
	p1, err := c.Rows(ctx, ref, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Total != 3 || p1.NextToken != "p2" || len(p1.Rows) != 2 || p1.Rows[1][0] != "2" || !p1.Complete {
		t.Errorf("page 1 = %+v", p1)
	}
	p2, err := c.Rows(ctx, ref, p1.NextToken, 2)
	if err != nil {
		t.Fatal(err)
	}
	if p2.NextToken != "" || len(p2.Rows) != 1 || p2.Rows[0][0] != "3" {
		t.Errorf("page 2 = %+v", p2)
	}
	if err := c.CancelQuery(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got, "\n"), "POST /bigquery/v2/projects/p/jobs/"+ref.ID+"/cancel?") {
		t.Errorf("calls = %q, want a cancel", got)
	}
}

func TestQueryJobFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"state":"DONE","errorResult":{"reason":"invalidQuery","message":"Unrecognized name: x at [1:8]"}}}`))
	}))
	defer srv.Close()
	st, err := NewClientWithHTTP(srv.Client(), srv.URL, "").QueryJob(context.Background(), JobRef{Project: "p", ID: "j"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Error != "Unrecognized name: x at [1:8]" || st.Cancelled {
		t.Errorf("status = %+v, want the message with its position", st)
	}
}
