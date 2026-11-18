package databricks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
)

// Run is a statement that built a dbt node, as Query History has it.
type Run struct {
	NodeID, Target string
	QueryID        string
	// Warehouse is the SQL warehouse it ran on: its name when the
	// workspace lists it, else its ID.
	Warehouse string
	Started   time.Time
	Duration  time.Duration
	// ReadBytes and ReadRows are what it read; ProducedRows what it wrote
	// or returned.
	ReadBytes, ReadRows, ProducedRows int64
	StatementType                     string
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
	Since     time.Time
	Truncated bool
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
	Warehouse     string `json:"warehouse_id"`
	Endpoint      string `json:"endpoint_id"`
	Start         int64  `json:"query_start_time_ms"`
	Duration      int64  `json:"duration"`
	RowsProduced  int64  `json:"rows_produced"`
	Metrics       *struct {
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
	h := &History{Runs: map[string]Run{}, Targets: map[string]int{}, Since: since}
	names := c.warehouseNames(ctx)
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
			h.add(q, target, names)
		}
		if token = next; token == "" {
			break
		}
	}
	return h, nil
}

func (h *History) add(q historyQuery, target string, names map[string]string) {
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
	if n := names[r.Warehouse]; n != "" {
		r.Warehouse = n
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
	if old, ok := h.Runs[r.NodeID]; !ok || r.Started.After(old.Started) {
		h.Runs[r.NodeID] = r
	}
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

// warehouseNames are the workspace's SQL warehouses' names by ID; empty
// when they can't be listed.
func (c *Client) warehouseNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	list, err := c.Warehouses(ctx)
	if err != nil {
		return out
	}
	for _, w := range list {
		out[w.ID] = w.Name
	}
	return out
}
