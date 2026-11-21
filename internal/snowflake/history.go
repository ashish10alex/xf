package snowflake

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// Cost on Snowflake is a model's last run (ADR 0044): Snowflake bills virtual
// warehouse compute time, not bytes scanned, so there is nothing to estimate
// before a run. SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY (30 days, falling back
// to INFORMATION_SCHEMA.QUERY_HISTORY for 7 days if ACCOUNT_USAGE is not
// accessible) keeps what each query did; dbt marks its queries with a JSON
// comment naming the node and target, on by default.

const (
	// HistoryDays is how far back ACCOUNT_USAGE.QUERY_HISTORY is read.
	HistoryDays = 30
	// InfoSchemaHistoryDays is the fallback window for INFORMATION_SCHEMA.
	InfoSchemaHistoryDays = 7
	maxHistoryRows        = 5000
)

// Run is a statement that built a dbt node, as Snowflake Query History has it.
type Run struct {
	NodeID, Target string
	QueryID        string
	Warehouse      string
	Started        time.Time
	Duration       time.Duration
	ReadBytes      int64
	ProducedRows   int64
	StatementType  string
}

// History is what a read of Snowflake Query History found.
type History struct {
	Runs      map[string]Run
	Queries   int
	Dbt       int
	Targets   map[string]int
	Since     time.Time
	Days      int
	Truncated bool
}

var dbtComment = regexp.MustCompile(`/\*\s*(\{[^*]*?"app"\s*:\s*"dbt"[^*]*?\})\s*\*/`)

var buildTypes = map[string]bool{
	"CREATE":                 true,
	"CREATE_TABLE":           true,
	"CREATE_TABLE_AS_SELECT": true,
	"CREATE_VIEW":            true,
	"REPLACE":                true,
	"MERGE":                  true,
	"INSERT":                 true,
	"COPY":                   true,
	"UPDATE":                 true,
	"DELETE":                 true,
}

// DbtRuns reads Snowflake Query History back to since (defaulting to
// HistoryDays) and keeps each dbt node's latest finished build whose query
// comment names target ("" for any). Falls back to INFORMATION_SCHEMA when
// SNOWFLAKE.ACCOUNT_USAGE is not granted. Both queries run on the configured
// virtual warehouse.
func (c *Client) DbtRuns(ctx context.Context, since time.Time, target string) (*History, error) {
	now := time.Now().UTC()
	if since.IsZero() {
		since = now.AddDate(0, 0, -HistoryDays)
	} else {
		since = since.UTC()
	}
	days := max(int(now.Sub(since).Hours()/24+0.5), 1)
	sinceLit := since.Format("2006-01-02 15:04:05 +0000")

	q := fmt.Sprintf(`SELECT
  QUERY_ID,
  QUERY_TEXT,
  QUERY_TYPE,
  WAREHOUSE_NAME,
  DATE_PART(epoch_millisecond, START_TIME) AS START_MS,
  TOTAL_ELAPSED_TIME,
  BYTES_SCANNED,
  ROWS_PRODUCED
FROM SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY
WHERE EXECUTION_STATUS = 'SUCCESS'
  AND START_TIME >= TO_TIMESTAMP_TZ('%s', 'YYYY-MM-DD HH24:MI:SS TZHTZM')
  AND CONTAINS(QUERY_TEXT, '"app"')
  AND CONTAINS(QUERY_TEXT, '"dbt"')
ORDER BY START_TIME DESC
LIMIT %d`, sinceLit, maxHistoryRows)

	st, err := c.execute(ctx, q)
	if err != nil {
		// Fall back to INFORMATION_SCHEMA.QUERY_HISTORY (up to 7 days) when
		// ACCOUNT_USAGE is not granted or does not exist.
		infoLimit := now.AddDate(0, 0, -InfoSchemaHistoryDays)
		fbSince := since
		if fbSince.Before(infoLimit) {
			fbSince = infoLimit
		}
		fbDays := min(days, InfoSchemaHistoryDays)
		fbLit := fbSince.Format("2006-01-02 15:04:05 +0000")
		fallback := fmt.Sprintf(`SELECT
  QUERY_ID,
  QUERY_TEXT,
  QUERY_TYPE,
  WAREHOUSE_NAME,
  DATE_PART(epoch_millisecond, START_TIME) AS START_MS,
  TOTAL_ELAPSED_TIME,
  BYTES_SCANNED,
  ROWS_PRODUCED
FROM TABLE(INFORMATION_SCHEMA.QUERY_HISTORY(
  END_TIME_RANGE_START => TO_TIMESTAMP_TZ('%s', 'YYYY-MM-DD HH24:MI:SS TZHTZM'),
  RESULT_LIMIT => %d
))
WHERE EXECUTION_STATUS = 'SUCCESS'
  AND CONTAINS(QUERY_TEXT, '"app"')
  AND CONTAINS(QUERY_TEXT, '"dbt"')
ORDER BY START_TIME DESC`, fbLit, maxHistoryRows)
		var ferr error
		st, ferr = c.execute(ctx, fallback)
		if ferr != nil {
			primary := mapStatementError(err)
			secondary := mapStatementError(ferr)
			if c.database == "" {
				return nil, fmt.Errorf("ACCOUNT_USAGE.QUERY_HISTORY failed (%w); INFORMATION_SCHEMA fallback also failed (no default database set): %v", primary, secondary)
			}
			return nil, fmt.Errorf("ACCOUNT_USAGE.QUERY_HISTORY failed (%w); INFORMATION_SCHEMA fallback also failed: %v", primary, secondary)
		}
		since = fbSince
		days = fbDays
	}

	h := &History{
		Runs:      map[string]Run{},
		Targets:   map[string]int{},
		Since:     since,
		Days:      days,
		Truncated: len(st.Data) >= maxHistoryRows,
	}
	idx := st.colIndex()
	h.Queries = len(st.Data)
	for _, row := range st.Data {
		h.addRow(row, idx, target)
	}
	return h, nil
}

