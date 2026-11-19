// Package bigquery calls the BigQuery REST API with Application Default
// Credentials: the login `gcloud auth application-default login` writes, or
// the service account $GOOGLE_APPLICATION_CREDENTIALS points at. It is
// deliberately small: table metadata, query dry runs and the statistics of
// finished jobs, no client library. Nothing it does reads table data or runs
// a query.
//
// The Client is xf's warehouse.Warehouse, and its Coster, ViewWriter and
// JobLister: cmd/xf makes it, and nothing else imports this package.
package bigquery

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	defaultBaseURL = "https://bigquery.googleapis.com"
	// scope is the full BigQuery scope, since a dry run is a jobs.insert
	// call; a dry run job only plans the query.
	scope = "https://www.googleapis.com/auth/bigquery"
	// tableFields limits the response to what Table carries.
	tableFields = "type,description,location,lastModifiedTime,schema,timePartitioning(field),rangePartitioning(field),view(query),materializedView(query)"
)

// Client makes authenticated calls to the BigQuery API.
type Client struct {
	http         *http.Client
	baseURL      string
	quotaProject string
	// creds, when set, are the ADC the client follows (gcpauth.Credentials),
	// whose quota project goes in place of quotaProject.
	creds *gcpauth.Credentials
}

// NewClient finds Application Default Credentials. It fails with
// warehouse.ErrAuth when there are none. Once made, it follows them: a new login is used
// from the next call.
func NewClient(context.Context) (*Client, error) {
	creds, err := gcpauth.NewCredentials(scope)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", warehouse.ErrAuth, err)
	}
	c := newClient(creds.Client(), defaultBaseURL, "")
	c.creds = creds
	return c, nil
}

// NewClientWithHTTP makes a client that calls the API at baseURL with
// httpClient, which does its own authorizing: for tests, and for pointing
// xf at an emulator.
func NewClientWithHTTP(httpClient *http.Client, baseURL, quotaProject string) *Client {
	return newClient(httpClient, baseURL, quotaProject)
}

// The Client is a warehouse that prices queries by the bytes they scan,
// holds Proxy Views for defer to prod, and lists its jobs.
var (
	_ warehouse.Warehouse     = (*Client)(nil)
	_ warehouse.Coster        = (*Client)(nil)
	_ warehouse.ViewWriter    = (*Client)(nil)
	_ warehouse.JobLister     = (*Client)(nil)
	_ warehouse.ProjectLister = (*Client)(nil)
)

// DefaultProject is QuotaProject.
func (c *Client) DefaultProject() string { return c.QuotaProject() }

// CostUSD is BigQuery's on-demand price of scanning bytes.
func (c *Client) CostUSD(bytes int64) float64 { return warehouse.CostUSD(bytes) }

func newClient(httpClient *http.Client, baseURL, quotaProject string) *Client {
	return &Client{http: httpClient, baseURL: baseURL, quotaProject: quotaProject}
}

// QuotaProject is the project of the Application Default Credentials (their
// quota_project_id), "" when they name none: where a Google client library
// runs jobs it is given no project for.
func (c *Client) QuotaProject() string {
	if c.creds != nil {
		return c.creds.QuotaProject()
	}
	return c.quotaProject
}

