package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// queryWorkspace is a workspace that runs one query: PENDING for a poll
// while its SQL warehouse starts, then three rows in two chunks, the first
// in a result file behind a presigned link and the second inline.
type queryWorkspace struct {
	mu        sync.Mutex
	submitted map[string]any
	polls     int
	state     string // the state after the first poll
	cancelled bool
	fileReads int
}

func (q *queryWorkspace) serve(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("POST /api/2.0/sql/statements", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&q.submitted)
		q.mu.Unlock()
		_, _ = io.WriteString(w, `{"statement_id":"q1","status":{"state":"PENDING"}}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/statements/q1", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.polls++
		first, state := q.polls == 1, q.state
		q.mu.Unlock()
		switch {
		case first:
			_, _ = io.WriteString(w, `{"statement_id":"q1","status":{"state":"PENDING"}}`)
		case state == "FAILED":
			_, _ = io.WriteString(w, `{"statement_id":"q1","status":{"state":"FAILED","error":{"error_code":"BAD_REQUEST","message":"[TABLE_OR_VIEW_NOT_FOUND] The table nope cannot be found."}}}`)
		case state != "SUCCEEDED":
			_, _ = io.WriteString(w, `{"statement_id":"q1","status":{"state":"`+state+`"}}`)
		default:
			_, _ = io.WriteString(w, `{"statement_id":"q1","status":{"state":"SUCCEEDED"},
				"manifest":{"format":"JSON_ARRAY","schema":{"columns":[{"name":"id","type_text":"BIGINT"},{"name":"name","type_text":"STRING"}]},
					"total_chunk_count":2,"total_row_count":3},
				"result":{"chunk_index":0,"external_links":[{"external_link":"`+srv.URL+`/files/0","http_headers":{"x-ms-blob-type":"BlockBlob"}}]}}`)
		}
	})
	mux.HandleFunc("GET /api/2.0/sql/statements/q1/result/chunks/{n}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("n") == "0" {
			_, _ = io.WriteString(w, `{"chunk_index":0,"external_links":[{"external_link":"`+srv.URL+`/files/0","http_headers":{"x-ms-blob-type":"BlockBlob"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"chunk_index":1,"data_array":[["3",null]]}`)
	})
	mux.HandleFunc("POST /api/2.0/sql/statements/q1/cancel", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.cancelled = true
		q.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	})
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the workspace's token went to a presigned link")
		}
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
			t.Errorf("the link's headers weren't sent")
		}
		q.mu.Lock()
		q.fileReads++
		q.mu.Unlock()
		_, _ = io.WriteString(w, `[["1","ada"],["2","grace"]]`)
	})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			files.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer dapi-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Config{Host: srv.URL, Warehouse: "wh1", Catalog: "workspace", Token: static("dapi-test"), HTTP: srv.Client()}), srv
}

// A query is submitted without waiting, polled to its end, and its rows
// paged a chunk at a time: from a result file, then inline.
func TestQueryPagesItsResult(t *testing.T) {
	q := &queryWorkspace{state: "SUCCEEDED"}
	c, _ := q.serve(t)
	ctx := context.Background()
	ref, err := c.StartQuery(ctx, warehouse.QueryOptions{SQL: "select * from people where name = :f0", Params: map[string]string{"f0": "ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "q1" || ref.Project != "workspace" {
		t.Errorf("ref = %+v", ref)
	}
	sub := q.submitted
	if sub["disposition"] != "EXTERNAL_LINKS" || sub["wait_timeout"] != "0s" || sub["catalog"] != "workspace" || sub["warehouse_id"] != "wh1" {
		t.Errorf("submitted %v", sub)
	}
	if p, _ := json.Marshal(sub["parameters"]); string(p) != `[{"name":"f0","type":"STRING","value":"ada"}]` {
		t.Errorf("parameters = %s", p)
	}
	st, err := c.QueryJob(ctx, ref)
	if err != nil || st.State != "PENDING" || st.StatementType != "SELECT" || st.Started.IsZero() {
		t.Fatalf("first poll: %+v, %v", st, err)
	}
	if st, _ = c.QueryJob(ctx, ref); st.State != "DONE" || st.Error != "" || st.Ended.IsZero() || st.DMLRows != -1 {
		t.Fatalf("second poll: %+v", st)
	}
	page, err := c.Rows(ctx, ref, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Schema) != 2 || page.Schema[0].Type != "BIGINT" || len(page.Rows) != 1 || page.Rows[0][1] != "ada" || page.NextToken != "0:1" {
		t.Fatalf("first page = %+v", page)
	}
	if page, err = c.Rows(ctx, ref, page.NextToken, 500); err != nil || len(page.Rows) != 1 || page.Rows[0][0] != "2" || page.NextToken != "1:0" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	if page, err = c.Rows(ctx, ref, page.NextToken, 500); err != nil || len(page.Rows) != 1 || page.Rows[0][0] != "3" || page.Rows[0][1] != nil || page.NextToken != "" {
		t.Fatalf("last page = %+v, %v", page, err)
	}
	if q.fileReads != 1 {
		t.Errorf("the result file was read %d times, want once", q.fileReads)
	}
}

func TestQueryFails(t *testing.T) {
	q := &queryWorkspace{state: "FAILED"}
	c, _ := q.serve(t)
	ctx := context.Background()
	ref, _ := c.StartQuery(ctx, warehouse.QueryOptions{SQL: "insert into nope values (1)"})
	_, _ = c.QueryJob(ctx, ref)
	st, err := c.QueryJob(ctx, ref)
	if err != nil || st.State != "DONE" || !strings.Contains(st.Error, "TABLE_OR_VIEW_NOT_FOUND") || st.StatementType != "INSERT" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := c.Rows(ctx, ref, "", 10); err == nil {
		t.Error("a failed query had rows")
	}
}

func TestQueryCancelled(t *testing.T) {
	q := &queryWorkspace{state: "CANCELED"}
	c, _ := q.serve(t)
	ctx := context.Background()
	ref, _ := c.StartQuery(ctx, warehouse.QueryOptions{SQL: "select 1"})
	if err := c.CancelQuery(ctx, ref); err != nil || !q.cancelled {
		t.Fatalf("cancel: %v, %v", err, q.cancelled)
	}
	_, _ = c.QueryJob(ctx, ref)
	if st, _ := c.QueryJob(ctx, ref); !st.Cancelled || st.State != "DONE" {
		t.Errorf("status = %+v", st)
	}
}

func TestQueryResultGone(t *testing.T) {
	q := &queryWorkspace{state: "CLOSED"}
	c, _ := q.serve(t)
	ctx := context.Background()
	ref := warehouse.JobRef{Project: "workspace", ID: "q1"}
	_, _ = c.QueryJob(ctx, ref)
	if _, err := c.Rows(ctx, ref, "", 10); !errors.Is(err, ErrResultGone) {
		t.Errorf("rows of a closed statement: %v", err)
	}
}

func TestQueryNeedsAWarehouse(t *testing.T) {
	c := New(Config{Host: "https://x", Token: static("t")})
	if _, err := c.StartQuery(context.Background(), warehouse.QueryOptions{SQL: "select 1"}); !errors.Is(err, warehouse.ErrNoWarehouse) {
		t.Errorf("err = %v", err)
	}
}
