package databricks

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// Cost on Databricks is a model's last run (ADR 0042): Databricks bills
// SQL warehouse time, not bytes scanned, so there is nothing to estimate
// before a run. Query History, which answers without a SQL warehouse, keeps
// what each query did; dbt marks the queries it runs with a comment naming
// the node and the target, on by default:
//
//	/* {"app": "dbt", "dbt_version": "1.9.0", "profile_name": "shop",
//	    "target_name": "prod", "node_id": "model.shop.orders"} */

const (
	// HistoryDays is how far back Query History is read: it keeps 30 days.
	HistoryDays = 30
	// historyPage is how many queries a page of history holds, its most.
	historyPage = 1000
	// maxHistoryPages bounds a read of a busy workspace's history.
	maxHistoryPages = 25
	// burstGap merges consecutive build statements of the same dbt node that
	// execute back-to-back (e.g. CREATE TEMP + MERGE) into one model run.
	burstGap = 2 * time.Minute
	// minRunsForEarlyExit stops paginating once every requested changed node
	// has at least this many distinct runs in history.
	minRunsForEarlyExit = 5
)

// Run is a dbt node's latest build burst (and cadence/medians over the window),
// as Query History has it.
type Run struct {
	NodeID, Target string
	QueryID        string
	// Warehouse is the SQL warehouse it ran on: its name when the
	// workspace lists it, else its ID.
	Warehouse     string
	WarehouseSize string
	Serverless    bool
	Started       time.Time
	Duration      time.Duration
	// ReadBytes and ReadRows are what it read; ProducedRows what it wrote
	// or returned.
	ReadBytes, ReadRows, ProducedRows int64
	StatementType                     string
	StatementCount                    int
	RunCount                          int
	MedianDuration                    time.Duration
	MedianReadBytes                   int64
}

// History is what a read of Query History found.
type History struct {
	// Runs are each dbt node's latest build with the target asked for, by
	// node ID (model.package.name).
	Runs map[string]Run
	// Queries is how many queries were read, and Dbt how many of them
	// carried dbt's query comment, with any target.
	Queries, Dbt int
	// Targets counts the dbt queries by target, to say which were seen when
	// none had the one asked for.
	Targets map[string]int
	// Since is how far back it read; Truncated says it stopped at its page
	// limit, or on an error after the first page, before reaching Since.
	// Sampled says pagination stopped early once every requested model had
	// enough runs for median/cadence stats.
	Since     time.Time
	Truncated bool
	Sampled   bool

	stmts map[string][]Run
}

// dbtComment is dbt's query comment.
var dbtComment = regexp.MustCompile(`/\*\s*(\{[^*]*?"app"\s*:\s*"dbt"[^*]*?\})\s*\*/`)

// buildTypes are the statement types that build a table or view; dbt's
// describes, shows and grants also carry the node's comment.
var buildTypes = map[string]bool{"CREATE": true, "REPLACE": true, "MERGE": true, "INSERT": true, "COPY": true, "UPDATE": true, "DELETE": true}

type historyQuery struct {
	ID            string `json:"query_id"`
	Status        string `json:"status"`
	Text          string `json:"query_text"`
	StatementType string `json:"statement_type"`
	UserName      string `json:"user_name"`
	ExecutedAs    string `json:"executed_as_user_name"`
	ChannelUsed   *struct {
		Name string `json:"name"`
	} `json:"channel_used"`
	Warehouse    string `json:"warehouse_id"`
	Endpoint     string `json:"endpoint_id"`
	Start        int64  `json:"query_start_time_ms"`
	Duration     int64  `json:"duration"`
	RowsProduced int64  `json:"rows_produced"`
	Metrics      *struct {
		ReadBytes    int64 `json:"read_bytes"`
		RowsRead     int64 `json:"rows_read_count"`
		RowsProduced int64 `json:"rows_produced_count"`
		TotalTime    int64 `json:"total_time_ms"`
	} `json:"metrics"`
}

// DbtRuns reads Query History back to since, without a SQL warehouse, and
// keeps each dbt node's latest finished build whose comment names target
// ("" for any).
func (c *Client) DbtRuns(ctx context.Context, since time.Time, target string) (*History, error) {
	return c.DbtRunsFor(ctx, since, target, nil)
}