// Table fetches the metadata of project.dataset.table.
func (c *Client) Table(ctx context.Context, project, dataset, table string) (*warehouse.Table, error) {
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s?fields=%s",
		c.baseURL, url.PathEscape(project), url.PathEscape(dataset), url.PathEscape(table), url.QueryEscape(tableFields))
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Type             string `json:"type"`
		Description      string `json:"description"`
		Location         string `json:"location"`
		LastModifiedTime string `json:"lastModifiedTime"`
		Schema           struct {
			Fields []warehouse.Column `json:"fields"`
		} `json:"schema"`
		TimePartitioning *struct {
			Field string `json:"field"`
		} `json:"timePartitioning"`
		RangePartitioning *struct {
			Field string `json:"field"`
		} `json:"rangePartitioning"`
		View *struct {
			Query string `json:"query"`
		} `json:"view"`
		MaterializedView *struct {
			Query string `json:"query"`
		} `json:"materializedView"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode table metadata: %w", err)
	}
	t := &warehouse.Table{Type: raw.Type, Description: raw.Description, Location: raw.Location, Schema: raw.Schema.Fields}
	switch {
	case raw.TimePartitioning != nil:
		t.Partition = &warehouse.Partition{Column: raw.TimePartitioning.Field}
	case raw.RangePartitioning != nil:
		t.Partition = &warehouse.Partition{Column: raw.RangePartitioning.Field}
	}
	switch {
	case raw.View != nil:
		t.ViewQuery = raw.View.Query
	case raw.MaterializedView != nil:
		t.ViewQuery = raw.MaterializedView.Query
	}
	if ms, err := strconv.ParseInt(raw.LastModifiedTime, 10, 64); err == nil {
		t.LastModified = time.UnixMilli(ms).UTC()
	}
	return t, nil
}

// Job fetches the statistics of the job jobID of project. location is
// where it ran: BigQuery needs it for jobs outside the US and EU
// multi-regions, and "" leaves it out.
func (c *Client) Job(ctx context.Context, project, location, jobID string) (*warehouse.Job, error) {
	q := url.Values{"fields": {"jobReference(location),status(state,errorResult(reason)),statistics(startTime,endTime,totalBytesProcessed,totalSlotMs,query(totalBytesBilled,totalBytesProcessed))"}}
	if location != "" {
		q.Set("location", location)
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs/%s?%s", c.baseURL, url.PathEscape(project), url.PathEscape(jobID), q.Encode())
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// The API spells int64 as a string.
	var raw struct {
		JobReference struct {
			Location string `json:"location"`
		} `json:"jobReference"`
		Status struct {
			State       string `json:"state"`
			ErrorResult *struct {
				Reason string `json:"reason"`
			} `json:"errorResult"`
		} `json:"status"`
		Statistics struct {
			StartTime           int64 `json:"startTime,string"`
			EndTime             int64 `json:"endTime,string"`
			TotalBytesProcessed int64 `json:"totalBytesProcessed,string"`
			TotalSlotMs         int64 `json:"totalSlotMs,string"`
			Query               struct {
				TotalBytesBilled    int64 `json:"totalBytesBilled,string"`
				TotalBytesProcessed int64 `json:"totalBytesProcessed,string"`
			} `json:"query"`
		} `json:"statistics"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode job: %w", err)
	}
	st := raw.Statistics
	j := &warehouse.Job{
		Location:       cmp.Or(raw.JobReference.Location, location),
		BytesBilled:    st.Query.TotalBytesBilled,
		BytesProcessed: cmp.Or(st.TotalBytesProcessed, st.Query.TotalBytesProcessed),
		SlotMs:         st.TotalSlotMs,
		Done:           raw.Status.State == "DONE",
		Failed:         raw.Status.ErrorResult != nil,
	}
	if st.StartTime != 0 {
		j.Started = time.UnixMilli(st.StartTime).UTC()
	}
	if st.EndTime != 0 {
		j.Ended = time.UnixMilli(st.EndTime).UTC()
	}
	return j, nil
}

// activeJobPages is how many pages of jobs ActiveJobs reads at most.
const activeJobPages = 5

