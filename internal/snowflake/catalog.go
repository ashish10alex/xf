package snowflake

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// Metadata commands (SHOW, DESCRIBE) execute in Snowflake's Cloud Services
// layer without requiring a virtual warehouse, so nothing in this file resumes
// compute or bills warehouse credits.

// Table returns the metadata and columns of database.schema.table using
// DESCRIBE TABLE (which in Snowflake works on both tables and views). Because
// DESCRIBE TABLE does not report object kind, Type defaults to "TABLE" (use
// Describe for the exact kind from SHOW OBJECTS). A missing object returns a
// 404 *warehouse.APIError.
func (c *Client) Table(ctx context.Context, database, schema, table string) (*warehouse.Table, error) {
	cols, err := c.describeColumns(ctx, database, schema, table)
	if err != nil {
		return nil, err
	}
	return &warehouse.Table{Type: "TABLE", Schema: cols}, nil
}

// Describe returns the full table metadata for hover cards: columns from
// DESCRIBE TABLE plus row count, byte size, kind and comment from SHOW OBJECTS.
func (c *Client) Describe(ctx context.Context, database, schema, table string) (*warehouse.TableDetail, error) {
	cols, err := c.describeColumns(ctx, database, schema, table)
	if err != nil {
		return nil, err
	}
	d := &warehouse.TableDetail{
		Type:     "TABLE",
		Schema:   cols,
		NumRows:  -1,
		NumBytes: -1,
	}
	// Best-effort object metadata (rows, bytes, kind, comment, created_on).
	if database != "" && schema != "" && table != "" {
		q := "SHOW OBJECTS LIKE " + quoteLiteral(escapeLike(table)) + " IN SCHEMA " + sqlIdent(database) + "." + sqlIdent(schema)
		if st, err := c.executeMetadata(ctx, q); err == nil {
			idx := st.colIndex()
			for _, row := range st.Data {
				if !strings.EqualFold(cellStr(row, idx, "name"), table) {
					continue
				}
				if kind := cellStr(row, idx, "kind"); kind != "" {
					d.Type = strings.ToUpper(kind)
				}
				d.Description = cellStr(row, idx, "comment")
				if s := cellStr(row, idx, "rows"); s != "" {
					if n, err := strconv.ParseInt(s, 10, 64); err == nil {
						d.NumRows = n
					}
				}
				if s := cellStr(row, idx, "bytes"); s != "" {
					if n, err := strconv.ParseInt(s, 10, 64); err == nil {
						d.NumBytes = n
					}
				}
				if s := cellStr(row, idx, "created_on"); s != "" {
					d.Created = parseSnowflakeTime(s)
				}
				if s := cellStr(row, idx, "cluster_by"); s != "" {
					d.ClusterFields = parseClusterBy(s)
				}
				break
			}
		}
	}
	return d, nil
}

func (c *Client) describeColumns(ctx context.Context, database, schema, table string) ([]warehouse.Column, error) {
	target := relationSQL(database, schema, table)
	st, err := c.executeMetadata(ctx, "DESCRIBE TABLE "+target)
	if err != nil {
		var se *StatementError
		if errors.As(err, &se) {
			if se.isNotFoundError() {
				return nil, &warehouse.APIError{Status: http.StatusNotFound, Message: se.Message}
			}
			return nil, &warehouse.APIError{Status: http.StatusBadRequest, Message: se.Message}
		}
		return nil, err
	}
	idx := st.colIndex()
	cols := make([]warehouse.Column, 0, len(st.Data))
	for _, row := range st.Data {
		name := cellStr(row, idx, "name")
		if name == "" {
			continue
		}
		if kind := strings.ToUpper(cellStr(row, idx, "kind")); kind != "" && kind != "COLUMN" {
			continue
		}
		cols = append(cols, warehouse.Column{
			Name:        name,
			Type:        cellStr(row, idx, "type"),
			Description: cellStr(row, idx, "comment"),
			Required:    strings.EqualFold(cellStr(row, idx, "null?"), "N"),
		})
	}
	return cols, nil
}

