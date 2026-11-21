package impact

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// bigQueryAliases maps BigQuery legacy SQL and synonym type names to their
// standard GoogleSQL spellings.
var bigQueryAliases = map[string]string{
	"INTEGER":    "INT64",
	"INT":        "INT64",
	"SMALLINT":   "INT64",
	"TINYINT":    "INT64",
	"BYTEINT":    "INT64",
	"BIGINT":     "INT64",
	"FLOAT":      "FLOAT64",
	"BOOLEAN":    "BOOL",
	"RECORD":     "STRUCT",
	"DECIMAL":    "NUMERIC",
	"BIGDECIMAL": "BIGNUMERIC",
}

// databricksAliases maps Spark SQL / Unity Catalog type synonyms to their
// canonical Databricks spellings (never applying BigQuery aliases).
var databricksAliases = map[string]string{
	"INTEGER": "INT",
	"LONG":    "BIGINT",
	"SHORT":   "SMALLINT",
	"BYTE":    "TINYINT",
	"REAL":    "FLOAT",
	"BOOL":    "BOOLEAN",
	"DEC":     "DECIMAL",
	"NUMERIC": "DECIMAL",
}

// snowflakeSimpleAliases maps Snowflake non-parameterized type synonyms.
var snowflakeSimpleAliases = map[string]string{
	"BOOL":             "BOOLEAN",
	"FLOAT4":           "FLOAT",
	"FLOAT8":           "FLOAT",
	"DOUBLE":           "FLOAT",
	"DOUBLE PRECISION": "FLOAT",
	"REAL":             "FLOAT",
}

// DiffSchemas is DiffSchemasFor with BigQuery rules: top-level and nested
// STRUCT columns prod has that dev no longer produces, plus columns whose
// type (including ARRAY/REPEATED and RANGE<T>) changed.
func DiffSchemas(dev, prod []warehouse.Column) []ColumnImpact {
	return DiffSchemasFor(project.BigQuery, dev, prod)
}

// DiffSchemasFor compares dev against prod using the type system rules of
// wh (project.BigQuery, project.Databricks, or project.Snowflake; empty
// defaults to BigQuery). Names compare case-insensitively. Nested STRUCT
// fields are diffed recursively using dotted paths ("user.address.zip").
// Nullable/Required mode differences are ignored because query dry runs do
// not reliably mirror table nullability across warehouses.
func DiffSchemasFor(wh string, dev, prod []warehouse.Column) []ColumnImpact {
	if wh == "" {
		wh = project.BigQuery
	}
	return diffFields(wh, "", dev, prod)
}

func diffFields(wh, prefix string, dev, prod []warehouse.Column) []ColumnImpact {
	byName := make(map[string]warehouse.Column, len(dev))
	for _, f := range dev {
		if wh == project.Databricks && len(f.Fields) == 0 {
			f = expandDatabricksColumn(f)
		}
		byName[strings.ToLower(f.Name)] = f
	}
	var out []ColumnImpact
	for _, p := range prod {
		if wh == project.Databricks && len(p.Fields) == 0 {
			p = expandDatabricksColumn(p)
		}
		fullName := prefix + p.Name
		d, ok := byName[strings.ToLower(p.Name)]
		if !ok {
			out = append(out, ColumnImpact{Column: fullName, Change: ColumnChange{Kind: Dropped}})
			continue
		}
		pKey, pDisp := columnType(wh, p)
		dKey, dDisp := columnType(wh, d)
		if pKey != dKey {
			out = append(out, ColumnImpact{
				Column: fullName,
				Change: ColumnChange{
					Kind:     TypeChanged,
					From:     pDisp,
					To:       dDisp,
					Widening: isSafeWidening(wh, pKey, dKey),
				},
			})
			continue
		}
		if len(p.Fields) > 0 && len(d.Fields) > 0 {
			out = append(out, diffFields(wh, fullName+".", d.Fields, p.Fields)...)
		}
	}
	return out
}

