package snowflake

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	infoSchemaMaxRows     = 10000
	// burstGap merges consecutive build statements of the same dbt node that
	// execute back-to-back (e.g. CREATE TEMP TABLE + MERGE) into one model run.
	burstGap = 2 * time.Minute
)

// Run is a dbt node's latest build burst (and cadence/medians over the window),
// as Snowflake Query History has it.
type Run struct {
	NodeID, Target  string
	QueryID         string
	Warehouse       string
	WarehouseSize   string
	Started         time.Time
	Duration        time.Duration
	ReadBytes       int64
	ProducedRows    int64
	Credits         float64
	StatementType   string
	StatementCount  int
	RunCount        int
	MedianDuration  time.Duration
	MedianReadBytes int64
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

	stmts map[string][]Run
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

const buildTypesSQL = `'CREATE', 'CREATE_TABLE', 'CREATE_TABLE_AS_SELECT', 'CREATE_VIEW', 'REPLACE', 'MERGE', 'INSERT', 'COPY', 'UPDATE', 'DELETE', 'UNKNOWN'`

// DbtRuns reads Snowflake Query History back to since (defaulting to
// HistoryDays) and keeps each dbt node's latest finished build whose query
// comment names target ("" for any). Falls back to INFORMATION_SCHEMA when
// SNOWFLAKE.ACCOUNT_USAGE is not granted. Both queries run on the configured
// virtual warehouse.
func (c *Client) DbtRuns(ctx context.Context, since time.Time, target string) (*History, error) {
	return c.DbtRunsFor(ctx, since, target, nil)
}

// DbtRunsFor is DbtRuns narrowed to nodeIDs when non-empty, pushing the
// node_id filter and build query types into Snowflake SQL to avoid scanning
// unrelated statements or hitting row limits prematurely. It also enriches
// runs with WAREHOUSE_SIZE and (when granted) QUERY_ATTRIBUTION_HISTORY
// compute credits, groups multi-statement dbt bursts, and computes run counts
// and medians.
func (c *Client) DbtRunsFor(ctx context.Context, since time.Time, target string, nodeIDs []string) (*History, error) {
	now := time.Now().UTC()
	if since.IsZero() {
		since = now.AddDate(0, 0, -HistoryDays)
	} else {
		since = since.UTC()
	}
	days := max(int(now.Sub(since).Hours()/24+0.5), 1)
	sinceLit := since.Format("2006-01-02 15:04:05 +0000")

	nodeFilter := ""
	if len(nodeIDs) > 0 {
		var clauses []string
		for _, id := range nodeIDs {
			if id != "" {
				clauses = append(clauses, "CONTAINS(QUERY_TEXT, "+quoteLiteral(`"`+id+`"`)+")")
			}
		}
		if len(clauses) > 0 {
			nodeFilter = "\n  AND (" + strings.Join(clauses, " OR ") + ")"
		}
	}

	baseCTE := fmt.Sprintf(`SELECT
  QUERY_ID,
  QUERY_TEXT,
  QUERY_TYPE,
  WAREHOUSE_NAME,
  WAREHOUSE_SIZE,
  DATE_PART(epoch_millisecond, START_TIME) AS START_MS,
  TOTAL_ELAPSED_TIME,
  BYTES_SCANNED,
  ROWS_PRODUCED
FROM SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY
WHERE EXECUTION_STATUS = 'SUCCESS'
  AND START_TIME >= TO_TIMESTAMP_TZ('%s', 'YYYY-MM-DD HH24:MI:SS TZHTZM')
  AND (QUERY_TYPE IN (%s) OR STARTSWITH(QUERY_TYPE, 'CREATE_'))
  AND CONTAINS(QUERY_TEXT, '"app"')
  AND CONTAINS(QUERY_TEXT, '"dbt"')%s
ORDER BY START_TIME DESC
LIMIT %d`, sinceLit, buildTypesSQL, nodeFilter, maxHistoryRows)

	attrSQL := fmt.Sprintf(`WITH q AS (
%s
)
SELECT
  q.QUERY_ID,
  q.QUERY_TEXT,
  q.QUERY_TYPE,
  q.WAREHOUSE_NAME,
  q.WAREHOUSE_SIZE,
  q.START_MS,
  q.TOTAL_ELAPSED_TIME,
  q.BYTES_SCANNED,
  q.ROWS_PRODUCED,
  a.CREDITS_ATTRIBUTED_COMPUTE AS CREDITS
FROM q
LEFT JOIN SNOWFLAKE.ACCOUNT_USAGE.QUERY_ATTRIBUTION_HISTORY a
  ON a.QUERY_ID = q.QUERY_ID
ORDER BY q.START_MS DESC`, baseCTE)

	rowCap := maxHistoryRows
	st, err := c.execute(ctx, attrSQL)
	if err != nil && ctx.Err() == nil && !errors.Is(err, warehouse.ErrAuth) {
		// QUERY_ATTRIBUTION_HISTORY may not be granted or available; try
		// plain ACCOUNT_USAGE.QUERY_HISTORY before falling back to 7-day
		// INFORMATION_SCHEMA.
		var se *StatementError
		if errors.As(err, &se) {
			if st2, err2 := c.execute(ctx, baseCTE); err2 == nil {
				st, err = st2, nil
			} else {
				err = err2
			}
		}
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, warehouse.ErrAuth) {
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
  WAREHOUSE_SIZE,
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
  AND CONTAINS(QUERY_TEXT, '"dbt"')%s
ORDER BY START_TIME DESC`, fbLit, infoSchemaMaxRows, nodeFilter)
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
		rowCap = infoSchemaMaxRows
	} else if err != nil {
		return nil, mapStatementError(err)
	}

	h := &History{
		Runs:      map[string]Run{},
		Targets:   map[string]int{},
		Since:     since,
		Days:      days,
		Truncated: len(st.Data) >= rowCap,
		stmts:     map[string][]Run{},
	}
	idx := st.colIndex()
	h.Queries = len(st.Data)
	for _, row := range st.Data {
		h.addRow(row, idx, target)
	}
	h.finalize()
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
	credits, _ := strconv.ParseFloat(cellStr(row, idx, "credits"), 64)

	r := Run{
		NodeID:        comment.Node,
		Target:        comment.Target,
		QueryID:       cellStr(row, idx, "query_id"),
		Warehouse:     cellStr(row, idx, "warehouse_name"),
		WarehouseSize: cellStr(row, idx, "warehouse_size"),
		Started:       time.UnixMilli(startMS),
		Duration:      time.Duration(elapsedMS) * time.Millisecond,
		ReadBytes:     bytesScanned,
		ProducedRows:  rowsProduced,
		Credits:       credits,
		StatementType: kind,
	}
	h.stmts[r.NodeID] = append(h.stmts[r.NodeID], r)
}

func (h *History) finalize() {
	for nodeID, stmts := range h.stmts {
		bursts := groupSnowflakeBursts(stmts)
		if len(bursts) == 0 {
			continue
		}
		latest := bursts[len(bursts)-1]
		latest.RunCount = len(bursts)
		durs := make([]time.Duration, len(bursts))
		bytes := make([]int64, len(bursts))
		for i, b := range bursts {
			durs[i] = b.Duration
			bytes[i] = b.ReadBytes
		}
		slices.Sort(durs)
		slices.Sort(bytes)
		latest.MedianDuration = medianDuration(durs)
		latest.MedianReadBytes = medianInt64(bytes)
		h.Runs[nodeID] = latest
	}
}

func medianDuration(sorted []time.Duration) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return sorted[n/2-1]/2 + sorted[n/2]/2 + (sorted[n/2-1]%2+sorted[n/2]%2)/2
}

func medianInt64(sorted []int64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return sorted[n/2-1]/2 + sorted[n/2]/2 + (sorted[n/2-1]%2+sorted[n/2]%2)/2
}

func groupSnowflakeBursts(stmts []Run) []Run {
	if len(stmts) == 0 {
		return nil
	}
	sorted := slices.Clone(stmts)
	slices.SortStableFunc(sorted, func(a, b Run) int {
		return cmp.Or(a.Started.Compare(b.Started), cmp.Compare(a.QueryID, b.QueryID))
	})
	var bursts []Run
	cur := sorted[0]
	cur.StatementCount = 1
	heavyBytes := cur.ReadBytes
	curEnd := cur.Started.Add(cur.Duration)
	for _, s := range sorted[1:] {
		if !s.Started.After(curEnd.Add(burstGap)) {
			cur.Duration += s.Duration
			cur.ReadBytes += s.ReadBytes
			cur.ProducedRows += s.ProducedRows
			cur.Credits += s.Credits
			cur.StatementCount++
			if cur.Warehouse == "" {
				cur.Warehouse = s.Warehouse
			}
			if cur.WarehouseSize == "" {
				cur.WarehouseSize = s.WarehouseSize
			}
			if s.ReadBytes > heavyBytes || (s.ReadBytes == heavyBytes && s.Started.After(cur.Started)) {
				heavyBytes = s.ReadBytes
				cur.QueryID = s.QueryID
				cur.StatementType = s.StatementType
			}
			if end := s.Started.Add(s.Duration); end.After(curEnd) {
				curEnd = end
			}
			continue
		}
		bursts = append(bursts, cur)
		cur = s
		cur.StatementCount = 1
		heavyBytes = s.ReadBytes
		curEnd = s.Started.Add(s.Duration)
	}
	return append(bursts, cur)
}
