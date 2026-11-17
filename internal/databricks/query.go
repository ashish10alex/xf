package databricks

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// The queries a user runs (scratch queries, the dependency inspector's)
// go through the Statement Execution API: StartQuery submits one and
// returns at once, QueryJob polls it, Rows pages its result and
// CancelQuery stops it. The result comes as EXTERNAL_LINKS, files the
// workspace keeps for a while, so a large one is read a chunk at a time
// rather than failing at INLINE's 25 MiB.

const (
	// maxJobs is how many queries' types and times a client remembers.
	maxJobs = 200
	// maxChunks is how many result chunks a client keeps read.
	maxChunks = 4
)

// job is what the API doesn't say of a query: what kind of statement it
// is, and when it started and ended as this client saw it.
type job struct {
	kind           string
	started, ended time.Time
	// manifest is its result's, once it succeeded.
	manifest *manifest
}

type cachedChunk struct {
	statement string
	index     int
	rows      [][]any
}

// jobOf is the client's record of statement id, made when it has none (a
// query started before xf did).
func (c *Client) jobOf(id string) *job {
	if c.jobs == nil {
		c.jobs = map[string]*job{}
	}
	j := c.jobs[id]
	if j == nil {
		if len(c.jobs) >= maxJobs {
			c.jobs = map[string]*job{} // a session never follows this many at once
		}
		j = &job{}
		c.jobs[id] = j
	}
	return j
}

// StartQuery submits o.SQL to the chosen SQL warehouse, starting it when
// it is stopped, and returns at once. o.Project is the catalog the
// statement's unqualified names are in, the client's when "";
// o.Location and o.Labels are BigQuery's and go unused. o.Params are
// STRING parameters, :name in the SQL.
func (c *Client) StartQuery(ctx context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error) {
	id := c.Warehouse()
	if id == "" {
		return warehouse.JobRef{}, ErrNoWarehouse
	}
	catalog := cmp.Or(o.Project, c.catalog)
	req := map[string]any{
		"warehouse_id": id, "statement": o.SQL,
		"wait_timeout": "0s", "on_wait_timeout": "CONTINUE",
		"disposition": "EXTERNAL_LINKS", "format": "JSON_ARRAY",
	}
	if catalog != "" {
		req["catalog"] = catalog
	}
	if len(o.Params) > 0 {
		var params []map[string]string
		for _, name := range slices.Sorted(maps.Keys(o.Params)) {
			params = append(params, map[string]string{"name": name, "value": o.Params[name], "type": "STRING"})
		}
		req["parameters"] = params
	}
	body, err := c.do(ctx, http.MethodPost, "/api/2.0/sql/statements", req)
	if err != nil {
		return warehouse.JobRef{}, err
	}
	var st statement
	if err := json.Unmarshal(body, &st); err != nil {
		return warehouse.JobRef{}, fmt.Errorf("decode statement: %w", err)
	}
	if st.ID == "" {
		return warehouse.JobRef{}, errors.New("the SQL warehouse returned no statement")
	}
	c.mu.Lock()
	j := c.jobOf(st.ID)
	j.kind, j.started = warehouse.StatementType(o.SQL), time.Now()
	c.mu.Unlock()
	return warehouse.JobRef{Project: cmp.Or(catalog, "databricks"), ID: st.ID}, nil
}

// statusOf reads a statement as it is now.
func (c *Client) statusOf(ctx context.Context, id string) (*statement, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/statements/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var st statement
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode statement: %w", err)
	}
	return &st, nil
}

// ErrResultGone is a result the workspace no longer keeps.
var ErrResultGone = errors.New("Databricks no longer has this result: run the query again")