// expandDatabricksColumn parses Unity Catalog / DESCRIBE QUERY STRUCT<...>
// (and ARRAY<STRUCT<...>>) type strings into Column.Fields so diffFields
// diffs Databricks struct fields one by one (ignoring field order, added
// fields, NOT NULL, and COMMENT clauses, matching BigQuery).
func expandDatabricksColumn(c warehouse.Column) warehouse.Column {
	s := strings.TrimSpace(c.Type)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "struct<") && strings.HasSuffix(s, ">"):
		if fields, ok := parseSparkStructFields(s[7 : len(s)-1]); ok {
			c.Type = "STRUCT"
			c.Fields = fields
		}
	case strings.HasPrefix(lower, "array<") && strings.HasSuffix(s, ">"):
		inner := strings.TrimSpace(s[6 : len(s)-1])
		if strings.HasPrefix(strings.ToLower(inner), "struct<") && strings.HasSuffix(inner, ">") {
			if fields, ok := parseSparkStructFields(inner[7 : len(inner)-1]); ok {
				c.Type = "ARRAY<STRUCT>"
				c.Fields = fields
			}
		}
	}
	return c
}

func parseSparkStructFields(inner string) ([]warehouse.Column, bool) {
	if strings.TrimSpace(inner) == "" {
		return nil, false
	}
	parts := splitTopLevel(inner, ',')
	fields := make([]warehouse.Column, 0, len(parts))
	for _, p := range parts {
		name, typ, ok := splitTopLevelColon(p)
		if !ok {
			return nil, false
		}
		name = strings.Trim(strings.TrimSpace(name), "`")
		if name == "" {
			return nil, false
		}
		typ = stripSparkFieldQualifiers(typ)
		fields = append(fields, expandDatabricksColumn(warehouse.Column{Name: name, Type: typ}))
	}
	return fields, len(fields) > 0
}

// stripSparkFieldQualifiers removes trailing NOT NULL, NULL, and COMMENT '...'
// clauses from a Spark STRUCT field type definition.
func stripSparkFieldQualifiers(typ string) string {
	s := strings.TrimSpace(typ)
	for {
		// Strip trailing COMMENT '...' or COMMENT "..."
		if idx := trailingSparkComment(s); idx >= 0 {
			s = strings.TrimSpace(s[:idx])
			continue
		}
		upper := strings.ToUpper(s)
		if strings.HasSuffix(upper, " NOT NULL") {
			s = strings.TrimSpace(s[:len(s)-len(" NOT NULL")])
			continue
		}
		if strings.HasSuffix(upper, " NULL") {
			s = strings.TrimSpace(s[:len(s)-len(" NULL")])
			continue
		}
		break
	}
	return s
}

func trailingSparkComment(s string) int {
	if len(s) < 2 {
		return -1
	}
	q := s[len(s)-1]
	if q != '\'' && q != '"' {
		return -1
	}
	// Walk backwards from before the closing quote to find the matching opening quote.
	i := len(s) - 2
	for i >= 0 {
		if s[i] == q {
			// Count preceding backslashes to handle escaped quotes.
			bs := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				bs++
			}
			if bs%2 == 0 {
				break
			}
		}
		i--
	}
	if i <= 0 {
		return -1
	}
	before := strings.TrimRight(s[:i], " \t\r\n")
	if len(before) >= len("COMMENT") && strings.EqualFold(before[len(before)-len("COMMENT"):], "COMMENT") {
		idx := len(before) - len("COMMENT")
		if idx == 0 || before[idx-1] == ' ' || before[idx-1] == '\t' || before[idx-1] == '>' || before[idx-1] == ')' {
			return idx
		}
	}
	return -1
}

// columnType returns a comparison key and human-readable normalized display
// type for c on warehouse wh.
func columnType(wh string, c warehouse.Column) (key, display string) {
	switch wh {
	case project.Databricks:
		t := normaliseDatabricksType(c.Type)
		return t, t
	case project.Snowflake:
		return normaliseSnowflakeType(c.Type)
	default:
		base := normaliseBigQueryType(c.Type)
		if base == "RANGE" && c.RangeElement != "" {
			base = "RANGE<" + normaliseBigQueryType(c.RangeElement) + ">"
		}
		if c.Repeated {
			base = "ARRAY<" + base + ">"
		}
		return base, base
	}
}

var typeParamRE = regexp.MustCompile(`^([A-Z0-9_ ]+?)\s*\(\s*([^)]*?)\s*\)$`)

