package bigquery

// Running queries: start a job, follow it, read its rows a page at a time
// and cancel it. Values stay as BigQuery spells them (strings), except that
// they are turned into one Go shape per column type by Cell, so nothing is
// squeezed through a float64 on its way to the page.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ashish10alex/xf/internal/warehouse"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// StartQuery creates a query job and returns at once, without waiting for
// it to finish. Follow it with QueryJob.
func (c *Client) StartQuery(ctx context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error) {
	prefix := o.JobPrefix
	if prefix == "" {
		prefix = "xf_scratch_"
	}
	ref := warehouse.JobRef{Project: o.Project, Location: o.Location, ID: prefix + randomID()}
	type jobRef struct {
		ProjectID string `json:"projectId"`
		Location  string `json:"location,omitempty"`
		JobID     string `json:"jobId"`
	}
	req := struct {
		JobReference  jobRef `json:"jobReference"`
		Configuration struct {
			Labels map[string]string `json:"labels,omitempty"`
			Query  struct {
				Query              string           `json:"query"`
				UseLegacySQL       bool             `json:"useLegacySql"`
				ParameterMode      string           `json:"parameterMode,omitempty"`
				QueryParameters    []queryParameter `json:"queryParameters,omitempty"`
				MaximumBytesBilled string           `json:"maximumBytesBilled,omitempty"`
			} `json:"query"`
		} `json:"configuration"`
	}{JobReference: jobRef{ProjectID: o.Project, Location: o.Location, JobID: ref.ID}}
	req.Configuration.Labels = o.Labels
	req.Configuration.Query.Query = o.SQL
	if o.MaximumBytesBilled > 0 {
		req.Configuration.Query.MaximumBytesBilled = strconv.FormatInt(o.MaximumBytesBilled, 10)
	}
	if len(o.Params) > 0 {
		req.Configuration.Query.ParameterMode = "NAMED"
		for _, name := range sortedKeys(o.Params) {
			req.Configuration.Query.QueryParameters = append(req.Configuration.Query.QueryParameters, queryParameter{
				Name: name, ParameterType: struct {
					Type string `json:"type"`
				}{"STRING"}, ParameterValue: struct {
					Value string `json:"value"`
				}{o.Params[name]},
			})
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ref, err
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs?fields=%s", c.baseURL, url.PathEscape(o.Project), url.QueryEscape("jobReference(location)"))
	resp, err := c.do(ctx, http.MethodPost, u, body)
	if err != nil {
		return ref, err
	}
	var raw struct {
		JobReference struct {
			Location string `json:"location"`
		} `json:"jobReference"`
	}
	if err := json.Unmarshal(resp, &raw); err == nil && raw.JobReference.Location != "" {
		ref.Location = raw.JobReference.Location
	}
	return ref, nil
}

type queryParameter struct {
	Name          string `json:"name"`
	ParameterType struct {
		Type string `json:"type"`
	} `json:"parameterType"`
	ParameterValue struct {
		Value string `json:"value"`
	} `json:"parameterValue"`
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// QueryJob reads the status of a query job.
func (c *Client) QueryJob(ctx context.Context, ref warehouse.JobRef) (*warehouse.QueryStatus, error) {
	q := url.Values{"fields": {"status(state,errorResult(reason,message)),configuration(query(destinationTable)),statistics(startTime,endTime,numChildJobs,query(cacheHit,statementType,totalBytesProcessed,totalBytesBilled,numDmlAffectedRows,dmlStats))"}}
	if ref.Location != "" {
		q.Set("location", ref.Location)
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs/%s?%s", c.baseURL, url.PathEscape(ref.Project), url.PathEscape(ref.ID), q.Encode())
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Status struct {
			State       string `json:"state"`
			ErrorResult *struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errorResult"`
		} `json:"status"`
		Configuration struct {
			Query struct {
				DestinationTable warehouse.TableRef `json:"destinationTable"`
			} `json:"query"`
		} `json:"configuration"`
		Statistics struct {
			StartTime    string `json:"startTime"`
			EndTime      string `json:"endTime"`
			NumChildJobs string `json:"numChildJobs"`
			Query        struct {
				CacheHit            bool   `json:"cacheHit"`
				StatementType       string `json:"statementType"`
				TotalBytesProcessed string `json:"totalBytesProcessed"`
				TotalBytesBilled    string `json:"totalBytesBilled"`
				NumDmlAffectedRows  string `json:"numDmlAffectedRows"`
				DMLStats            *struct {
					Inserted string `json:"insertedRowCount"`
					Deleted  string `json:"deletedRowCount"`
					Updated  string `json:"updatedRowCount"`
				} `json:"dmlStats"`
			} `json:"query"`
		} `json:"statistics"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode job: %w", err)
	}
	st, qs := raw.Statistics, raw.Statistics.Query
	s := &warehouse.QueryStatus{
		State: raw.Status.State, StatementType: qs.StatementType, CacheHit: qs.CacheHit,
		BytesProcessed: atoi(qs.TotalBytesProcessed), BytesBilled: atoi(qs.TotalBytesBilled),
		DMLRows: -1, NumChild: atoi(st.NumChildJobs),
	}
	if qs.NumDmlAffectedRows != "" {
		s.DMLRows = atoi(qs.NumDmlAffectedRows)
	} else if d := qs.DMLStats; d != nil {
		s.DMLRows = atoi(d.Inserted) + atoi(d.Deleted) + atoi(d.Updated)
	}
	if ms := atoi(st.StartTime); ms != 0 {
		s.Started = time.UnixMilli(ms).UTC()
	}
	if ms := atoi(st.EndTime); ms != 0 {
		s.Ended = time.UnixMilli(ms).UTC()
	}
	if t := raw.Configuration.Query.DestinationTable; t.Table != "" {
		s.ResultTable = &t
	}
	if e := raw.Status.ErrorResult; e != nil {
		s.Error = e.Message
		if s.Error == "" {
			s.Error = e.Reason
		}
		s.Cancelled = e.Reason == "stopped"
	}
	return s, nil
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// CancelQuery asks BigQuery to stop a job. The job may already be done.
func (c *Client) CancelQuery(ctx context.Context, ref warehouse.JobRef) error {
	q := url.Values{"fields": {"job/status"}}
	if ref.Location != "" {
		q.Set("location", ref.Location)
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs/%s/cancel?%s", c.baseURL, url.PathEscape(ref.Project), url.PathEscape(ref.ID), q.Encode())
	_, err := c.do(ctx, http.MethodPost, u, []byte("{}"))
	return err
}

// ChildJobs lists the statements a script job ran, in the order they ran.
func (c *Client) ChildJobs(ctx context.Context, ref warehouse.JobRef) ([]warehouse.ChildJob, error) {
	var out []warehouse.ChildJob
	token := ""
	for page := 0; page < 10; page++ {
		q := url.Values{
			"parentJobId": {ref.ID}, "maxResults": {"200"}, "projection": {"full"},
			"fields": {"nextPageToken,jobs(jobReference(jobId),status(state,errorResult(message)),statistics(creationTime,query(statementType,totalBytesBilled,numDmlAffectedRows,ddlTargetTable)),configuration(query(query)))"},
		}
		if ref.Location != "" {
			q.Set("location", ref.Location)
		}
		if token != "" {
			q.Set("pageToken", token)
		}
		u := fmt.Sprintf("%s/bigquery/v2/projects/%s/jobs?%s", c.baseURL, url.PathEscape(ref.Project), q.Encode())
		body, err := c.do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Jobs          []struct {
				JobReference struct {
					JobID string `json:"jobId"`
				} `json:"jobReference"`
				Status struct {
					State       string `json:"state"`
					ErrorResult *struct {
						Message string `json:"message"`
					} `json:"errorResult"`
				} `json:"status"`
				Statistics struct {
					CreationTime string `json:"creationTime"`
					Query        struct {
						StatementType      string             `json:"statementType"`
						TotalBytesBilled   string             `json:"totalBytesBilled"`
						NumDmlAffectedRows string             `json:"numDmlAffectedRows"`
						DDLTargetTable     warehouse.TableRef `json:"ddlTargetTable"`
					} `json:"query"`
				} `json:"statistics"`
				Configuration struct {
					Query struct {
						Query string `json:"query"`
					} `json:"query"`
				} `json:"configuration"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode child jobs: %w", err)
		}
		for _, j := range raw.Jobs {
			qs := j.Statistics.Query
			cj := warehouse.ChildJob{
				ID: j.JobReference.JobID, StatementType: qs.StatementType, State: j.Status.State,
				BytesBilled: atoi(qs.TotalBytesBilled), DMLRows: -1,
				Query: shorten(j.Configuration.Query.Query, 300), HasRows: qs.StatementType == "SELECT",
			}
			if qs.NumDmlAffectedRows != "" {
				cj.DMLRows = atoi(qs.NumDmlAffectedRows)
			}
			if j.Status.ErrorResult != nil {
				cj.Error = j.Status.ErrorResult.Message
			}
			if t := qs.DDLTargetTable; t.Table != "" {
				cj.Table = t.Project + "." + t.Dataset + "." + t.Table
			}
			out = append(out, cj)
		}
		// The API lists newest first; a script reads in the order it ran.
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 { // not in the middle of a character
		cut--
	}
	return s[:cut] + "…"
}

// Rows reads a page of the rows of job ref. token is the previous page's
// NextToken, "" for the first. Rows come back as BigQuery formats them with
// timestamps as integer microseconds, so none loses precision.
func (c *Client) Rows(ctx context.Context, ref warehouse.JobRef, token string, size int) (*warehouse.Page, error) {
	if size <= 0 {
		size = warehouse.DefaultPageSize
	}
	q := url.Values{
		"maxResults":                      {strconv.Itoa(size)},
		"formatOptions.useInt64Timestamp": {"true"},
	}
	if ref.Location != "" {
		q.Set("location", ref.Location)
	}
	if token != "" {
		q.Set("pageToken", token)
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/queries/%s?%s", c.baseURL, url.PathEscape(ref.Project), url.PathEscape(ref.ID), q.Encode())
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return decodePage(body)
}

func decodePage(body []byte) (*warehouse.Page, error) {
	var raw struct {
		JobComplete bool `json:"jobComplete"`
		Schema      struct {
			Fields []warehouse.Column `json:"fields"`
		} `json:"schema"`
		TotalRows string            `json:"totalRows"`
		PageToken string            `json:"pageToken"`
		Rows      []json.RawMessage `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode rows: %w", err)
	}
	p := &warehouse.Page{Schema: raw.Schema.Fields, Rows: [][]any{}, NextToken: raw.PageToken, Total: atoi(raw.TotalRows), Complete: raw.JobComplete}
	for _, r := range raw.Rows {
		var row struct {
			F []struct {
				V json.RawMessage `json:"v"`
			} `json:"f"`
		}
		if err := json.Unmarshal(r, &row); err != nil {
			return nil, fmt.Errorf("decode row: %w", err)
		}
		cells := make([]any, len(row.F))
		for i, f := range row.F {
			var field warehouse.Column
			if i < len(p.Schema) {
				field = p.Schema[i]
			}
			cells[i] = Cell(field, f.V)
		}
		p.Rows = append(p.Rows, cells)
	}
	return p, nil
}

// Cell turns the value v of a column of schema f into the shape the page
// draws:
//
//   - NULL is nil;
//   - a REPEATED column is a []any of its elements' shapes;
//   - a STRUCT is a []any with a shape per field, in the schema's order;
//   - a RANGE is a [2]any of its bounds' shapes, nil for an unbounded end;
//   - TIMESTAMP is "2026-10-15 12:00:00.123456 UTC";
//   - BYTES is {"b64": "…", "len": n}, so the page can show size and hex;
//   - everything else is the string BigQuery sent: INT64, NUMERIC and
//     BIGNUMERIC keep every digit, FLOAT64 may be NaN, Infinity or -Infinity.
func Cell(f warehouse.Column, v json.RawMessage) any {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	if f.Repeated {
		var items []struct {
			V json.RawMessage `json:"v"`
		}
		if err := json.Unmarshal(v, &items); err != nil {
			return string(v)
		}
		elem := f
		elem.Repeated = false
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = Cell(elem, it.V)
		}
		return out
	}
	switch f.Type {
	case "RECORD", "STRUCT":
		var rec struct {
			F []struct {
				V json.RawMessage `json:"v"`
			} `json:"f"`
		}
		if err := json.Unmarshal(v, &rec); err != nil {
			return string(v)
		}
		out := make([]any, len(rec.F))
		for i, c := range rec.F {
			var sub warehouse.Column
			if i < len(f.Fields) {
				sub = f.Fields[i]
			}
			out[i] = Cell(sub, c.V)
		}
		return out
	case "RANGE":
		var r struct {
			Start struct {
				V json.RawMessage `json:"v"`
			} `json:"start"`
			End struct {
				V json.RawMessage `json:"v"`
			} `json:"end"`
		}
		elem := warehouse.Column{Type: "STRING"}
		if f.RangeElement != "" {
			elem = warehouse.Column{Type: f.RangeElement}
		}
		if err := json.Unmarshal(v, &r); err == nil && (r.Start.V != nil || r.End.V != nil) {
			return []any{Cell(elem, r.Start.V), Cell(elem, r.End.V)}
		}
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return strings.TrimSpace(string(v)) // a number or boolean a server sent bare
	}
	switch f.Type {
	case "TIMESTAMP":
		if t, ok := FormatTimestamp(s); ok {
			return t
		}
	case "BYTES":
		n := base64.StdEncoding.DecodedLen(len(s))
		if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
			n = len(raw)
		}
		return map[string]any{"b64": s, "len": n}
	}
	return s
}

// FormatTimestamp words integer microseconds since the epoch (as a string,
// which is how BigQuery sends them) the way BigQuery prints a TIMESTAMP.
func FormatTimestamp(micros string) (string, bool) {
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		// Maybe an old-style float of seconds; keep what it says.
		return "", false
	}
	t := time.UnixMicro(n).UTC()
	layout := "2006-01-02 15:04:05"
	if n%1_000_000 != 0 {
		layout += ".000000"
	}
	return t.Format(layout) + " UTC", true
}
