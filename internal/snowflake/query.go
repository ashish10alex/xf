package snowflake

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	maxJobs   = 200
	maxChunks = 4
)

type job struct {
	kind           string
	started, ended time.Time
	meta           *resultSet
	// resultHandle is the handle whose result rows are fetched: for a
	// multi-statement script, the last handle in statementHandles.
	resultHandle string
	// part0 holds partition 0 rows when returned inline by QueryJob.
	part0 [][]any
}

type cachedChunk struct {
	handle string
	index  int
	rows   [][]any
}

func (c *Client) jobOf(handle string) *job {
	if c.jobs == nil {
		c.jobs = map[string]*job{}
	}
	j := c.jobs[handle]
	if j == nil {
		if len(c.jobs) >= maxJobs {
			c.jobs = map[string]*job{}
		}
		j = &job{}
		c.jobs[handle] = j
	}
	return j
}

// StartQuery submits o.SQL asynchronously to Snowflake SQL API v2
// (/api/v2/statements?async=true) and returns immediately with its handle.
func (c *Client) StartQuery(ctx context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error) {
	wh := c.Warehouse()
	if wh == "" {
		return warehouse.JobRef{}, ErrNoWarehouse
	}
	db := cmp.Or(o.Project, c.database)
	req := map[string]any{
		"statement": o.SQL,
		"warehouse": wh,
		"timeout":   1800,
		"parameters": map[string]string{
			"MULTI_STATEMENT_COUNT":   "0",
			"DATE_OUTPUT_FORMAT":      "YYYY-MM-DD",
			"TIMESTAMP_OUTPUT_FORMAT": "YYYY-MM-DD HH24:MI:SS.FF3 TZHTZM",
		},
	}
	if db != "" {
		req["database"] = db
	}
	if c.schema != "" {
		req["schema"] = c.schema
	}
	if c.role != "" {
		req["role"] = c.role
	}
	_, body, err := c.do(ctx, http.MethodPost, "/api/v2/statements?async=true", req)
	if err != nil {
		return warehouse.JobRef{}, mapStatementError(err)
	}
	var st statementResp
	if err := json.Unmarshal(body, &st); err != nil {
		return warehouse.JobRef{}, fmt.Errorf("decode statement: %w", err)
	}
	if st.Handle == "" {
		return warehouse.JobRef{}, errors.New("Snowflake returned no statement handle")
	}
	c.mu.Lock()
	j := c.jobOf(st.Handle)
	j.kind, j.started = warehouse.StatementType(o.SQL), time.Now()
	if len(st.StatementHandles) > 0 {
		j.resultHandle = st.StatementHandles[len(st.StatementHandles)-1]
	}
	if st.Meta != nil {
		j.meta = st.Meta
		if len(st.Data) > 0 {
			j.part0 = cells(st.Data)
		}
	}
	c.mu.Unlock()
	return warehouse.JobRef{Project: cmp.Or(db, "snowflake"), ID: st.Handle}, nil
}

// ErrResultGone is returned when Snowflake no longer retains the statement's
// result (Snowflake keeps results for 24 hours).
var ErrResultGone = errors.New("Snowflake no longer has this result: run the query again")

// QueryJob checks where the statement ref names stands.
func (c *Client) QueryJob(ctx context.Context, ref warehouse.JobRef) (*warehouse.QueryStatus, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/api/v2/statements/"+url.PathEscape(ref.ID), nil)
	if err != nil {
		var se *StatementError
		if errors.As(err, &se) {
			c.mu.Lock()
			j := c.jobOf(ref.ID)
			if j.ended.IsZero() {
				j.ended = time.Now()
			}
			qs := &warehouse.QueryStatus{
				State:         "DONE",
				StatementType: j.kind,
				DMLRows:       -1,
				Started:       j.started,
				Ended:         j.ended,
				Error:         se.Message,
				Cancelled:     se.isCancelledError(),
			}
			if qs.Cancelled {
				qs.Error = "the query was cancelled"
			}
			c.mu.Unlock()
			return qs, nil
		}
		return nil, err
	}
	var st statementResp
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode statement: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobOf(ref.ID)
	qs := &warehouse.QueryStatus{State: "RUNNING", StatementType: j.kind, DMLRows: -1, Started: j.started}
	if status == http.StatusOK {
		if j.ended.IsZero() {
			j.ended = time.Now()
		}
		qs.State, qs.Ended = "DONE", j.ended
		if len(st.StatementHandles) > 0 && j.resultHandle == "" {
			j.resultHandle = st.StatementHandles[len(st.StatementHandles)-1]
		}
		if st.Meta != nil && j.meta == nil {
			j.meta = st.Meta
			j.part0 = cells(st.Data)
		}
	}
	return qs, nil
}

