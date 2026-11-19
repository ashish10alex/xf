package databricks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// Unity Catalog answers without a SQL warehouse, so nothing here starts one
// or bills.

// ucTable is a table as Unity Catalog's tables API has it.
type ucTable struct {
	Name      string `json:"name"`
	Type      string `json:"table_type"`
	Comment   string `json:"comment"`
	Created   int64  `json:"created_at"`
	Updated   int64  `json:"updated_at"`
	ViewQuery string `json:"view_definition"`
	Columns   []struct {
		Name      string `json:"name"`
		TypeText  string `json:"type_text"`
		Comment   string `json:"comment"`
		Nullable  *bool  `json:"nullable"`
		Partition *int   `json:"partition_index"`
	} `json:"columns"`
}

func (t *ucTable) schema() []warehouse.Column {
	cols := make([]warehouse.Column, 0, len(t.Columns))
	for _, c := range t.Columns {
		cols = append(cols, warehouse.Column{Name: c.Name, Type: c.TypeText, Description: c.Comment,
			Required: c.Nullable != nil && !*c.Nullable})
	}
	return cols
}

// partition is the first column the table is partitioned by, "" for none.
func (t *ucTable) partition() string {
	first, at := "", -1
	for _, c := range t.Columns {
		if c.Partition != nil && (at < 0 || *c.Partition < at) {
			first, at = c.Name, *c.Partition
		}
	}
	return first
}

func millis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// FullName is catalog.schema.table as Unity Catalog names a table.
func FullName(catalog, schema, table string) string { return catalog + "." + schema + "." + table }

// CatalogURL is a table's page in the workspace at host's Catalog
// Explorer; "" without a host or a table.
func CatalogURL(host, catalog, schema, table string) string {
	if host == "" || table == "" {
		return ""
	}
	return host + "/explore/data/" + url.PathEscape(catalog) + "/" + url.PathEscape(schema) + "/" + url.PathEscape(table)
}

func (c *Client) ucTable(ctx context.Context, catalog, schema, table string) (*ucTable, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/2.1/unity-catalog/tables/"+url.PathEscape(FullName(catalog, schema, table)), nil)
	if err != nil {
		return nil, err
	}
	var t ucTable
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("decode table metadata: %w", err)
	}
	return &t, nil
}

// Table is the Unity Catalog metadata of catalog.schema.table: its type
// (MANAGED, EXTERNAL, VIEW, MATERIALIZED_VIEW, STREAMING_TABLE), columns
// and when it last changed. A table that isn't there is the API's 404.
func (c *Client) Table(ctx context.Context, catalog, schema, table string) (*warehouse.Table, error) {
	t, err := c.ucTable(ctx, catalog, schema, table)
	if err != nil {
		return nil, err
	}
	out := &warehouse.Table{Type: t.Type, Description: t.Comment, LastModified: millis(t.Updated), Schema: t.schema(), ViewQuery: t.ViewQuery}
	if p := t.partition(); p != "" {
		out.Partition = &warehouse.Partition{Column: p}
	}
	return out, nil
}

// Describe is the table as a hover card shows it. Unity Catalog counts no
// rows or bytes without a warehouse, so both are -1.
func (c *Client) Describe(ctx context.Context, catalog, schema, table string) (*warehouse.TableDetail, error) {
	t, err := c.ucTable(ctx, catalog, schema, table)
	if err != nil {
		return nil, err
	}
	return &warehouse.TableDetail{Type: t.Type, Description: t.Comment, Schema: t.schema(), PartitionField: t.partition(),
		NumRows: -1, NumBytes: -1, Created: millis(t.Created), Modified: millis(t.Updated)}, nil
}

// ListProjects lists the catalogs in Unity Catalog without waking a SQL
// warehouse.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	var out []string
	err := c.pages(ctx, "/api/2.1/unity-catalog/catalogs", func(body []byte) (string, error) {
		var page struct {
			Catalogs []struct {
				Name string `json:"name"`
			} `json:"catalogs"`
			Next string `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("decode catalogs: %w", err)
		}
		for _, cat := range page.Catalogs {
			if cat.Name != "" {
				out = append(out, cat.Name)
			}
		}
		return page.Next, nil
	})
	return out, err
}

// ListDatasets lists the schemas of catalog.
func (c *Client) ListDatasets(ctx context.Context, catalog string) ([]string, error) {
	var out []string
	err := c.pages(ctx, "/api/2.1/unity-catalog/schemas?catalog_name="+url.QueryEscape(catalog), func(body []byte) (string, error) {
		var page struct {
			Schemas []struct {
				Name string `json:"name"`
			} `json:"schemas"`
			Next string `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("decode schemas: %w", err)
		}
		for _, s := range page.Schemas {
			out = append(out, s.Name)
		}
		return page.Next, nil
	})
	return out, err
}

// ListTables lists the tables and views of catalog.schema.
func (c *Client) ListTables(ctx context.Context, catalog, schema string) ([]warehouse.TableListing, error) {
	var out []warehouse.TableListing
	path := "/api/2.1/unity-catalog/tables?catalog_name=" + url.QueryEscape(catalog) + "&schema_name=" + url.QueryEscape(schema)
	err := c.pages(ctx, path, func(body []byte) (string, error) {
		var page struct {
			Tables []struct {
				Name string `json:"name"`
				Type string `json:"table_type"`
			} `json:"tables"`
			Next string `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("decode tables: %w", err)
		}
		for _, t := range page.Tables {
			out = append(out, warehouse.TableListing{ID: t.Name, Type: t.Type})
		}
		return page.Next, nil
	})
	return out, err
}

// pages GETs path and the pages after it, as each page's read says.
func (c *Client) pages(ctx context.Context, path string, read func(body []byte) (next string, err error)) error {
	token := ""
	for range 100 {
		p := path
		if token != "" {
			sep := "&"
			if !strings.Contains(p, "?") {
				sep = "?"
			}
			p += sep + "page_token=" + url.QueryEscape(token)
		}
		body, err := c.do(ctx, http.MethodGet, p, nil)
		if err != nil {
			return err
		}
		if token, err = read(body); err != nil || token == "" {
			return err
		}
	}
	return nil
}