func (h *History) addRow(row []*string, idx map[string]int, target string) {
	text := cellStr(row, idx, "query_text")
	m := dbtComment.FindStringSubmatch(text)
	if m == nil {
		return
	}
	var comment struct {
		Target string `json:"target_name"`
		Node   string `json:"node_id"`
	}
	if json.Unmarshal([]byte(m[1]), &comment) != nil || comment.Node == "" {
		return
	}
	h.Dbt++
	h.Targets[comment.Target]++

	kind := strings.ToUpper(cellStr(row, idx, "query_type"))
	if kind == "" || kind == "UNKNOWN" {
		kind = warehouse.StatementType(strings.Replace(text, m[0], "", 1))
	}
	isBuild := buildTypes[kind] || strings.HasPrefix(kind, "CREATE_")
	if !isBuild || (target != "" && comment.Target != target) {
		return
	}

	startMS, _ := strconv.ParseInt(cellStr(row, idx, "start_ms"), 10, 64)
	elapsedMS, _ := strconv.ParseInt(cellStr(row, idx, "total_elapsed_time"), 10, 64)
	bytesScanned, _ := strconv.ParseInt(cellStr(row, idx, "bytes_scanned"), 10, 64)
	rowsProduced, _ := strconv.ParseInt(cellStr(row, idx, "rows_produced"), 10, 64)

	r := Run{
		NodeID:        comment.Node,
		Target:        comment.Target,
		QueryID:       cellStr(row, idx, "query_id"),
		Warehouse:     cellStr(row, idx, "warehouse_name"),
		Started:       time.UnixMilli(startMS),
		Duration:      time.Duration(elapsedMS) * time.Millisecond,
		ReadBytes:     bytesScanned,
		ProducedRows:  rowsProduced,
		StatementType: kind,
	}
	if old, ok := h.Runs[r.NodeID]; !ok || r.Started.After(old.Started) {
		h.Runs[r.NodeID] = r
	}
}
