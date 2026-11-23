package warehouse

import (
	"encoding/json"
	"fmt"
	"time"
)

// TableRef names a table: project.dataset.table in BigQuery,
// catalog.schema.table elsewhere.
type TableRef struct {
	Project string `json:"projectId"`
	Dataset string `json:"datasetId"`
	Table   string `json:"tableId"`
}

// Table is the metadata of a table, view or other table resource.
type Table struct {
	// Type is the warehouse's: TABLE, VIEW, EXTERNAL, MATERIALIZED_VIEW,
	// SNAPSHOT in BigQuery.
	Type         string
	Description  string
	Location     string
	LastModified time.Time
	Schema       []Column
	// Partition is how the table is partitioned; nil when it isn't.
	Partition *Partition
	// ViewQuery is a view's or materialized view's SQL, "" for a table.
	ViewQuery string
}

// Partition is a table's partitioning.
type Partition struct {
	// Column is the column the table is partitioned by; "" for a table
	// partitioned by ingestion time, whose partitions the _PARTITIONTIME
	// and _PARTITIONDATE pseudo-columns name.
	Column string
}

// Check is what a warehouse says of a query it planned without running it.
type Check struct {
	// Bytes is what the query would scan, as a Coster estimates it (what
	// on-demand pricing bills); 0 from a warehouse that doesn't.
	Bytes    int64
	Accuracy string // one of the Accuracy constants; "" when not given
	// StatementType is SELECT, SCRIPT, INSERT, CREATE_TABLE_AS_SELECT and so on.
	StatementType string
	// Schema is the columns a query returns; empty for scripts and
	// statements that return nothing.
	Schema []Column `json:",omitempty"`
	// Tables are the tables the query reads, views resolved to the tables
	// under them. BigQuery lists at most 50.
	Tables []TableRef `json:",omitempty"`
}

// Accuracy of a check's byte estimate.
const (
	AccuracyPrecise    = "PRECISE"
	AccuracyUpperBound = "UPPER_BOUND"
	AccuracyLowerBound = "LOWER_BOUND"
	// AccuracyUnknown means the warehouse could not estimate the bytes and
	// reported 0; the query still scans data when it runs.
	AccuracyUnknown = "UNKNOWN"
)

// OnDemandUSDPerTiB is the on-demand price per TiB scanned of BigQuery, the
// warehouse that bills by bytes.
const OnDemandUSDPerTiB = 6.25

// CostUSD is the on-demand price of scanning bytes.
func CostUSD(bytes int64) float64 {
	return float64(bytes) / (1 << 40) * OnDemandUSDPerTiB
}

// CostUSD is the on-demand price of the bytes the check estimates.
func (c *Check) CostUSD() float64 { return CostUSD(c.Bytes) }

// FormatBytes uses binary units, as BigQuery bills: "0 B", "512 B", "1.20 GiB".
func FormatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// TableListing is a table in a dataset's listing.
type TableListing struct {
	ID string `json:"id"`
	// Type is TABLE, VIEW, EXTERNAL, MATERIALIZED_VIEW or SNAPSHOT.
	Type string `json:"type"`
}

