// Package warehouse is what xf asks of a data warehouse (ADR 0042): check a
// query, read a table's metadata, run a query and page its rows, and list
// catalogs and tables. What only some warehouses do is a separate interface
// that callers type-assert, as backend.Runner is for backends: Coster,
// ViewWriter and JobLister.
//
// BigQuery (internal/bigquery) and Databricks (internal/databricks)
// implement it. Names follow BigQuery: a TableRef's Project and Dataset are
// a catalog and schema elsewhere.
package warehouse

import (
	"context"
	"time"
)

// Warehouse is what every warehouse does.
type Warehouse interface {
	// Check plans query, billed to project and run in location ("" lets
	// the warehouse pick), without running it: the columns it returns, the
	// tables it reads, or why it fails. A query the warehouse rejects comes
	// back as an *APIError whose Message says why.
	Check(ctx context.Context, project, location, query string) (*Check, error)
	// Table is the metadata of project.dataset.table.
	Table(ctx context.Context, project, dataset, table string) (*Table, error)
	// Describe is the metadata of project.dataset.table in full, as a
	// hover card shows it.
	Describe(ctx context.Context, project, dataset, table string) (*TableDetail, error)

	// StartQuery starts a query and returns at once; QueryJob follows it,
	// Rows pages its result and CancelQuery stops it.
	StartQuery(ctx context.Context, o QueryOptions) (JobRef, error)
	QueryJob(ctx context.Context, ref JobRef) (*QueryStatus, error)
	// Rows reads a page of the rows of ref. token is the previous page's
	// NextToken, "" for the first; size <= 0 is DefaultPageSize.
	Rows(ctx context.Context, ref JobRef, token string, size int) (*Page, error)
	CancelQuery(ctx context.Context, ref JobRef) error

	// ListDatasets lists the datasets of project; ListTables the tables
	// and views of one.
	ListDatasets(ctx context.Context, project string) ([]string, error)
	ListTables(ctx context.Context, project, dataset string) ([]TableListing, error)
	// DefaultProject is the project queries are billed to when nothing
	// else names one, "" when the credentials name none.
	DefaultProject() string
}

// Waker is a warehouse that bills while it runs, as a Databricks SQL
// warehouse does: a Check or a query starts it when it is stopped. xf checks
// on such a warehouse only when asked (ADR 0042), and says when it starts.
type Waker interface {
	// Running says whether the warehouse is up, without starting it.
	Running(ctx context.Context) (bool, error)
}

// Coster is a warehouse that bills a query by the bytes it scans, which its
// Check estimates (Check.Bytes and Check.Accuracy).
type Coster interface {
	// CostUSD is the on-demand price of scanning bytes.
	CostUSD(bytes int64) float64
}

// ViewWriter is a warehouse defer to prod (ADR 0031) can use: it lists a
// dataset's tables with their labels and its routines, says whether a table
// exists, and makes, repoints and removes Proxy Views.
type ViewWriter interface {
	// ListTablesLabelled lists the tables and views of a dataset with
	// their labels. A dataset that doesn't exist fails with IsNotFound.
	ListTablesLabelled(ctx context.Context, project, dataset string) ([]LabelledTable, error)
	// ListRoutines lists the names of a dataset's functions and procedures.
	ListRoutines(ctx context.Context, project, dataset string) ([]string, error)
	// Exists says whether a table, or with routine a function or
	// procedure, exists. A 403 is returned as the error.
	Exists(ctx context.Context, project, dataset, name string, routine bool) (bool, error)
	// View reads a table's labels, view query and age; nil when it
	// doesn't exist.
	View(ctx context.Context, project, dataset, table string) (*ViewInfo, error)
	CreateView(ctx context.Context, v ViewSpec) error
	UpdateView(ctx context.Context, v ViewSpec) error
	// DeleteTable removes a table or view; one already gone is no error.
	DeleteTable(ctx context.Context, project, dataset, table string) error
}

// JobLister is a warehouse whose jobs xf can look up: what a finished job
// did, the caller's jobs still running, and the statements of a script.
type JobLister interface {
	// Job is what the job jobID of project did. location is where it
	// ran, "" when not known.
	Job(ctx context.Context, project, location, jobID string) (*Job, error)
	// ActiveJobs lists the caller's own pending and running jobs in
	// project created since the given time, newest first.
	ActiveJobs(ctx context.Context, project string, since time.Time) ([]ActiveJob, error)
	// ChildJobs lists the statements a script job ran, in the order they ran.
	ChildJobs(ctx context.Context, ref JobRef) ([]ChildJob, error)
}

// ProjectLister is a warehouse that lists the projects (or catalogs, on
// Databricks) the caller can query, for SQL completion without a CLI call.
type ProjectLister interface {
	ListProjects(ctx context.Context) ([]string, error)
}