func splitTypeParam(upper string) (base, params string, hasParam bool) {
	m := typeParamRE.FindStringSubmatch(upper)
	if m == nil {
		return upper, "", false
	}
	parts := strings.Split(m[2], ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.TrimSpace(m[1]), strings.Join(parts, ","), true
}

func normaliseBigQueryType(t string) string {
	upper := collapseSpaces(strings.ToUpper(strings.TrimSpace(t)))
	if base, params, ok := splitTypeParam(upper); ok {
		base = cmp.Or(bigQueryAliases[base], base)
		return base + "(" + params + ")"
	}
	return cmp.Or(bigQueryAliases[upper], upper)
}

func normaliseDatabricksType(t string) string {
	s := strings.TrimSpace(t)
	if s == "" {
		return ""
	}
	return normaliseSparkExpr(s)
}

// normaliseSparkExpr recursively normalizes Databricks / Spark SQL type
// expressions: scalar synonyms (integer -> INT, long -> BIGINT), DECIMAL(p,s)
// whitespace, and nested ARRAY<T>, MAP<K, V>, STRUCT<f: T, ...>.
func normaliseSparkExpr(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "array<") && strings.HasSuffix(s, ">"):
		return "ARRAY<" + normaliseSparkExpr(s[6:len(s)-1]) + ">"
	case strings.HasPrefix(lower, "map<") && strings.HasSuffix(s, ">"):
		parts := splitTopLevel(s[4:len(s)-1], ',')
		for i := range parts {
			parts[i] = normaliseSparkExpr(parts[i])
		}
		return "MAP<" + strings.Join(parts, ", ") + ">"
	case strings.HasPrefix(lower, "struct<") && strings.HasSuffix(s, ">"):
		fields := splitTopLevel(s[7:len(s)-1], ',')
		for i, f := range fields {
			if name, typ, ok := splitTopLevelColon(f); ok {
				name = strings.Trim(strings.TrimSpace(name), "`")
				fields[i] = strings.ToLower(name) + ": " + normaliseSparkExpr(stripSparkFieldQualifiers(typ))
			} else {
				fields[i] = normaliseSparkExpr(f)
			}
		}
		return "STRUCT<" + strings.Join(fields, ", ") + ">"
	}
	upper := collapseSpaces(strings.ToUpper(s))
	if base, params, ok := splitTypeParam(upper); ok {
		base = cmp.Or(databricksAliases[base], base)
		if base == "DECIMAL" && !strings.Contains(params, ",") && params != "" {
			return "DECIMAL(" + params + ",0)"
		}
		return base + "(" + params + ")"
	}
	canon := cmp.Or(databricksAliases[upper], upper)
	if canon == "DECIMAL" {
		return "DECIMAL(10,0)"
	}
	return canon
}

// normaliseSnowflakeType returns (comparisonKey, displayName) for Snowflake.
// SELECT * FROM (...) LIMIT 0 infers expression lengths/precisions (e.g.
// VARCHAR(3), NUMBER(1,0)) while DESCRIBE TABLE reports storage types
// (VARCHAR(16777216), NUMBER(38,0)) and structured ARRAY(...)/OBJECT(...)
// shapes. Comparing VARCHAR/BINARY/ARRAY/OBJECT by family and NUMBER by
// scale eliminates false positives while catching real type, scale, and
// timezone-kind changes (e.g. NUMBER(38,0) -> NUMBER(38,2), VARCHAR ->
// NUMBER, TIMESTAMP_NTZ -> TIMESTAMP_TZ). By design, VARCHAR length and
// NUMBER precision narrowing are not reported because LIMIT 0 cannot
// distinguish literal inference from explicit narrowing.
func normaliseSnowflakeType(t string) (key, display string) {
	upper := collapseSpaces(strings.ToUpper(strings.TrimSpace(t)))
	base, params, hasParam := splitTypeParam(upper)
	if !hasParam {
		base = upper
	}
	switch base {
	case "VARCHAR", "TEXT", "STRING", "NVARCHAR", "NVARCHAR2", "CHAR", "CHARACTER", "NCHAR", "CHAR VARYING", "CHARACTER VARYING":
		if hasParam {
			return "VARCHAR", "VARCHAR(" + params + ")"
		}
		return "VARCHAR", "VARCHAR"
	case "BINARY", "VARBINARY":
		if hasParam {
			return "BINARY", "BINARY(" + params + ")"
		}
		return "BINARY", "BINARY"
	case "ARRAY", "OBJECT":
		return base, base
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTEINT":
		return "NUMBER(*,0)", "NUMBER(38,0)"
	case "NUMBER", "DECIMAL", "NUMERIC", "DEC":
		if !hasParam {
			return "NUMBER(*,0)", "NUMBER(38,0)"
		}
		parts := strings.Split(params, ",")
		if len(parts) == 1 {
			return "NUMBER(*,0)", "NUMBER(" + parts[0] + ",0)"
		}
		return "NUMBER(*," + parts[1] + ")", "NUMBER(" + parts[0] + "," + parts[1] + ")"
	case "TIMESTAMP_NTZ", "TIMESTAMP_LTZ", "TIMESTAMP_TZ", "TIME":
		if hasParam {
			return base, base + "(" + params + ")"
		}
		return base, base
	case "DATETIME":
		return "TIMESTAMP_NTZ", "TIMESTAMP_NTZ"
	}
	canon := cmp.Or(snowflakeSimpleAliases[base], base)
	if hasParam {
		return canon + "(" + params + ")", canon + "(" + params + ")"
	}
	return canon, canon
}

