package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// SQLWarehouse is one of the workspace's SQL warehouses.
type SQLWarehouse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// State is RUNNING, STOPPED, STARTING, STOPPING, DELETING or DELETED.
	State string `json:"state"`
	// Size is its cluster size, such as 2X-Small.
	Size string `json:"size,omitempty"`
	// Serverless says it starts in seconds rather than minutes.
	Serverless bool `json:"serverless,omitempty"`
}

type warehouseJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Size       string `json:"cluster_size"`
	Serverless bool   `json:"enable_serverless_compute"`
}

func (w warehouseJSON) public() SQLWarehouse {
	return SQLWarehouse{ID: w.ID, Name: w.Name, State: w.State, Size: w.Size, Serverless: w.Serverless}
}

// Warehouses lists the workspace's SQL warehouses the caller can see. It
// starts none.
func (c *Client) Warehouses(ctx context.Context) ([]SQLWarehouse, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/warehouses", nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Warehouses []warehouseJSON `json:"warehouses"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode SQL warehouses: %w", err)
	}
	out := make([]SQLWarehouse, 0, len(raw.Warehouses))
	for _, w := range raw.Warehouses {
		out = append(out, w.public())
	}
	return out, nil
}

// SQLWarehouse is the chosen SQL warehouse, as it is now.
func (c *Client) SQLWarehouse(ctx context.Context) (SQLWarehouse, error) {
	id := c.Warehouse()
	if id == "" {
		return SQLWarehouse{}, ErrNoWarehouse
	}
	body, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/warehouses/"+url.PathEscape(id), nil)
	if err != nil {
		return SQLWarehouse{}, err
	}
	var w warehouseJSON
	if err := json.Unmarshal(body, &w); err != nil {
		return SQLWarehouse{}, fmt.Errorf("decode SQL warehouse: %w", err)
	}
	return w.public(), nil
}

// Running says whether the chosen SQL warehouse is up, without starting it.
func (c *Client) Running(ctx context.Context) (bool, error) {
	w, err := c.SQLWarehouse(ctx)
	if err != nil {
		return false, err
	}
	return w.State == "RUNNING", nil
}