// Rows reads a page of rows from statement ref. A token is "partition:offset".
func (c *Client) Rows(ctx context.Context, ref warehouse.JobRef, token string, size int) (*warehouse.Page, error) {
	if size <= 0 {
		size = warehouse.DefaultPageSize
	}
	meta, err := c.metaOf(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	page := &warehouse.Page{Rows: [][]any{}, Total: meta.NumRows, Complete: true}
	for _, rt := range meta.RowType {
		page.Schema = append(page.Schema, columnFromRowType(rt))
	}
	index, offset, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	totalParts := len(meta.PartitionInfo)
	if totalParts == 0 {
		totalParts = 1
	}
	if index >= totalParts {
		return page, nil
	}
	rows, err := c.partitionRows(ctx, ref.ID, index)
	if err != nil {
		return nil, err
	}
	offset = min(offset, len(rows))
	end := min(offset+size, len(rows))
	page.Rows = append(page.Rows, rows[offset:end]...)
	switch {
	case end < len(rows):
		page.NextToken = strconv.Itoa(index) + ":" + strconv.Itoa(end)
	case index+1 < totalParts:
		page.NextToken = strconv.Itoa(index+1) + ":0"
	}
	return page, nil
}

func (c *Client) metaOf(ctx context.Context, handle string) (*resultSet, error) {
	c.mu.Lock()
	j := c.jobOf(handle)
	m := j.meta
	target := cmp.Or(j.resultHandle, handle)
	c.mu.Unlock()
	if m != nil {
		return m, nil
	}
	st, err := c.fetchStatementResult(ctx, target)
	if err != nil {
		return nil, err
	}
	// Multi-statement responses return child statementHandles without inline
	// resultSetMetaData; fetch the final statement's result set.
	if st.Meta == nil && len(st.StatementHandles) > 0 {
		last := st.StatementHandles[len(st.StatementHandles)-1]
		c.mu.Lock()
		c.jobOf(handle).resultHandle = last
		c.mu.Unlock()
		st, err = c.fetchStatementResult(ctx, last)
		if err != nil {
			return nil, err
		}
	}
	if st.Meta == nil {
		return nil, errors.New("Snowflake described no result")
	}
	c.mu.Lock()
	j = c.jobOf(handle)
	j.meta = st.Meta
	j.part0 = cells(st.Data)
	c.mu.Unlock()
	return st.Meta, nil
}

func (c *Client) fetchStatementResult(ctx context.Context, handle string) (*statementResp, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/api/v2/statements/"+url.PathEscape(handle), nil)
	if err != nil {
		var apiErr *warehouse.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrResultGone
		}
		return nil, mapStatementError(err)
	}
	if status == http.StatusAccepted {
		return nil, errors.New("the query is still running")
	}
	var st statementResp
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode statement: %w", err)
	}
	return &st, nil
}

func (c *Client) partitionRows(ctx context.Context, handle string, index int) ([][]any, error) {
	c.mu.Lock()
	target := handle
	if j := c.jobs[handle]; j != nil {
		if j.resultHandle != "" {
			target = j.resultHandle
		}
		if index == 0 && j.part0 != nil {
			rows := j.part0
			c.mu.Unlock()
			return rows, nil
		}
	}
	for _, k := range c.chunks {
		if k.handle == target && k.index == index {
			c.mu.Unlock()
			return k.rows, nil
		}
	}
	c.mu.Unlock()
	path := "/api/v2/statements/" + url.PathEscape(target)
	if index > 0 {
		path += "?partition=" + strconv.Itoa(index)
	}
	_, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		var apiErr *warehouse.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrResultGone
		}
		return nil, mapStatementError(err)
	}
	var st statementResp
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode partition %d: %w", index, err)
	}
	rows := cells(st.Data)
	c.mu.Lock()
	c.chunks = append(c.chunks, cachedChunk{handle: target, index: index, rows: rows})
	if len(c.chunks) > maxChunks {
		c.chunks = slices.Delete(c.chunks, 0, len(c.chunks)-maxChunks)
	}
	c.mu.Unlock()
	return rows, nil
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

// CancelQuery cancels the running statement ref names.
func (c *Client) CancelQuery(ctx context.Context, ref warehouse.JobRef) error {
	_, _, err := c.do(ctx, http.MethodPost, "/api/v2/statements/"+url.PathEscape(ref.ID)+"/cancel", nil)
	return err
}
