package snowflake

import (
	"cmp"
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

// VirtualWarehouse is one of the Snowflake account's virtual warehouses.
type VirtualWarehouse struct {
	Name string `json:"name"`
	// State is STARTED, SUSPENDED, RESIZING, STARTING, or SUSPENDING.
	State string `json:"state"`
	// Size is X-Small, Small, Medium, Large, etc.
	Size string `json:"size,omitempty"`
	// Type is STANDARD or SNOWPARK-OPTIMIZED.
	Type string `json:"type,omitempty"`
}

// StatementError is a statement Snowflake rejected (HTTP 422): its SQL is
// wrong, or names an object that does not exist.
type StatementError struct {
	Code, SQLState, Message string
}

func (e *StatementError) Error() string { return e.Message }

// isNotFoundError reports whether e is Snowflake's "does not exist or not
// authorized" SQL compilation error (code 002003 / SQLState 42S02).
func (e *StatementError) isNotFoundError() bool {
	if e == nil {
		return false
	}
	return e.Code == "002003" || e.SQLState == "42S02" ||
		strings.Contains(strings.ToLower(e.Message), "does not exist or not authorized")
}

// isCancelledError reports whether e indicates a canceled query (000604).
func (e *StatementError) isCancelledError() bool {
	if e == nil {
		return false
	}
	return e.Code == "000604" || strings.Contains(strings.ToLower(e.Message), "sql execution canceled")
}

// isTimeoutError reports whether e indicates a Snowflake statement timeout (000630).
func (e *StatementError) isTimeoutError() bool {
	if e == nil {
		return false
	}
	return e.Code == "000630" || strings.Contains(strings.ToLower(e.Message), "statement reached its statement or warehouse timeout")
}

// statementResp is Snowflake SQL API v2's response to POST/GET /api/v2/statements.
type statementResp struct {
	Handle           string      `json:"statementHandle"`
	StatementHandles []string    `json:"statementHandles"`
	StatusURL        string      `json:"statementStatusUrl"`
	Code             string      `json:"code"`
	SQLState         string      `json:"sqlState"`
	Message          string      `json:"message"`
	Meta             *resultSet  `json:"resultSetMetaData"`
	Data             [][]*string `json:"data"`
}

type resultSet struct {
	NumRows       int64           `json:"numRows"`
	Format        string          `json:"format"`
	PartitionInfo []partitionInfo `json:"partitionInfo"`
	RowType       []rowType       `json:"rowType"`
}

type partitionInfo struct {
	RowCount         int64 `json:"rowCount"`
	UncompressedSize int64 `json:"uncompressedSize"`
}

type rowType struct {
	Name       string `json:"name"`
	Database   string `json:"database"`
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	Nullable   bool   `json:"nullable"`
	ByteLength *int64 `json:"byteLength"`
	Length     *int64 `json:"length"`
	Scale      *int64 `json:"scale"`
	Precision  *int64 `json:"precision"`
	Type       string `json:"type"`
}

// colIndex maps lowercased column names of r's result set to their index.
func (r *statementResp) colIndex() map[string]int {
	idx := map[string]int{}
	if r == nil || r.Meta == nil {
		return idx
	}
	for i, c := range r.Meta.RowType {
		idx[strings.ToLower(c.Name)] = i
	}
	return idx
}

func cellStr(row []*string, idx map[string]int, col string) string {
	i, ok := idx[col]
	if !ok || i < 0 || i >= len(row) || row[i] == nil {
		return ""
	}
	return *row[i]
}

// execute runs sql on the configured virtual warehouse and waits for completion.
// Fails with ErrNoWarehouse when no warehouse is chosen.
func (c *Client) execute(ctx context.Context, sql string) (*statementResp, error) {
	wh := c.Warehouse()
	if wh == "" {
		return nil, ErrNoWarehouse
	}
	return c.executeWith(ctx, sql, wh, c.database, c.schema)
}

// executeMetadata runs a Cloud Services metadata statement (SHOW, DESCRIBE)
// without sending a virtual warehouse. Because Snowflake falls back to the
// user's DEFAULT_WAREHOUSE for statements that need compute, only SHOW and
// DESCRIBE (never SELECT) may call executeMetadata.
func (c *Client) executeMetadata(ctx context.Context, sql string) (*statementResp, error) {
	return c.executeWith(ctx, sql, "", c.database, c.schema)
}

func (c *Client) executeWith(ctx context.Context, sql, wh, db, schema string) (*statementResp, error) {
	req := map[string]any{
		"statement": sql,
		"timeout":   300,
		"parameters": map[string]string{
			"DATE_OUTPUT_FORMAT":      "YYYY-MM-DD",
			"TIMESTAMP_OUTPUT_FORMAT": "YYYY-MM-DD HH24:MI:SS.FF3 TZHTZM",
			"QUERY_TAG":               "xf:internal",
		},
	}
	if wh != "" {
		req["warehouse"] = wh
	}
	if db != "" {
		req["database"] = db
	}
	if schema != "" {
		req["schema"] = schema
	}
	if c.role != "" {
		req["role"] = c.role
	}
	status, body, err := c.do(ctx, http.MethodPost, "/api/v2/statements", req)
	if err != nil {
		return nil, err
	}
	var st statementResp
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode statement: %w", err)
	}
	for status == http.StatusAccepted {
		select {
		case <-ctx.Done():
			c.cancel(st.Handle)
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
		status, body, err = c.do(ctx, http.MethodGet, "/api/v2/statements/"+url.PathEscape(st.Handle), nil)
		if err != nil {
			if ctx.Err() != nil {
				c.cancel(st.Handle)
			}
			return nil, err
		}
		st = statementResp{}
		if err := json.Unmarshal(body, &st); err != nil {
			return nil, fmt.Errorf("decode statement: %w", err)
		}
	}
	if st.Meta != nil && len(st.Meta.PartitionInfo) > 1 && st.Handle != "" {
		for i := 1; i < len(st.Meta.PartitionInfo); i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			partPath := "/api/v2/statements/" + url.PathEscape(st.Handle) + "?partition=" + strconv.Itoa(i)
			_, partBody, err := c.do(ctx, http.MethodGet, partPath, nil)
			if err != nil {
				return nil, err
			}
			var part statementResp
			if err := json.Unmarshal(partBody, &part); err != nil {
				return nil, fmt.Errorf("decode statement partition %d: %w", i, err)
			}
			st.Data = append(st.Data, part.Data...)
		}
	}
	return &st, nil
}