// ActiveJobs lists the caller's own pending and running jobs in project
// that were created since the given time, newest first.
func (c *Client) ActiveJobs(ctx context.Context, project string, since time.Time) ([]warehouse.ActiveJob, error) {
	q := url.Values{
		"projection":      {"full"},
		"stateFilter":     {"pending", "running"},
		"minCreationTime": {strconv.FormatInt(since.UnixMilli(), 10)},
		"maxResults":      {"200"},
		"fields":          {"nextPageToken,jobs(jobReference(jobId,location),configuration(query(query)),statistics(creationTime,startTime))"},
	}
	var jobs []warehouse.ActiveJob
	for range activeJobPages {
		u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs?%s", c.baseURL, url.PathEscape(project), q.Encode())
		body, err := c.do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Jobs          []struct {
				JobReference struct {
					JobID    string `json:"jobId"`
					Location string `json:"location"`
				} `json:"jobReference"`
				Configuration struct {
					Query struct {
						Query string `json:"query"`
					} `json:"query"`
				} `json:"configuration"`
				Statistics struct {
					CreationTime int64 `json:"creationTime,string"`
					StartTime    int64 `json:"startTime,string"`
				} `json:"statistics"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode jobs: %w", err)
		}
		for _, j := range raw.Jobs {
			a := warehouse.ActiveJob{ID: j.JobReference.JobID, Location: j.JobReference.Location, Query: j.Configuration.Query.Query}
			if ms := cmp.Or(j.Statistics.StartTime, j.Statistics.CreationTime); ms != 0 {
				a.Started = time.UnixMilli(ms).UTC()
			}
			jobs = append(jobs, a)
		}
		if raw.NextPageToken == "" {
			break
		}
		q.Set("pageToken", raw.NextPageToken)
	}
	return jobs, nil
}

// Check asks BigQuery to plan query (GoogleSQL), with a dry run billed to project,
// without running it. location is the job's location ("" lets BigQuery pick
// it from the tables the query reads). A query BigQuery rejects comes back as
// a *warehouse.APIError whose Message says why, usually with a [line:column].
func (c *Client) Check(ctx context.Context, project, location, query string) (*warehouse.Check, error) {
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs?fields=%s",
		c.baseURL, url.PathEscape(project), url.QueryEscape("statistics(totalBytesProcessed,query(totalBytesProcessedAccuracy,statementType,schema,referencedTables))"))
	type jobRef struct {
		ProjectID string `json:"projectId"`
		Location  string `json:"location,omitempty"`
	}
	var req struct {
		JobReference  jobRef `json:"jobReference"`
		Configuration struct {
			DryRun bool `json:"dryRun"`
			Query  struct {
				Query        string `json:"query"`
				UseLegacySQL bool   `json:"useLegacySql"`
			} `json:"query"`
		} `json:"configuration"`
	}
	req.JobReference = jobRef{ProjectID: project, Location: location}
	req.Configuration.DryRun = true
	req.Configuration.Query.Query = query
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodPost, u, reqBody)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Statistics struct {
			TotalBytesProcessed string `json:"totalBytesProcessed"`
			Query               struct {
				TotalBytesProcessedAccuracy string `json:"totalBytesProcessedAccuracy"`
				StatementType               string `json:"statementType"`
				Schema                      struct {
					Fields []warehouse.Column `json:"fields"`
				} `json:"schema"`
				ReferencedTables []warehouse.TableRef `json:"referencedTables"`
			} `json:"query"`
		} `json:"statistics"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode dry run: %w", err)
	}
	q := raw.Statistics.Query
	d := &warehouse.Check{Accuracy: q.TotalBytesProcessedAccuracy, StatementType: q.StatementType, Schema: q.Schema.Fields, Tables: q.ReferencedTables}
	if s := raw.Statistics.TotalBytesProcessed; s != "" {
		if d.Bytes, err = strconv.ParseInt(s, 10, 64); err != nil {
			return nil, fmt.Errorf("decode dry run: totalBytesProcessed %q", s)
		}
	}
	return d, nil
}

// do sends an API request (with a JSON body when body is non-nil) and
// returns the body of a 200 response.
func (c *Client) do(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if quota := c.QuotaProject(); quota != "" {
		req.Header.Set("X-Goog-User-Project", quota)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, gcpauth.ErrNoCredentials) {
			return nil, fmt.Errorf("%w: %v", warehouse.ErrAuth, err)
		}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return nil, fmt.Errorf("%w: %s", warehouse.ErrAuth, retrieveReason(re))
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// apiError turns a BigQuery error response into an error carrying its
// message.
func apiError(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", warehouse.ErrAuth, msg)
	}
	if status == http.StatusForbidden {
		if hint := RoleHint(msg); hint != "" {
			msg += " (" + hint + ")"
		}
	}
	return &warehouse.APIError{Status: status, Message: msg}
}

var (
	jobsCreateRE = regexp.MustCompile(`bigquery\.jobs\.create permission in project ([\w.:-]+?)\.?(?:\s|$)`)
	tableReadRE  = regexp.MustCompile(`(?:permission to query table|Permission bigquery\.tables\.(?:get|getData) denied on table) ([\w.:-]+)`)
	datasetRE    = regexp.MustCompile(`Permission bigquery\.\w+\.\w+ denied on dataset ([\w.:-]+)`)
)

// RoleHint says which role an access-denied message from BigQuery lacks,
// and where: "needs roles/bigquery.jobUser on acme-dev". "" when the
// message isn't one it knows.
func RoleHint(msg string) string {
	if m := jobsCreateRE.FindStringSubmatch(msg); m != nil {
		return "needs roles/bigquery.jobUser on " + m[1]
	}
	if m := tableReadRE.FindStringSubmatch(msg); m != nil {
		return "needs roles/bigquery.dataViewer on the dataset " + datasetOf(m[1])
	}
	if m := datasetRE.FindStringSubmatch(msg); m != nil {
		return "needs roles/bigquery.dataViewer on the dataset " + strings.Replace(m[1], ":", ".", 1)
	}
	return ""
}

// datasetOf is the dataset of "project:dataset.table" (or
// project.dataset.table), as project.dataset.
func datasetOf(table string) string {
	table = strings.Replace(table, ":", ".", 1)
	if i := strings.LastIndex(table, "."); i > 0 {
		return table[:i]
	}
	return table
}

func retrieveReason(re *oauth2.RetrieveError) string {
	switch {
	case re.ErrorDescription != "":
		return re.ErrorDescription
	case re.ErrorCode != "":
		return re.ErrorCode
	}
	return "token refresh failed"
}
