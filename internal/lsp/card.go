package lsp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// The hover card follows vscode-dataform-tools' createHoverContentForTable
// (src/hoverProvider.ts), with the schema table padded so it lines up in a
// terminal.

const (
	// maxSchemaRows caps the schema table, as the extension does.
	maxSchemaRows = 150
	// maxCellRunes keeps descriptions from making the float too wide.
	maxCellRunes = 60
	// nestIndent indents nested fields; non-breaking spaces survive markdown.
	nestIndent = "   "
)

// card renders the hover for one table reference. tbl is BigQuery's metadata,
// or nil with bqErr saying why (bqErr is also nil when BigQuery wasn't asked).
func card(ref navigate.TableRef, tbl *bigquery.Table, bqErr error, timeout time.Duration) string {
	var b strings.Builder
	t := ref.Target
	fmt.Fprintf(&b, "#### [%s](%s)", string(t.ID()), t.ConsoleURL())
	if ref.Source == navigate.SourceSelf {
		return b.String()
	}
	if ref.Action == nil && tbl == nil {
		// A raw id nobody could look up: all we know is its name.
		if bqErr != nil {
			b.WriteString("\n\n---\n\n" + unavailable(bqErr, timeout))
		}
		return b.String()
	}
	b.WriteString("\n\n---\n\n")

	a := ref.Action
	if a == nil {
		a = &project.Action{}
	}
	var facts []string
	description := a.Description
	if tbl != nil && strings.TrimSpace(tbl.Description) != "" {
		description = tbl.Description
	}
	if d := strings.TrimSpace(description); d != "" {
		facts = append(facts, "**Description:** "+d)
	}
	switch {
	case ref.Action != nil:
		facts = append(facts, "**Type:** "+a.Kind.String())
	case tbl != nil && tbl.Type != "":
		facts = append(facts, "**Type:** "+strings.ToLower(tbl.Type))
	}
	if tbl != nil && tbl.Location != "" {
		facts = append(facts, "**Location:** "+tbl.Location)
	}
	if a.PartitionBy != "" {
		facts = append(facts, "**Partition:** `"+a.PartitionBy+"`")
	}
	if tbl != nil && !tbl.LastModified.IsZero() {
		facts = append(facts, "**Last Modified Time:** "+tbl.LastModified.UTC().Format("Jan 2, 2006, 03:04:05 PM")+" UTC")
	}
	b.WriteString(strings.Join(facts, "\n\n"))

	var rows []schemaRow
	if tbl != nil {
		rows = schemaRows(tbl.Schema, a.Columns)
	} else {
		rows = configRows(a.Columns)
	}
	if len(rows) > 0 {
		b.WriteString("\n\n---\n\n")
		b.WriteString(schemaTable(rows))
	} else if bqErr == nil {
		// A BigQuery error says why there is no schema; otherwise say it
		// here, so the card never looks like the lookup did nothing.
		b.WriteString("\n\n_" + noSchema(ref.Action, tbl) + "_")
	}
	if bqErr != nil {
		b.WriteString("\n\n" + unavailable(bqErr, timeout))
	}
	return b.String()
}

// hasOutputTable reports whether a builds a table at its target to look
// up: a model does, and so does an operation with hasOutput.
func hasOutputTable(a *project.Action) bool {
	return a.Kind.HasTable() || a.Kind == project.KindOperation && a.HasOutput && a.Target.Database != ""
}

// noSchema says why a card with no BigQuery error has no schema to show.
// a is nil for a table outside the project.
func noSchema(a *project.Action, tbl *bigquery.Table) string {
	switch {
	case a != nil && a.Kind == project.KindOperation && a.HasOutput && !hasOutputTable(a):
		return "no schema: this operation's target names no project to look it up in"
	case a != nil && a.Kind == project.KindOperation && !hasOutputTable(a):
		return "no schema: this operation has no output table (set `hasOutput: true` to give it one)"
	case a != nil && a.Kind == project.KindEphemeral:
		return "no schema: an ephemeral model has no table of its own (it is inlined where it is used)"
	case a != nil && !hasOutputTable(a):
		kind := a.Kind.String()
		article := "a "
		if strings.ContainsRune("aeiou", rune(kind[0])) {
			article = "an "
		}
		return "no schema: " + article + kind + " has no table of its own"
	case tbl != nil:
		return "no schema: BigQuery has no columns for the table"
	}
	return "no schema: no BigQuery access here, and the config documents no columns"
}