// ListProjects lists the Snowflake databases the caller can see.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	st, err := c.executeMetadata(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, mapStatementError(err)
	}
	idx := st.colIndex()
	out := make([]string, 0, len(st.Data))
	for _, row := range st.Data {
		if name := cellStr(row, idx, "name"); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// ListDatasets lists the schemas of database.
func (c *Client) ListDatasets(ctx context.Context, database string) ([]string, error) {
	st, err := c.executeMetadata(ctx, "SHOW SCHEMAS IN DATABASE "+sqlIdent(database))
	if err != nil {
		return nil, mapStatementError(err)
	}
	idx := st.colIndex()
	out := make([]string, 0, len(st.Data))
	for _, row := range st.Data {
		if name := cellStr(row, idx, "name"); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// ListTables lists the tables and views of database.schema.
func (c *Client) ListTables(ctx context.Context, database, schema string) ([]warehouse.TableListing, error) {
	st, err := c.executeMetadata(ctx, "SHOW OBJECTS IN SCHEMA "+sqlIdent(database)+"."+sqlIdent(schema))
	if err != nil {
		return nil, mapStatementError(err)
	}
	idx := st.colIndex()
	out := make([]warehouse.TableListing, 0, len(st.Data))
	for _, row := range st.Data {
		name := cellStr(row, idx, "name")
		if name == "" {
			continue
		}
		out = append(out, warehouse.TableListing{ID: name, Type: strings.ToUpper(cellStr(row, idx, "kind"))})
	}
	return out, nil
}

func relationSQL(database, schema, table string) string {
	var parts []string
	if database != "" {
		parts = append(parts, sqlIdent(database))
	}
	if schema != "" {
		parts = append(parts, sqlIdent(schema))
	}
	parts = append(parts, sqlIdent(table))
	return strings.Join(parts, ".")
}

func mapStatementError(err error) error {
	var se *StatementError
	if errors.As(err, &se) {
		if se.isNotFoundError() {
			return &warehouse.APIError{Status: http.StatusNotFound, Message: se.Message}
		}
		return &warehouse.APIError{Status: http.StatusBadRequest, Message: se.Message}
	}
	return err
}

func parseSnowflakeTime(s string) time.Time {
	// Snowflake JSONv2 timestamps may arrive as epoch seconds ("1700000000.000")
	// or formatted timestamps ("2024-01-02 03:04:05.000 -0700").
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 1e8 {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec).UTC()
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.000 -0700",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ClusterBy returns the clustering key expressions of database.schema.table
// using a single Cloud Services SHOW OBJECTS call (no DESCRIBE TABLE, no
// virtual warehouse).
func (c *Client) ClusterBy(ctx context.Context, database, schema, table string) ([]string, error) {
	if database == "" || schema == "" || table == "" {
		return nil, nil
	}
	q := "SHOW OBJECTS LIKE " + quoteLiteral(escapeLike(table)) + " IN SCHEMA " + sqlIdent(database) + "." + sqlIdent(schema)
	st, err := c.executeMetadata(ctx, q)
	if err != nil {
		return nil, mapStatementError(err)
	}
	idx := st.colIndex()
	for _, row := range st.Data {
		if !strings.EqualFold(cellStr(row, idx, "name"), table) {
			continue
		}
		if s := cellStr(row, idx, "cluster_by"); s != "" {
			return parseClusterBy(s), nil
		}
		return nil, nil
	}
	return nil, nil
}

func parseClusterBy(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToUpper(s), "LINEAR(") && strings.HasSuffix(s, ")") {
		s = s[len("LINEAR(") : len(s)-1]
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	depth := 0
	inSingle := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if inSingle && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inSingle = !inSingle
		case '(':
			if !inSingle {
				depth++
			}
		case ')':
			if !inSingle && depth > 0 {
				depth--
			}
		case ',':
			if !inSingle && depth == 0 {
				if p := strings.TrimSpace(s[start:i]); p != "" {
					out = append(out, p)
				}
				start = i + 1
			}
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		out = append(out, p)
	}
	return out
}