// QueryJob is where the statement ref names stands. PENDING covers a SQL
// warehouse that is still starting.
func (c *Client) QueryJob(ctx context.Context, ref warehouse.JobRef) (*warehouse.QueryStatus, error) {
	st, err := c.statusOf(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobOf(ref.ID)
	qs := &warehouse.QueryStatus{State: st.Status.State, StatementType: j.kind, DMLRows: -1, Started: j.started}
	if st.done() {
		if j.ended.IsZero() {
			j.ended = time.Now()
		}
		qs.State, qs.Ended = "DONE", j.ended
	}
	switch st.Status.State {
	case "SUCCEEDED":
		j.manifest = st.Manifest
	case "FAILED":
		qs.Error = "the statement failed"
		if st.Status.Error != nil && st.Status.Error.Message != "" {
			qs.Error = st.Status.Error.Message
		}
	case "CANCELED":
		qs.Error, qs.Cancelled = "the query was cancelled", true
	case "CLOSED":
		qs.Error = ErrResultGone.Error()
	}
	return qs, nil
}

// manifestOf is a succeeded statement's manifest.
func (c *Client) manifestOf(ctx context.Context, id string) (*manifest, error) {
	c.mu.Lock()
	m := c.jobOf(id).manifest
	c.mu.Unlock()
	if m != nil {
		return m, nil
	}
	st, err := c.statusOf(ctx, id)
	if err != nil {
		return nil, err
	}
	switch st.Status.State {
	case "SUCCEEDED":
	case "CLOSED":
		return nil, ErrResultGone
	default:
		return nil, fmt.Errorf("the query has no rows to read: it is %s", strings.ToLower(st.Status.State))
	}
	if st.Manifest == nil {
		return nil, errors.New("the SQL warehouse described no result")
	}
	c.mu.Lock()
	c.jobOf(id).manifest = st.Manifest
	c.mu.Unlock()
	return st.Manifest, nil
}

// Rows reads a page of the result of ref. A token is "chunk:offset", where
// the page starts; a page ends at its chunk's end, so it may hold fewer
// than size rows with more to come. Cells are strings or nil, as
// JSON_ARRAY gives them.
func (c *Client) Rows(ctx context.Context, ref warehouse.JobRef, token string, size int) (*warehouse.Page, error) {
	if size <= 0 {
		size = warehouse.DefaultPageSize
	}
	m, err := c.manifestOf(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	page := &warehouse.Page{Rows: [][]any{}, Total: m.TotalRows, Complete: true}
	for _, col := range m.Schema.Columns {
		page.Schema = append(page.Schema, warehouse.Column{Name: col.Name, Type: col.TypeText})
	}
	index, offset, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	if m.TotalChunks == 0 || index >= m.TotalChunks {
		return page, nil
	}
	rows, err := c.chunkRows(ctx, ref.ID, index)
	if err != nil {
		return nil, err
	}
	offset = min(offset, len(rows))
	end := min(offset+size, len(rows))
	page.Rows = append(page.Rows, rows[offset:end]...)
	switch {
	case end < len(rows):
		page.NextToken = strconv.Itoa(index) + ":" + strconv.Itoa(end)
	case index+1 < m.TotalChunks:
		page.NextToken = strconv.Itoa(index+1) + ":0"
	}
	return page, nil
}

func parseToken(token string) (index, offset int, err error) {
	if token == "" {
		return 0, 0, nil
	}
	a, b, ok := strings.Cut(token, ":")
	index, err1 := strconv.Atoi(a)
	offset, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || index < 0 || offset < 0 {
		return 0, 0, fmt.Errorf("not a page token: %q", token)
	}
	return index, offset, nil
}

// chunkRows are the rows of one chunk of a statement's result, read from
// its files once and kept while it is among the last read.
func (c *Client) chunkRows(ctx context.Context, id string, index int) ([][]any, error) {
	c.mu.Lock()
	for _, k := range c.chunks {
		if k.statement == id && k.index == index {
			c.mu.Unlock()
			return k.rows, nil
		}
	}
	c.mu.Unlock()
	body, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/statements/"+url.PathEscape(id)+"/result/chunks/"+strconv.Itoa(index), nil)
	if err != nil {
		return nil, err
	}
	var ch chunk
	if err := json.Unmarshal(body, &ch); err != nil {
		return nil, fmt.Errorf("decode result chunk: %w", err)
	}
	rows := cells(ch.Rows)
	for _, l := range ch.Links {
		more, err := c.fetchLink(ctx, l)
		if err != nil {
			return nil, err
		}
		rows = append(rows, more...)
	}
	c.mu.Lock()
	c.chunks = append(c.chunks, cachedChunk{statement: id, index: index, rows: rows})
	if len(c.chunks) > maxChunks {
		c.chunks = slices.Delete(c.chunks, 0, len(c.chunks)-maxChunks)
	}
	c.mu.Unlock()
	return rows, nil
}

// fetchLink reads the rows in one result file. The URL is presigned, so
// the request carries the headers the API gave and never the token.
func (c *Client) fetchLink(ctx context.Context, l resultLink) ([][]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range l.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the result: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return nil, ErrResultGone // a link expires; the statement may still answer with a fresh one
		}
		return nil, fmt.Errorf("read the result: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var raw [][]*string
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode the result: %w", err)
	}
	return cells(raw), nil
}

// cells are JSON_ARRAY's rows as a Page holds them.
func cells(raw [][]*string) [][]any {
	out := make([][]any, 0, len(raw))
	for _, r := range raw {
		row := make([]any, len(r))
		for i, v := range r {
			if v != nil {
				row[i] = *v
			}
		}
		out = append(out, row)
	}
	return out
}

// CancelQuery asks the SQL warehouse to stop the statement ref names.
func (c *Client) CancelQuery(ctx context.Context, ref warehouse.JobRef) error {
	_, err := c.do(ctx, http.MethodPost, "/api/2.0/sql/statements/"+url.PathEscape(ref.ID)+"/cancel", nil)
	return err
}