func unavailable(err error, timeout time.Duration) string {
	reason := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = fmt.Sprintf("timed out after %s", timeout)
	case errors.Is(err, bigquery.ErrAuth):
		reason += "; " + bigquery.AuthHint
	}
	return "⚠ **BigQuery metadata unavailable:** " + reason
}

type schemaRow struct {
	name, typ, description string
	depth                  int
}

// schemaRows flattens a schema depth first. A column documented in the
// action's config (matched by its full path) takes that description: config
// descriptions reach BigQuery only when the table is next built.
func schemaRows(fields []bigquery.Field, columns []project.ColumnDescription) []schemaRow {
	var rows []schemaRow
	var walk func(fields []bigquery.Field, path []string)
	walk = func(fields []bigquery.Field, path []string) {
		for _, f := range fields {
			p := append(slices.Clone(path), f.Name)
			desc := f.Description
			for _, c := range columns {
				if slices.Equal(c.Path, p) {
					desc = c.Description
				}
			}
			typ := f.Type
			if f.Mode != "" && f.Mode != "NULLABLE" {
				typ += " " + f.Mode
			}
			rows = append(rows, schemaRow{name: f.Name, typ: typ, description: desc, depth: len(path)})
			walk(f.Fields, p)
		}
	}
	walk(fields, nil)
	return rows
}

// configRows lists the columns documented in an action's config, for when
// BigQuery can't be asked for the schema.
func configRows(columns []project.ColumnDescription) []schemaRow {
	var rows []schemaRow
	for _, c := range columns {
		if len(c.Path) > 0 {
			rows = append(rows, schemaRow{name: c.Path[len(c.Path)-1], description: c.Description, depth: len(c.Path) - 1})
		}
	}
	return rows
}

// schemaTable renders rows as a markdown table padded to equal widths.
func schemaTable(rows []schemaRow) string {
	omitted := 0
	if len(rows) > maxSchemaRows {
		omitted = len(rows) - maxSchemaRows
		rows = rows[:maxSchemaRows]
	}
	cells := [][3]string{{"name", "type", "description"}}
	for _, r := range rows {
		name := r.name
		if r.depth > 0 {
			name = strings.Repeat(nestIndent, r.depth-1) + "└─ " + name
		}
		cells = append(cells, [3]string{name, r.typ, cellText(r.description)})
	}
	var widths [3]int
	for _, row := range cells {
		for i, c := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	var b strings.Builder
	line := func(row [3]string) {
		for i, c := range row {
			b.WriteString("| " + c + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)) + " ")
		}
		b.WriteString("|\n")
	}
	line(cells[0])
	for i := range widths {
		b.WriteString("|" + strings.Repeat("-", widths[i]+2))
	}
	b.WriteString("|\n")
	for _, row := range cells[1:] {
		line(row)
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if omitted > 0 {
		plural := "s"
		if omitted == 1 {
			plural = ""
		}
		out += fmt.Sprintf("\n\n_… %d more field%s not shown_", omitted, plural)
	}
	return out
}

// cellText puts a description on one line, short enough for a float, with
// | escaped so it can't split the table.
func cellText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCellRunes {
		s = string([]rune(s)[:maxCellRunes-1]) + "…"
	}
	return strings.ReplaceAll(s, "|", `\|`)
}

// macroCard renders the hover for a dbt macro call: the macro's signature,
// package and file, then its description and arguments as YAML documents
// them.
func macroCard(m navigate.MacroRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### `%s`\n\n%s · `%s`", m.Signature, m.Macro.Package, m.Macro.FileName)
	if d := strings.TrimSpace(m.Macro.Description); d != "" {
		b.WriteString("\n\n---\n\n" + d)
	}
	var rows []schemaRow
	documented := false
	for _, a := range m.Macro.Arguments {
		rows = append(rows, schemaRow{name: a.Name, typ: a.Type, description: a.Description})
		documented = documented || a.Type != "" || a.Description != ""
	}
	if documented {
		b.WriteString("\n\n---\n\n" + schemaTable(rows))
	}
	return b.String()
}

// configKeyCard is a sqlx config key's hover: where it goes, what its
// value is, what it does, and the action types that take it.
func configKeyCard(k navigate.ConfigKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### `%s: %s`\n\n%s", k.Path, k.Kind, k.Doc)
	if len(k.Values) > 0 {
		b.WriteString("\n\nValues: `" + strings.Join(k.Values, "` · `") + "`")
	}
	if len(k.For) > 0 {
		b.WriteString("\n\nFor: " + strings.Join(k.For, ", "))
	}
	if k.Unread() {
		fmt.Fprintf(&b, "\n\n⚠ a %s doesn't read it", k.ConfigType)
	}
	return b.String()
}