// TableDetail is a table as its hover card shows it.
type TableDetail struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Schema      []Column `json:"schema"`
	// PartitionField is the column the table is partitioned by, "" for
	// ingestion time; PartitionType is DAY, HOUR, MONTH, YEAR or RANGE,
	// "" when it isn't partitioned.
	PartitionField string   `json:"partitionField,omitempty"`
	PartitionType  string   `json:"partitionType,omitempty"`
	ClusterFields  []string `json:"clusterFields,omitempty"`
	// NumRows and NumBytes are -1 for a view, which has neither.
	NumRows  int64     `json:"numRows"`
	NumBytes int64     `json:"numBytes"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// DefaultPageSize is how many rows a page of results holds.
const DefaultPageSize = 500

// QueryOptions describe a query to run.
type QueryOptions struct {
	// Project is billed for the job; Location is where it runs ("" lets
	// the warehouse pick it from the tables the query reads).
	Project, Location string
	SQL               string
	// Labels are put on the job.
	Labels map[string]string
	// Params are named STRING query parameters, @name in the SQL.
	Params map[string]string
	// JobPrefix overrides the BigQuery job ID prefix ("xf_scratch_" when empty).
	JobPrefix string
	// MaximumBytesBilled caps BigQuery on-demand billing for this job (0 = no cap).
	MaximumBytesBilled int64
}

// JobRef names a job.
type JobRef struct {
	Project  string `json:"project"`
	Location string `json:"location,omitempty"`
	ID       string `json:"id"`
}

// QueryStatus is where a query job stands.
type QueryStatus struct {
	// State is PENDING, RUNNING or DONE.
	State string `json:"state"`
	// Error is why a DONE job failed; "" when it did not.
	Error         string `json:"error,omitempty"`
	StatementType string `json:"statementType,omitempty"`
	CacheHit      bool   `json:"cacheHit"`
	// BytesProcessed and BytesBilled are int64s; the page shows them as
	// numbers, which hold them exactly up to 2^53.
	BytesProcessed int64 `json:"bytesProcessed"`
	BytesBilled    int64 `json:"bytesBilled"`
	// DMLRows is the rows an INSERT, UPDATE, DELETE or MERGE changed,
	// -1 when the statement is none of those.
	DMLRows   int64     `json:"dmlRows"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	NumChild  int64     `json:"numChildJobs"`
	Cancelled bool      `json:"cancelled,omitempty"`
	// ResultTable is the table that holds the job's rows (BigQuery keeps
	// one for a day for any query with a result), nil when it has none.
	ResultTable *TableRef `json:"resultTable,omitempty"`
}

// Page is one page of a query's rows.
type Page struct {
	// Schema is the result's columns; nil when the statement has none.
	Schema []Column `json:"schema"`
	// Rows are the page's rows, each a cell per column in the shapes
	// CellText reads.
	Rows [][]any `json:"rows"`
	// NextToken asks for the page after this one; "" when it was the last.
	NextToken string `json:"nextToken,omitempty"`
	// Total is how many rows the result has.
	Total int64 `json:"total"`
	// Complete is false while the job is still running.
	Complete bool `json:"complete"`
}

// CellText words a cell of a Page as one line of text, as a CSV file or the
// clipboard gets it: nested values as JSON, BYTES ({"b64": …, "len": n}) as
// base64.
func CellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any:
		if s, ok := x["b64"].(string); ok {
			return s
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// ChildJob is one statement of a script.
type ChildJob struct {
	ID            string `json:"id"`
	StatementType string `json:"statementType"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	BytesBilled   int64  `json:"bytesBilled"`
	DMLRows       int64  `json:"dmlRows"`
	// Query is the statement's SQL, cut short.
	Query string `json:"query"`
	// Table is the table the statement created or changed, "" when it
	// names none.
	Table string `json:"table,omitempty"`
	// HasRows says the statement has a result to read: a SELECT.
	HasRows bool `json:"hasRows"`
}

// Job is what a finished job did, from its statistics.
type Job struct {
	// Location is where the job ran.
	Location string
	// BytesBilled is what on-demand pricing charges for; BytesProcessed
	// what the job read. A script's are the sums of its statements'.
	BytesBilled, BytesProcessed int64
	// SlotMs is the slot time the job used, in milliseconds.
	SlotMs         int64
	Started, Ended time.Time
	// Done says the job has ended, and Failed that it ended in an error.
	Done, Failed bool
}

// CostUSD is the on-demand price of the bytes the job was billed.
func (j *Job) CostUSD() float64 { return CostUSD(j.BytesBilled) }

// ActiveJob is a job that has not ended.
type ActiveJob struct {
	ID, Location string
	// Query is the SQL the job runs, "" for a job that is not a query.
	Query string
	// Started is when the job began to run, else when it was created.
	Started time.Time
}

// LabelledTable is a table in a dataset's listing, with its labels.
type LabelledTable struct {
	ID     string
	Type   string
	Labels map[string]string
}

// ViewInfo is what defer to prod reads of a table at a Dev Target: whether
// it is a Proxy View, what it reads, and how old it is.
type ViewInfo struct {
	Type         string
	Labels       map[string]string
	Query        string
	LastModified time.Time
}

// ViewSpec is a view to make or repoint.
type ViewSpec struct {
	Project, Dataset, Table string
	// Query is standard SQL.
	Query       string
	Labels      map[string]string
	Description string
}
