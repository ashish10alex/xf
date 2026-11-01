package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// What defer to prod (ADR 0031) asks of BigQuery: which tables a dataset
// holds and which of them are Proxy Views, whether a prod table exists, and
// making, repointing and removing Proxy Views.

// LabelledTable is a table in a dataset's listing, with its labels.
type LabelledTable struct {
	ID     string
	Type   string
	Labels map[string]string
}

// ListTablesLabelled lists the tables and views of a dataset with their
// labels. A dataset that doesn't exist fails with a 404 APIError.
func (c *Client) ListTablesLabelled(ctx context.Context, project, dataset string) ([]LabelledTable, error) {
	var out []LabelledTable
	token := ""
	for range listPages {
		q := url.Values{"maxResults": {fmt.Sprint(listPageSize)}, "fields": {"nextPageToken,tables(tableReference/tableId,type,labels)"}}
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
				Type   string            `json:"type"`
				Labels map[string]string `json:"labels"`
			} `json:"tables"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode tables: %w", err)
		}
		for _, t := range raw.Tables {
			out = append(out, LabelledTable{ID: t.TableReference.TableID, Type: t.Type, Labels: t.Labels})
		}
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	return out, nil
}

// ListRoutines lists the names of a dataset's functions and procedures,
// which a table listing leaves out.
func (c *Client) ListRoutines(ctx context.Context, project, dataset string) ([]string, error) {
	var out []string
	token := ""
	for range listPages {
		q := url.Values{"maxResults": {fmt.Sprint(listPageSize)}, "fields": {"nextPageToken,routines(routineReference/routineId)"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/routines?%s", c.baseURL, url.PathEscape(project), url.PathEscape(dataset), q.Encode()), nil)
		if err != nil {
			return nil, err
		}
		var raw struct {
			NextPageToken string `json:"nextPageToken"`
			Routines      []struct {
				RoutineReference struct {
					RoutineID string `json:"routineId"`
				} `json:"routineReference"`
			} `json:"routines"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("decode routines: %w", err)
		}
		for _, r := range raw.Routines {
			out = append(out, r.RoutineReference.RoutineID)
		}
		if raw.NextPageToken == "" {
			break
		}
		token = raw.NextPageToken
	}
	return out, nil
}

// Exists says whether a table, or with routine a function or procedure,
// exists. A 403 is returned as the error, so a caller can tell a table it
// may not read from one that isn't there.
func (c *Client) Exists(ctx context.Context, project, dataset, name string, routine bool) (bool, error) {
	kind, fields := "tables", "id"
	if routine {
		kind, fields = "routines", "etag"
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/%s/%s?fields=%s", c.baseURL,
		url.PathEscape(project), url.PathEscape(dataset), kind, url.PathEscape(name), fields)
	_, err := c.do(ctx, http.MethodGet, u, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// IsNotFound says err is BigQuery's 404.
func IsNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

// IsForbidden says err is BigQuery's 403.
func IsForbidden(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusForbidden
}

// ViewInfo is what defer to prod reads of a table at a Dev Target: whether
// it is a Proxy View, what it reads, and how old it is.
type ViewInfo struct {
	Type         string
	Labels       map[string]string
	Query        string
	LastModified time.Time
}

// View reads a table's labels, view query and age; nil when it doesn't
// exist.
func (c *Client) View(ctx context.Context, project, dataset, table string) (*ViewInfo, error) {
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s?fields=%s", c.baseURL,
		url.PathEscape(project), url.PathEscape(dataset), url.PathEscape(table), url.QueryEscape("type,labels,lastModifiedTime,view(query)"))
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Type             string            `json:"type"`
		Labels           map[string]string `json:"labels"`
		LastModifiedTime string            `json:"lastModifiedTime"`
		View             *struct {
			Query string `json:"query"`
		} `json:"view"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode table metadata: %w", err)
	}
	v := &ViewInfo{Type: raw.Type, Labels: raw.Labels}
	if raw.View != nil {
		v.Query = raw.View.Query
	}
	if ms, err := strconv.ParseInt(raw.LastModifiedTime, 10, 64); err == nil {
		v.LastModified = time.UnixMilli(ms).UTC()
	}
	return v, nil
}

// ViewSpec is a view to make or repoint.
type ViewSpec struct {
	Project, Dataset, Table string
	// Query is standard SQL.
	Query       string
	Labels      map[string]string
	Description string
}

func (v ViewSpec) body() ([]byte, error) {
	return json.Marshal(map[string]any{
		"tableReference": map[string]string{"projectId": v.Project, "datasetId": v.Dataset, "tableId": v.Table},
		"view":           map[string]any{"query": v.Query, "useLegacySql": false},
		"labels":         v.Labels,
		"description":    v.Description,
	})
}

// CreateView makes a view.
func (c *Client) CreateView(ctx context.Context, v ViewSpec) error {
	body, err := v.body()
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables", c.baseURL, url.PathEscape(v.Project), url.PathEscape(v.Dataset))
	_, err = c.do(ctx, http.MethodPost, u, body)
	return err
}

// UpdateView points an existing view at v's query, labels and
// description.
func (c *Client) UpdateView(ctx context.Context, v ViewSpec) error {
	body, err := v.body()
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s", c.baseURL, url.PathEscape(v.Project), url.PathEscape(v.Dataset), url.PathEscape(v.Table))
	_, err = c.do(ctx, http.MethodPatch, u, body)
	return err
}

// DeleteTable removes a table or view; one already gone is no error.
func (c *Client) DeleteTable(ctx context.Context, project, dataset, table string) error {
	u := fmt.Sprintf("%s/bigquery/v2/projects/%s/datasets/%s/tables/%s", c.baseURL, url.PathEscape(project), url.PathEscape(dataset), url.PathEscape(table))
	_, err := c.do(ctx, http.MethodDelete, u, nil)
	var api *APIError
	if err == nil || IsNotFound(err) || errors.As(err, &api) && api.Status == http.StatusNoContent {
		return nil
	}
	return err
}
