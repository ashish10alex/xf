package bigquery

// What the Scratch view's completion and hover need of BigQuery: the
// datasets of a project, the tables of a dataset, and one table in full.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ashish10alex/xf/internal/warehouse"
	"net/http"
	"net/url"
	"time"
)

// listPages is how many pages of datasets or tables a listing reads at
// most, at listPageSize each.
const (
	listPages    = 5
	listPageSize = 1000
)

// ListProjects lists the IDs of the BigQuery projects the caller can see.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	var out []string
	token := ""
	for range listPages {
		q := url.Values{"maxResults": {fmt.Sprint(listPageSize)}, "fields": {"nextPageToken,projects(id,projectReference/projectId)"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/bigquery/v2/projects?%s", c.baseURL, q.Encode()), nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Projects      []struct {
				ID               string `json:"id"`
				ProjectReference struct {
					ProjectID string `json:"projectId"`
				} `json:"projectReference"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode projects: %w", err)
		}
		for _, p := range raw.Projects {
			if id := cmp.Or(p.ProjectReference.ProjectID, p.ID); id != "" {
				out = append(out, id)
			}
		}
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	return out, nil
}

// ListDatasets lists the ids of the datasets of project.
func (c *Client) ListDatasets(ctx context.Context, project string) ([]string, error) {
	var out []string
	token := ""
	for range listPages {
		q := url.Values{"maxResults": {fmt.Sprint(listPageSize)}, "fields": {"nextPageToken,datasets(datasetReference/datasetId)"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets?%s", c.baseURL, url.PathEscape(project), q.Encode()), nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Datasets      []struct {
				DatasetReference struct {
					DatasetID string `json:"datasetId"`
				} `json:"datasetReference"`
			} `json:"datasets"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode datasets: %w", err)
		}
		for _, d := range raw.Datasets {
			out = append(out, d.DatasetReference.DatasetID)
		}
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	return out, nil
}

// ListTables lists the tables and views of a dataset.
func (c *Client) ListTables(ctx context.Context, project, dataset string) ([]warehouse.TableListing, error) {
	var out []warehouse.TableListing
	token := ""
	for range listPages {
		q := url.Values{"maxResults": {fmt.Sprint(listPageSize)}, "fields": {"nextPageToken,tables(tableReference/tableId,type)"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables?%s", c.baseURL, url.PathEscape(project), url.PathEscape(dataset), q.Encode()), nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Tables        []struct {
				TableReference struct {
					TableID string `json:"tableId"`
				} `json:"tableReference"`
				Type string `json:"type"`
			} `json:"tables"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode tables: %w", err)
		}
		for _, t := range raw.Tables {
			out = append(out, warehouse.TableListing{ID: t.TableReference.TableID, Type: t.Type})
		}
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	return out, nil
}

const detailFields = "type,description,location,schema,numRows,numBytes,creationTime,lastModifiedTime,timePartitioning(field,type),rangePartitioning(field),clustering(fields)"

// Describe fetches the metadata of project.dataset.table in full.
func (c *Client) Describe(ctx context.Context, project, dataset, table string) (*warehouse.TableDetail, error) {
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s?fields=%s",
		c.baseURL, url.PathEscape(project), url.PathEscape(dataset), url.PathEscape(table), url.QueryEscape(detailFields))
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Type             string `json:"type"`
		Description      string `json:"description"`
		Location         string `json:"location"`
		NumRows          string `json:"numRows"`
		NumBytes         string `json:"numBytes"`
		CreationTime     string `json:"creationTime"`
		LastModifiedTime string `json:"lastModifiedTime"`
		Schema           struct {
			Fields []warehouse.Column `json:"fields"`
		} `json:"schema"`
		TimePartitioning *struct {
			Field string `json:"field"`
			Type  string `json:"type"`
		} `json:"timePartitioning"`
		RangePartitioning *struct {
			Field string `json:"field"`
		} `json:"rangePartitioning"`
		Clustering *struct {
			Fields []string `json:"fields"`
		} `json:"clustering"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode table: %w", err)
	}
	d := &warehouse.TableDetail{Type: raw.Type, Description: raw.Description, Location: raw.Location, Schema: raw.Schema.Fields,
		NumRows: -1, NumBytes: -1}
	if raw.NumRows != "" {
		d.NumRows = atoi(raw.NumRows)
	}
	if raw.NumBytes != "" {
		d.NumBytes = atoi(raw.NumBytes)
	}
	if ms := atoi(raw.CreationTime); ms != 0 {
		d.Created = time.UnixMilli(ms).UTC()
	}
	if ms := atoi(raw.LastModifiedTime); ms != 0 {
		d.Modified = time.UnixMilli(ms).UTC()
	}
	switch {
	case raw.TimePartitioning != nil:
		d.PartitionField, d.PartitionType = raw.TimePartitioning.Field, raw.TimePartitioning.Type
	case raw.RangePartitioning != nil:
		d.PartitionField, d.PartitionType = raw.RangePartitioning.Field, "RANGE"
	}
	if raw.Clustering != nil {
		d.ClusterFields = raw.Clustering.Fields
	}
	return d, nil
}