// isSafeWidening reports whether changing from -> to on warehouse wh is a
// strictly lossless widening within the same type family that does not break
// downstream readers (e.g. INT -> BIGINT, FLOAT -> DOUBLE, VARCHAR(50) ->
// VARCHAR(200), DECIMAL(10,2) -> DECIMAL(18,2)). Cross-family changes like
// INT64 -> FLOAT64 or scale changes like DECIMAL(10,0) -> DECIMAL(10,2) are
// NOT considered safe widenings.
func isSafeWidening(wh, from, to string) bool {
	if from == to {
		return false
	}
	switch wh {
	case project.Databricks:
		if intWidth(from) > 0 && intWidth(to) > intWidth(from) {
			return true
		}
		if from == "FLOAT" && to == "DOUBLE" {
			return true
		}
		if isStringWidening(from, to, "STRING") || isDecimalWidening(from, to, "DECIMAL") {
			return true
		}
	case project.BigQuery:
		if isStringWidening(from, to, "STRING") || isBytesWidening(from, to) ||
			isDecimalWidening(from, to, "NUMERIC") || isDecimalWidening(from, to, "BIGNUMERIC") {
			return true
		}
	}
	return false
}

func intWidth(t string) int {
	switch t {
	case "TINYINT":
		return 8
	case "SMALLINT":
		return 16
	case "INT":
		return 32
	case "BIGINT":
		return 64
	}
	return 0
}

func isStringWidening(from, to, unbounded string) bool {
	fb, fp, fHas := splitTypeParam(from)
	tb, tp, tHas := splitTypeParam(to)
	if !fHas || (fb != "VARCHAR" && fb != "CHAR" && fb != unbounded) {
		return false
	}
	fn, err := strconv.Atoi(fp)
	if err != nil {
		return false
	}
	if !tHas && tb == unbounded {
		return true
	}
	if tHas && (tb == "VARCHAR" || tb == unbounded || (fb == "CHAR" && tb == "CHAR")) {
		tn, err := strconv.Atoi(tp)
		return err == nil && (tn > fn || (fb == "CHAR" && tb == "VARCHAR" && tn >= fn))
	}
	return false
}

func isBytesWidening(from, to string) bool {
	fb, fp, fHas := splitTypeParam(from)
	tb, tp, tHas := splitTypeParam(to)
	if !fHas || fb != "BYTES" || tb != "BYTES" {
		return false
	}
	fn, err := strconv.Atoi(fp)
	if err != nil {
		return false
	}
	if !tHas {
		return true
	}
	tn, err := strconv.Atoi(tp)
	return err == nil && tn > fn
}

func isDecimalWidening(from, to, family string) bool {
	fb, fp, fHas := splitTypeParam(from)
	tb, tp, tHas := splitTypeParam(to)
	if fb != family || tb != family || !fHas {
		return false
	}
	if !tHas {
		return true // bounded NUMERIC(p,s) -> unbounded NUMERIC
	}
	fp1, fs1, ok1 := parsePrecisionScale(fp)
	tp2, ts2, ok2 := parsePrecisionScale(tp)
	return ok1 && ok2 && fs1 == ts2 && tp2 > fp1
}

func parsePrecisionScale(params string) (p, s int, ok bool) {
	parts := strings.Split(params, ",")
	if len(parts) == 1 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		return n, 0, err == nil
	}
	if len(parts) == 2 {
		n1, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		n2, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		return n1, n2, err1 == nil && err2 == nil
	}
	return 0, 0, false
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth, paren := 0, 0
	start := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case sep:
			if depth == 0 && paren == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func splitTopLevelColon(s string) (left, right string, ok bool) {
	depth, paren := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case ':':
			if depth == 0 && paren == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}