// cancel asks Snowflake to cancel a running statement handle.
func (c *Client) cancel(handle string) {
	if handle == "" {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_, _, _ = c.do(ctx, http.MethodPost, "/api/v2/statements/"+url.PathEscape(handle)+"/cancel", nil)
}

// Warehouses lists the account's virtual warehouses the caller can see, via
// SHOW WAREHOUSES in Cloud Services. It starts none.
func (c *Client) Warehouses(ctx context.Context) ([]VirtualWarehouse, error) {
	st, err := c.executeMetadata(ctx, "SHOW WAREHOUSES")
	if err != nil {
		return nil, err
	}
	idx := st.colIndex()
	out := make([]VirtualWarehouse, 0, len(st.Data))
	for _, row := range st.Data {
		name := cellStr(row, idx, "name")
		if name == "" {
			continue
		}
		out = append(out, VirtualWarehouse{
			Name:  name,
			State: strings.ToUpper(cellStr(row, idx, "state")),
			Size:  cellStr(row, idx, "size"),
			Type:  cellStr(row, idx, "type"),
		})
	}
	return out, nil
}

// VirtualWarehouse returns the chosen virtual warehouse's current status
// without starting it.
func (c *Client) VirtualWarehouse(ctx context.Context) (VirtualWarehouse, error) {
	name := c.Warehouse()
	if name == "" {
		return VirtualWarehouse{}, ErrNoWarehouse
	}
	st, err := c.executeMetadata(ctx, "SHOW WAREHOUSES LIKE "+quoteLiteral(escapeLike(name)))
	if err != nil {
		return VirtualWarehouse{}, err
	}
	idx := st.colIndex()
	for _, row := range st.Data {
		n := cellStr(row, idx, "name")
		if strings.EqualFold(n, name) {
			return VirtualWarehouse{
				Name:  n,
				State: strings.ToUpper(cellStr(row, idx, "state")),
				Size:  cellStr(row, idx, "size"),
				Type:  cellStr(row, idx, "type"),
			}, nil
		}
	}
	return VirtualWarehouse{}, &warehouse.APIError{Status: http.StatusNotFound, Message: fmt.Sprintf("virtual warehouse %q not found or not accessible", name)}
}

// Running says whether the chosen virtual warehouse is STARTED, without
// starting it.
func (c *Client) Running(ctx context.Context) (bool, error) {
	w, err := c.VirtualWarehouse(ctx)
	if err != nil {
		return false, err
	}
	return w.State == "STARTED", nil
}

// Check validates query and returns its output columns by running
// SELECT * FROM (<query>) LIMIT 0 on the virtual warehouse, which scans zero
// rows and returns resultSetMetaData.rowType with exact Snowflake types.
// A suspended warehouse may resume when needed (ADR 0044).
func (c *Client) Check(ctx context.Context, db, _, query string) (*warehouse.Check, error) {
	query = strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
	if query == "" {
		return nil, &warehouse.APIError{Status: http.StatusBadRequest, Message: "empty query"}
	}
	wh := c.Warehouse()
	if wh == "" {
		return nil, ErrNoWarehouse
	}
	wrapped := "/* xf:check */ SELECT * FROM (\n" + query + "\n) LIMIT 0"
	st, err := c.executeWith(ctx, wrapped, wh, cmp.Or(db, c.database), c.schema)
	var se *StatementError
	if errors.As(err, &se) {
		if se.isTimeoutError() {
			return nil, &warehouse.APIError{Status: http.StatusGatewayTimeout, Message: "check timed out on Snowflake: " + se.Message}
		}
		return nil, &warehouse.APIError{Status: http.StatusBadRequest, Message: adjustLineOffset(se.Message, 1)}
	}
	if err != nil {
		return nil, err
	}
	res := &warehouse.Check{StatementType: "SELECT", Accuracy: warehouse.AccuracyUnknown}
	if st.Meta != nil {
		for _, rt := range st.Meta.RowType {
			res.Schema = append(res.Schema, columnFromRowType(rt))
		}
	}
	return res, nil
}

// columnFromRowType converts a Snowflake SQL API rowType into a warehouse.Column
// using Snowflake's own DDL type spelling (matching DESCRIBE TABLE).
func columnFromRowType(rt rowType) warehouse.Column {
	return warehouse.Column{
		Name:     rt.Name,
		Type:     formatSnowflakeType(rt),
		Required: !rt.Nullable,
	}
}

// formatSnowflakeType spells a SQL API rowType as Snowflake's DESCRIBE TABLE
// spells it, so Check and Table schemas compare cleanly in xf impact.
func formatSnowflakeType(rt rowType) string {
	switch strings.ToLower(rt.Type) {
	case "fixed":
		if rt.Precision != nil && rt.Scale != nil {
			return fmt.Sprintf("NUMBER(%d,%d)", *rt.Precision, *rt.Scale)
		}
		if rt.Precision != nil {
			return fmt.Sprintf("NUMBER(%d,0)", *rt.Precision)
		}
		return "NUMBER(38,0)"
	case "text":
		if rt.Length != nil && *rt.Length > 0 {
			return fmt.Sprintf("VARCHAR(%d)", *rt.Length)
		}
		return "VARCHAR(16777216)"
	case "real":
		return "FLOAT"
	case "boolean":
		return "BOOLEAN"
	case "date":
		return "DATE"
	case "time":
		if rt.Scale != nil {
			return fmt.Sprintf("TIME(%d)", *rt.Scale)
		}
		return "TIME(9)"
	case "timestamp_ntz":
		if rt.Scale != nil {
			return fmt.Sprintf("TIMESTAMP_NTZ(%d)", *rt.Scale)
		}
		return "TIMESTAMP_NTZ(9)"
	case "timestamp_ltz":
		if rt.Scale != nil {
			return fmt.Sprintf("TIMESTAMP_LTZ(%d)", *rt.Scale)
		}
		return "TIMESTAMP_LTZ(9)"
	case "timestamp_tz":
		if rt.Scale != nil {
			return fmt.Sprintf("TIMESTAMP_TZ(%d)", *rt.Scale)
		}
		return "TIMESTAMP_TZ(9)"
	case "binary":
		if rt.Length != nil && *rt.Length > 0 {
			return fmt.Sprintf("BINARY(%d)", *rt.Length)
		}
		return "BINARY(8388608)"
	case "variant":
		return "VARIANT"
	case "object":
		return "OBJECT"
	case "array":
		return "ARRAY"
	case "geography":
		return "GEOGRAPHY"
	case "geometry":
		return "GEOMETRY"
	default:
		return strings.ToUpper(rt.Type)
	}
}

var sfLineRE = regexp.MustCompile(`\bline (\d+) at position (\d+)`)

// adjustLineOffset shifts Snowflake's "line N at position P" error locations
// back by delta lines (for the wrapper line added by Check).
func adjustLineOffset(msg string, delta int) string {
	return sfLineRE.ReplaceAllStringFunc(msg, func(m string) string {
		sub := sfLineRE.FindStringSubmatch(m)
		n, err := strconv.Atoi(sub[1])
		if err != nil || n <= delta {
			return m
		}
		return "line " + strconv.Itoa(n-delta) + " at position " + sub[2]
	})
}