// statement is the Statement Execution API's answer.
type statement struct {
	ID     string `json:"statement_id"`
	Status struct {
		State string `json:"state"`
		Error *struct {
			Code    string `json:"error_code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"status"`
	Manifest *manifest `json:"manifest"`
	// Result is the result's first chunk.
	Result chunk `json:"result"`
}

// manifest describes a statement's result: its columns and how many rows
// and chunks it has.
type manifest struct {
	Schema struct {
		Columns []struct {
			Name     string `json:"name"`
			TypeText string `json:"type_text"`
		} `json:"columns"`
	} `json:"schema"`
	TotalChunks int   `json:"total_chunk_count"`
	TotalRows   int64 `json:"total_row_count"`
	// Truncated says the result was cut at a row or byte limit.
	Truncated bool `json:"truncated"`
}

// chunk is a part of a statement's result: its rows inline (INLINE), or
// links to the files that hold them (EXTERNAL_LINKS).
type chunk struct {
	Index int          `json:"chunk_index"`
	Rows  [][]*string  `json:"data_array"`
	Links []resultLink `json:"external_links"`
}

// resultLink is a presigned URL to rows of a chunk. It takes no token: the
// workspace's must never be sent to it.
type resultLink struct {
	URL     string            `json:"external_link"`
	Headers map[string]string `json:"http_headers"`
}

// StatementError is a statement the warehouse ran and failed: its SQL is
// wrong, or names what isn't there.
type StatementError struct {
	Code, Message string
}

func (e *StatementError) Error() string { return e.Message }

// done says the statement has ended.
func (s *statement) done() bool {
	switch s.Status.State {
	case "SUCCEEDED", "FAILED", "CANCELED", "CLOSED":
		return true
	}
	return false
}

// execute runs sql on the chosen warehouse and waits for its rows, which a
// small result (a DESCRIBE, an EXPLAIN) has inline. A warehouse that is
// stopped starts. Cancelling ctx cancels the statement.
func (c *Client) execute(ctx context.Context, sql string) (*statement, error) {
	id := c.Warehouse()
	if id == "" {
		return nil, ErrNoWarehouse
	}
	body, err := c.do(ctx, http.MethodPost, "/api/2.0/sql/statements", map[string]any{
		"warehouse_id": id, "statement": sql,
		"wait_timeout": "30s", "on_wait_timeout": "CONTINUE",
		"disposition": "INLINE", "format": "JSON_ARRAY",
	})
	if err != nil {
		return nil, err
	}
	var st statement
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode statement: %w", err)
	}
	for !st.done() {
		select {
		case <-ctx.Done():
			c.cancel(st.ID)
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
		body, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/statements/"+url.PathEscape(st.ID), nil)
		if err != nil {
			if ctx.Err() != nil {
				c.cancel(st.ID)
			}
			return nil, err
		}
		st = statement{}
		if err := json.Unmarshal(body, &st); err != nil {
			return nil, fmt.Errorf("decode statement: %w", err)
		}
	}
	switch st.Status.State {
	case "SUCCEEDED":
		return &st, nil
	case "FAILED":
		e := &StatementError{Message: "the statement failed"}
		if st.Status.Error != nil {
			e.Code, e.Message = st.Status.Error.Code, st.Status.Error.Message
		}
		return nil, e
	}
	return nil, fmt.Errorf("the statement was %s", strings.ToLower(st.Status.State))
}

// cancel asks the warehouse to stop a statement whose caller has gone.
func (c *Client) cancel(id string) {
	if id == "" {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_, _ = c.do(ctx, http.MethodPost, "/api/2.0/sql/statements/"+url.PathEscape(id)+"/cancel", nil)
}

// Check asks the SQL warehouse what query returns: DESCRIBE QUERY gives its
// columns, and when it fails EXPLAIN says why, in the planner's words. It
// starts a stopped warehouse, which bills while it runs (ADR 0042). The
// tables the query reads stay empty; project and location are BigQuery's
// and go unused.
func (c *Client) Check(ctx context.Context, _, _, query string) (*warehouse.Check, error) {
	query = strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
	st, err := c.execute(ctx, "DESCRIBE QUERY "+query)
	var se *StatementError
	if errors.As(err, &se) {
		msg := atQuery(se.Message, "DESCRIBE QUERY ")
		if why := c.explainError(ctx, query); why != "" {
			msg = why
		}
		return nil, &warehouse.APIError{Status: http.StatusBadRequest, Message: msg}
	}
	if err != nil {
		return nil, err
	}
	res := &warehouse.Check{StatementType: "SELECT", Accuracy: warehouse.AccuracyUnknown}
	for _, row := range st.Result.Rows {
		if len(row) < 2 || row[0] == nil {
			continue
		}
		col := warehouse.Column{Name: *row[0]}
		if row[1] != nil {
			col.Type = *row[1]
		}
		if len(row) > 2 && row[2] != nil {
			col.Description = *row[2]
		}
		res.Schema = append(res.Schema, col)
	}
	return res, nil
}

// planningError starts EXPLAIN's answer for a query the planner rejects.
const planningError = "Error occurred during query planning:"

// explainError is why the planner rejects query, from EXPLAIN; "" when it
// doesn't say.
func (c *Client) explainError(ctx context.Context, query string) string {
	const prefix = "EXPLAIN "
	st, err := c.execute(ctx, prefix+query)
	if se, ok := errors.AsType[*StatementError](err); ok {
		return atQuery(se.Message, prefix)
	}
	if err != nil {
		return ""
	}
	// The plan comes a line to a row: the marker, then the error, then
	// the plan as far as it got.
	var lines []string
	for _, row := range st.Result.Rows {
		if len(row) > 0 && row[0] != nil {
			lines = append(lines, *row[0])
		}
	}
	plan := strings.Join(lines, "\n")
	i := strings.Index(plan, planningError)
	if i < 0 {
		return ""
	}
	for _, l := range strings.Split(plan[i+len(planningError):], "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return atQuery(strings.TrimSuffix(l, ";"), prefix)
		}
	}
	return ""
}

var linePos = regexp.MustCompile(`\bline 1 pos (\d+)`)

// atQuery places a message's "line 1 pos N" in the query as the user wrote
// it, before prefix was put in front of it.
func atQuery(msg, prefix string) string {
	return linePos.ReplaceAllStringFunc(msg, func(m string) string {
		n, err := strconv.Atoi(linePos.FindStringSubmatch(m)[1])
		if err != nil || n < len(prefix) {
			return m
		}
		return "line 1 pos " + strconv.Itoa(n-len(prefix))
	})
}