// DbtRunsFor is DbtRuns that stops paginating early once every requested node
// in nodeIDs has enough runs for median/cadence stats, enriches runs with SQL
// warehouse size and serverless metadata, and merges multi-statement dbt
// bursts into a single run per build.
func (c *Client) DbtRunsFor(ctx context.Context, since time.Time, target string, nodeIDs []string) (*History, error) {
	h := &History{Runs: map[string]Run{}, Targets: map[string]int{}, Since: since, stmts: map[string][]Run{}}
	warehouses := c.warehouseInfo(ctx)
	var wanted []string
	for _, id := range nodeIDs {
		if id != "" {
			wanted = append(wanted, id)
		}
	}
	token := ""
	for page := 0; ; page++ {
		if page == maxHistoryPages {
			h.Truncated = true
			break
		}
		queries, next, err := c.historyPage(ctx, since, token)
		if err != nil {
			if page == 0 {
				return nil, err
			}
			h.Truncated = true
			break
		}
		h.Queries += len(queries)
		for _, q := range queries {
			h.add(q, target, warehouses)
		}
		if token = next; token == "" {
			break
		}
		if len(wanted) > 0 && h.enoughForAll(wanted) {
			h.Sampled = true
			break
		}
	}
	h.finalize()
	return h, nil
}

func (h *History) enoughForAll(wanted []string) bool {
	for _, id := range wanted {
		if len(groupDatabricksBursts(h.stmts[id])) < minRunsForEarlyExit {
			return false
		}
	}
	return true
}

func (h *History) add(q historyQuery, target string, warehouses map[string]SQLWarehouse) {
	m := dbtComment.FindStringSubmatch(q.Text)
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
	kind := strings.ToUpper(q.StatementType)
	if kind == "" || kind == "OTHER" {
		kind, _, _ = strings.Cut(warehouse.StatementType(strings.Replace(q.Text, m[0], "", 1)), "_")
	}
	if (q.Status != "" && q.Status != "FINISHED") || !buildTypes[kind] || (target != "" && comment.Target != target) {
		return
	}
	r := Run{NodeID: comment.Node, Target: comment.Target, QueryID: q.ID, Warehouse: q.Warehouse, StatementType: kind,
		Started: time.UnixMilli(q.Start), Duration: time.Duration(q.Duration) * time.Millisecond, ProducedRows: q.RowsProduced}
	if r.Warehouse == "" {
		r.Warehouse = q.Endpoint
	}
	if info, ok := warehouses[r.Warehouse]; ok {
		if info.Name != "" {
			r.Warehouse = info.Name
		}
		r.WarehouseSize = info.Size
		r.Serverless = info.Serverless
	}
	if mt := q.Metrics; mt != nil {
		r.ReadBytes, r.ReadRows = mt.ReadBytes, mt.RowsRead
		if mt.RowsProduced > 0 {
			r.ProducedRows = mt.RowsProduced
		}
		if r.Duration == 0 {
			r.Duration = time.Duration(mt.TotalTime) * time.Millisecond
		}
	}
	h.stmts[r.NodeID] = append(h.stmts[r.NodeID], r)
}

func (h *History) finalize() {
	for nodeID, stmts := range h.stmts {
		bursts := groupDatabricksBursts(stmts)
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

func groupDatabricksBursts(stmts []Run) []Run {
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
			cur.ReadRows += s.ReadRows
			cur.ProducedRows += s.ProducedRows
			cur.StatementCount++
			if cur.Warehouse == "" {
				cur.Warehouse = s.Warehouse
				cur.WarehouseSize = s.WarehouseSize
				cur.Serverless = s.Serverless
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

// historyPage reads a page of finished queries since since; token is the
// last page's next one, "" for the first. The filter goes as query
// parameters and, as Databricks' examples send it, as a JSON body.
func (c *Client) historyPage(ctx context.Context, since time.Time, token string) ([]historyQuery, string, error) {
	q := url.Values{"max_results": {strconv.Itoa(historyPage)}, "include_metrics": {"true"}}
	body := map[string]any{"max_results": historyPage, "include_metrics": true}
	if token != "" {
		q.Set("page_token", token)
		body["page_token"] = token
	} else {
		start := strconv.FormatInt(since.UnixMilli(), 10)
		q.Set("filter_by.query_start_time_range.start_time_ms", start)
		q.Set("filter_by.statuses", "FINISHED")
		body["filter_by"] = map[string]any{"query_start_time_range": map[string]any{"start_time_ms": since.UnixMilli()}, "statuses": []string{"FINISHED"}}
	}
	data, err := c.do(ctx, http.MethodGet, "/api/2.0/sql/history/queries?"+q.Encode(), body)
	if err != nil {
		return nil, "", err
	}
	var raw struct {
		Res     []historyQuery `json:"res"`
		Next    string         `json:"next_page_token"`
		HasNext *bool          `json:"has_next_page"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, "", fmt.Errorf("decode query history: %w", err)
	}
	if raw.HasNext != nil && !*raw.HasNext {
		raw.Next = ""
	}
	return raw.Res, raw.Next, nil
}

// warehouseInfo returns the workspace's SQL warehouses by ID (name, size,
// serverless); empty when they can't be listed.
func (c *Client) warehouseInfo(ctx context.Context) map[string]SQLWarehouse {
	out := map[string]SQLWarehouse{}
	list, err := c.Warehouses(ctx)
	if err != nil {
		return out
	}
	for _, w := range list {
		out[w.ID] = w
	}
	return out
}
