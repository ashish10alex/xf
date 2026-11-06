package navigate

import (
	"bytes"
	"slices"
	"strings"
)

// A sqlx file's config block takes the keys Dataform core's sqlx actions
// accept (core/actions/*.ts, verifyConfig, with the legacy sqlx names:
// schema, database, dependencies, bigquery { }), each for the action types
// that read it. configKeys is that list, in the order completion offers
// them: the common keys first.

// configKey is one key a config object takes.
type configKey struct {
	name   string
	in     string   // the key of the object it goes in; "" for the config itself
	detail string   // what it holds, shown beside it in completion: its value's kind · a few words
	doc    string   // what it does, as a hover shows it
	types  string   // the action types that take it, space-separated; "" for all
	values []string // the strings it takes, when they are a fixed set
}

const (
	tableTypes  = "table view incremental"
	outputTypes = "table view incremental operations"
	runTypes    = "table view incremental operations assertion"
	icebergs    = "table incremental"
)

var configKeys = []configKey{
	{name: "type", detail: "string · the kind of action", values: []string{"table", "view", "incremental", "operations", "assertion", "declaration"},
		doc: "The kind of action the file defines: a table rebuilt each run, a view, an incremental table that adds new rows each run, operations (SQL run as written), an assertion (a query whose rows are failures) or a declaration of a table made outside Dataform. A file without it is operations."},
	{name: "schema", detail: "string · the dataset it writes to",
		doc: "The BigQuery dataset the action's table goes in. Defaults to defaultDataset in workflow_settings.yaml (defaultAssertionDataset for an assertion)."},
	{name: "name", detail: "string · its table's name (default: the file's)",
		doc: "The name of the action and its table. Defaults to the file's name."},
	{name: "database", detail: "string · the Google Cloud project it writes to",
		doc: "The Google Cloud project the action's table goes in. Defaults to defaultProject in workflow_settings.yaml."},
	{name: "description", detail: "string · its table's description",
		doc: "A description of the action, set on its table in BigQuery."},
	{name: "tags", detail: "string[] · tags to run it by", types: runTypes,
		doc: "Tags to pick actions by when running: dataform run --tags daily runs the actions tagged daily."},
	{name: "dependencies", detail: "string[] · actions it runs after", types: runTypes,
		doc: "Actions this one runs after, by name, beside those its ref()s already add: for an action whose SQL reads a table without ref(), or one that must run first."},
	{name: "columns", detail: "object · column descriptions", types: outputTypes + " declaration",
		doc: "Descriptions of the table's columns, set in BigQuery: column: \"description\", or column: { description, bigqueryPolicyTags, columns } for a record's fields."},
	{name: "bigquery", detail: "object · partitioning, clustering, labels, options", types: tableTypes,
		doc: "BigQuery options of the table: partitionBy, clusterBy, labels, additionalOptions and the like."},
	{name: "assertions", detail: "object · uniqueKey, nonNull, rowConditions", types: tableTypes,
		doc: "Checks of the table's data, each made into an assertion that runs after it: uniqueKey, uniqueKeys, nonNull and rowConditions."},
	{name: "uniqueKey", detail: "string[] · the columns MERGE matches rows by", types: "incremental",
		doc: "The columns that identify a row. With it, each run merges the new rows in: a row whose key is in the table updates it, any other is inserted. Without it, new rows are appended."},
	{name: "protected", detail: "bool · never rebuild it from scratch", types: "incremental",
		doc: "When true, a full refresh never rebuilds the table from scratch, so the rows it has gathered are kept."},
	{name: "onSchemaChange", detail: "string · what a new or dropped column does", types: "incremental", values: []string{"IGNORE", "FAIL", "EXTEND", "SYNCHRONIZE"},
		doc: "What a run does when the query's columns differ from the table's: IGNORE (the default) leaves the table's columns as they are, FAIL stops the run, EXTEND adds new columns, SYNCHRONIZE adds new columns and drops or renames the others."},
	{name: "incrementalStrategy", detail: "string · how new rows are written", types: "incremental", values: []string{"MERGE", "INSERT_OVERWRITE"},
		doc: "How each run writes its rows: MERGE matches them to the table's rows by uniqueKey, INSERT_OVERWRITE replaces each partition the new rows fall in. Unset, it merges when uniqueKey is set and appends otherwise."},
	{name: "materialized", detail: "bool · a materialized view", types: "view",
		doc: "When true, the view is a BigQuery materialized view, which keeps its results and refreshes them as its tables change."},
	{name: "hasOutput", detail: "bool · the operations make a table others can ref", types: "operations",
		doc: "When true, the operations create a table with the action's name (write it as ${self()}), which other actions can ref()."},
	{name: "disabled", detail: "bool · compiled but not run", types: runTypes,
		doc: "When true, the action is compiled but never run."},
	{name: "dependOnDependencyAssertions", detail: "bool · also wait for its dependencies' assertions", types: runTypes,
		doc: "When true, the action also depends on the assertions of every action it depends on, so it runs only once they pass."},
	{name: "metadata", detail: "object · overview, extraProperties", types: tableTypes + " assertion",
		doc: "More about the action: an overview and any extraProperties."},
	{name: "reservation", detail: "string · the BigQuery reservation it runs in", types: runTypes,
		doc: "The BigQuery reservation the action's queries run in. Defaults to defaultReservation in workflow_settings.yaml. The Dataform CLI only."},
	{name: "hermetic", detail: "bool · reads only its declared dependencies", types: runTypes,
		doc: "Says whether the action reads only the data of its declared dependencies (true) or also data from a source not declared (false)."},

	{in: "bigquery", name: "partitionBy", detail: "string · PARTITION BY expression, e.g. DATE(ts)", types: tableTypes,
		doc: "The table's PARTITION BY expression: a DATE, TIMESTAMP or DATETIME column, DATE(ts), TIMESTAMP_TRUNC(ts, MONTH), or an integer column's RANGE_BUCKET. A query that filters on it scans only the partitions it needs."},
	{in: "bigquery", name: "clusterBy", detail: "string[] · CLUSTER BY columns, up to 4", types: tableTypes,
		doc: "Up to four columns to cluster the table by, most filtered first: a query that filters or groups by them scans less."},
	{in: "bigquery", name: "requirePartitionFilter", detail: "bool · queries must filter on the partition", types: icebergs,
		doc: "When true, BigQuery refuses a query of the table that doesn't filter on its partitioning column."},
	{in: "bigquery", name: "partitionExpirationDays", detail: "number · days a partition is kept", types: icebergs,
		doc: "How many days BigQuery keeps each partition before deleting it."},
	{in: "bigquery", name: "updatePartitionFilter", detail: "string · limits the partitions MERGE scans", types: "incremental",
		doc: "A SQL condition on the table's partitioning column that the merge adds, so a run scans only the partitions new rows can match: \"ts >= DATE_SUB(CURRENT_DATE(), INTERVAL 3 DAY)\". incrementalPredicates replaces it."},
	{in: "bigquery", name: "incrementalPredicates", detail: "string[] · conditions MERGE adds to its ON", types: "incremental",
		doc: "SQL conditions the merge adds to its ON, so a run matches against fewer of the table's rows. Not with updatePartitionFilter."},
	{in: "bigquery", name: "labels", detail: "object · the table's labels", types: tableTypes,
		doc: "BigQuery labels set on the table, as key: \"value\"."},
	{in: "bigquery", name: "additionalOptions", detail: "object · more OPTIONS(), e.g. friendly_name", types: tableTypes,
		doc: "More of the table's OPTIONS(), as name: \"value\". Options with their own key here, as partitionExpirationDays, go there instead."},
	{in: "bigquery", name: "iceberg", detail: "object · a BigLake Iceberg table", types: icebergs,
		doc: "Makes the table a BigLake Iceberg table, its data in Cloud Storage at gs://bucketName/tableFolderRoot/tableFolderSubpath. Unset parts come from defaultIcebergConfig in workflow_settings.yaml."},

	{in: "iceberg", name: "connection", detail: "string · the BigQuery connection", types: icebergs,
		doc: "The BigQuery connection that reaches Cloud Storage: {project}.{location}.{connection_id}, projects/{project}/locations/{location}/connections/{connection_id}, or DEFAULT."},
	{in: "iceberg", name: "bucketName", detail: "string · the Cloud Storage bucket", types: icebergs,
		doc: "The Cloud Storage bucket the table's data is in."},
	{in: "iceberg", name: "tableFolderRoot", detail: "string · the folder in the bucket", types: icebergs,
		doc: "The folder in the bucket that holds the table's folder."},
	{in: "iceberg", name: "tableFolderSubpath", detail: "string · the table's folder under the root", types: icebergs,
		doc: "The table's folder under tableFolderRoot."},
	{in: "iceberg", name: "fileFormat", detail: "string · the files' format", types: icebergs, values: []string{"PARQUET"},
		doc: "The format of the table's data files: only PARQUET."},

	{in: "assertions", name: "uniqueKey", detail: "string[] · columns no two rows share", types: tableTypes,
		doc: "Columns that together are unique: the assertion fails on two rows with the same values in them."},
	{in: "assertions", name: "uniqueKeys", detail: "string[][] · several unique keys", types: tableTypes,
		doc: "Several unique keys, each a list of columns, each checked as uniqueKey is."},
	{in: "assertions", name: "nonNull", detail: "string[] · columns never null", types: tableTypes,
		doc: "Columns that are never null: the assertion fails on a row with a null in one."},
	{in: "assertions", name: "rowConditions", detail: "string[] · SQL conditions every row meets", types: tableTypes,
		doc: "SQL conditions every row meets, as \"amount >= 0\": the assertion fails on a row where one is not true."},

	{in: "metadata", name: "overview", detail: "string · a longer description", types: tableTypes + " assertion",
		doc: "A longer description of the action."},
	{in: "metadata", name: "extraProperties", detail: "object · any other properties", types: tableTypes + " assertion",
		doc: "Any other properties of the action, as key: value."},
}

// takes says whether the key applies to an action of type typ; an unknown
// or unset type takes every key.
func (k configKey) takes(typ string) bool {
	if k.types == "" || !slices.Contains(actionTypes, typ) {
		return true
	}
	return slices.Contains(strings.Fields(k.types), typ)
}

var actionTypes = configKeys[0].values

// configToken is a word, a string or a punctuation character of a config.
type configToken struct {
	kind       byte // 'w' a word, 's' a string, or the punctuation character itself
	start, end int  // a string's start is its opening quote; its end is past its closing one
	text       string
	closed     bool // a string's closing quote was read
}

// configTokens splits a config's body into tokens, leaving out spaces and
// comments, and says where its comments are, from their first character
// to their last. A string not closed ends with its line.
func configTokens(body []byte) (toks []configToken, comments [][2]int) {
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case isSpace(c):
			i++
		case c == '/' && i+1 < len(body) && body[i+1] == '/':
			end := i
			for end < len(body) && body[end] != '\n' {
				end++
			}
			comments = append(comments, [2]int{i, end})
			i = end
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			end := len(body)
			if j := bytes.Index(body[i+2:], []byte("*/")); j >= 0 {
				end = i + 2 + j + 2
			}
			comments = append(comments, [2]int{i, end - 1})
			i = end
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(body) && body[j] != c && body[j] != '\n' {
				if body[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j, len(body))
			t := configToken{kind: 's', start: i, text: string(body[i+1 : j])}
			if j < len(body) && body[j] == c {
				j++
				t.closed = true
			}
			t.end = j
			toks = append(toks, t)
			i = j
		case isIdentByte(c):
			j := i
			for j < len(body) && isIdentByte(body[j]) {
				j++
			}
			toks = append(toks, configToken{kind: 'w', start: i, end: j, text: string(body[i:j])})
			i = j
		default:
			toks = append(toks, configToken{kind: c, start: i, end: i + 1})
			i++
		}
	}
	return toks, comments
}

// keyAt says whether toks[i] is a key: a word or string followed by a
// colon.
func keyAt(toks []configToken, i int) bool {
	return i >= 0 && (toks[i].kind == 'w' || toks[i].kind == 's') && i+1 < len(toks) && toks[i+1].kind == ':'
}

// configPlace is where the cursor is in a config.
type configPlace struct {
	// path holds the keys of the objects the cursor is in, outermost
	// first: none for the config itself, ["bigquery"] in its bigquery { }.
	path []string
	// keyStart is where the key being typed starts, or -1 when the cursor
	// is not where a key goes; keys are the other keys of its object.
	keyStart int
	keys     []string
	// valueKey is the key whose string value the cursor is in, from
	// valueStart; "" when it is in none.
	valueKey   string
	valueStart int
	// typ is the config's type.
	typ string
}

// scanConfig says where cursor is in a config block's body; offsets are
// from the body's start.
func scanConfig(body []byte, cursor int) configPlace {
	place := configPlace{keyStart: -1}
	toks, comments := configTokens(body)
	for _, c := range comments {
		if cursor > c[0] && cursor <= c[1] {
			return place
		}
	}
	place.typ = configType(toks)

	// n tokens start before the cursor; the last is the one being typed
	// when the cursor is in or just after a word, or in a string.
	n := 0
	for n < len(toks) && toks[n].start < cursor {
		n++
	}
	cur := -1
	if n > 0 {
		t := toks[n-1]
		if t.kind == 'w' && cursor <= t.end || t.kind == 's' && (cursor < t.end || !t.closed && cursor == t.end) {
			cur = n - 1
		}
	}
	before := n
	if cur >= 0 {
		before = cur
	}

	stack := configStack(toks, before)
	top := stack[len(stack)-1]
	path, ok := objectPath(stack)
	if !ok {
		return place
	}
	place.path = path

	if cur >= 0 && toks[cur].kind == 's' {
		if keyAt(toks, cur-2) && toks[cur-1].kind == ':' {
			place.valueKey, place.valueStart = toks[cur-2].text, toks[cur].start+1
		}
		return place
	}
	keyStart := cursor
	if cur >= 0 {
		keyStart = toks[cur].start
	}
	// A key goes after the object's brace or a comma, or on the line after
	// the last key's value, before its comma is written.
	if before > 0 {
		switch prev := toks[before-1]; prev.kind {
		case '{', ',':
		case 'w', 's', ']', '}', ')':
			if !bytes.Contains(body[prev.end:keyStart], []byte("\n")) {
				return place
			}
		default:
			return place
		}
	}
	place.keyStart = keyStart
	// The object's other keys, those after the cursor too.
	depth := 0
	for i := top.from; i < len(toks) && depth >= 0; i++ {
		switch toks[i].kind {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		}
		if depth == 0 && i != cur && keyAt(toks, i) {
			place.keys = append(place.keys, toks[i].text)
		}
	}
	return place
}

// configType is a config's type: the string after type: in the config
// itself, or "".
func configType(toks []configToken) string {
	depth := 0
	for i, t := range toks {
		switch t.kind {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		}
		if depth == 0 && keyAt(toks, i) && t.text == "type" && i+2 < len(toks) && toks[i+2].kind == 's' {
			return toks[i+2].text
		}
	}
	return ""
}

// configFrame is an object, array or parentheses open in a config. An
// object's key is the key it is the value of: "" for the config itself and
// "?" for an object that is no key's value (in an array, say).
type configFrame struct {
	open byte
	key  string
	from int // its first token
}

// configStack is what is open before toks[before], the config itself
// first.
func configStack(toks []configToken, before int) []configFrame {
	stack := []configFrame{{open: '{'}}
	for i := 0; i < before; i++ {
		switch t := toks[i]; t.kind {
		case '{':
			key := "?"
			if top := stack[len(stack)-1]; top.open == '{' && top.key != "?" && i >= 2 && toks[i-1].kind == ':' && keyAt(toks, i-2) {
				key = toks[i-2].text
			}
			stack = append(stack, configFrame{open: '{', key: key, from: i + 1})
		case '[', '(':
			stack = append(stack, configFrame{open: t.kind})
		case '}', ']', ')':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return stack
}

// objectPath is the keys of the objects open in stack, outermost first;
// false when the innermost is not an object a key holds.
func objectPath(stack []configFrame) ([]string, bool) {
	top := stack[len(stack)-1]
	if top.open != '{' || top.key == "?" {
		return nil, false
	}
	var path []string
	for _, f := range stack[1:] {
		path = append(path, f.key)
	}
	return path, true
}

// configCompletions are the keys that can go where the cursor is in a
// config, or the values a key's string takes. body is the config's body,
// start its offset in the file, and after the file's text after the
// cursor.
func configCompletions(body []byte, start, cursor int, after []byte) []Completion {
	place := scanConfig(body, cursor-start)
	in := ""
	if len(place.path) > 0 {
		in = place.path[len(place.path)-1]
	}
	if !knownPath(place.path) {
		return nil
	}
	var out []Completion
	if place.valueKey != "" {
		for _, k := range configKeys {
			if k.in != in || k.name != place.valueKey {
				continue
			}
			for _, v := range k.values {
				out = append(out, Completion{Kind: CompletionConfigValue, Label: v, Detail: k.name, InsertText: v, FilterText: v, Start: start + place.valueStart})
			}
		}
		return out
	}
	if place.keyStart < 0 {
		return nil
	}
	// A key being renamed keeps its colon.
	colon := bytes.HasPrefix(bytes.TrimLeft(after, " \t"), []byte(":"))
	for _, k := range configKeys {
		if k.in != in || !k.takes(place.typ) || slices.Contains(place.keys, k.name) {
			continue
		}
		insert := k.name + ": "
		if colon {
			insert = k.name
		}
		out = append(out, Completion{Kind: CompletionConfigKey, Label: k.name, Detail: k.detail, InsertText: insert, FilterText: k.name, Start: start + place.keyStart})
	}
	return out
}

// knownPath says whether path is a chain of objects configKeys knows: the
// config itself, bigquery, bigquery.iceberg, assertions, metadata.
func knownPath(path []string) bool {
	in := ""
	for _, key := range path {
		if !slices.ContainsFunc(configKeys, func(k configKey) bool { return k.in == in && k.name == key && strings.HasPrefix(k.detail, "object") }) {
			return false
		}
		in = key
	}
	return true
}

// ConfigKey is a key of a sqlx file's config, as a hover shows it.
type ConfigKey struct {
	Start, End int    // the key as written, a quoted key's quotes too
	Path       string // where it is: "bigquery.partitionBy"
	Kind       string // its value's: "string", "string[]", "bool", "number" or "object"
	Doc        string
	Values     []string // the strings it takes, when they are a fixed set
	// For holds the action types that take it, nil for every type; the
	// config's own type is ConfigType.
	For        []string
	ConfigType string
}

// ConfigKeyAt is the config key at offset in a sqlx file: one Dataform
// takes, written where it goes (bigquery's keys in bigquery { }).
func ConfigKeyAt(p Project, file string, src []byte, offset int) (ConfigKey, bool) {
	if p.Dbt || isJSFile(file) || offset < 0 || offset > len(src) {
		return ConfigKey{}, false
	}
	r := Regions(src)
	if r.ConfigStart < 0 || offset < r.ConfigStart || offset > r.ConfigEnd {
		return ConfigKey{}, false
	}
	body := src[r.ConfigStart:r.ConfigEnd]
	at := offset - r.ConfigStart
	toks, _ := configTokens(body)
	i := slices.IndexFunc(toks, func(t configToken) bool { return t.start <= at && at <= t.end && (t.kind == 'w' || t.kind == 's') })
	if i < 0 || !keyAt(toks, i) {
		return ConfigKey{}, false
	}
	path, ok := objectPath(configStack(toks, i))
	if !ok || !knownPath(path) {
		return ConfigKey{}, false
	}
	in := ""
	if len(path) > 0 {
		in = path[len(path)-1]
	}
	name := toks[i].text
	k := slices.IndexFunc(configKeys, func(k configKey) bool { return k.in == in && k.name == name })
	if k < 0 {
		return ConfigKey{}, false
	}
	key := configKeys[k]
	kind, _, _ := strings.Cut(key.detail, " · ")
	return ConfigKey{
		Start: r.ConfigStart + toks[i].start, End: r.ConfigStart + toks[i].end,
		Path: strings.Join(append(path, name), "."), Kind: kind, Doc: key.doc,
		Values: key.values, For: strings.Fields(key.types), ConfigType: configType(toks),
	}, true
}

// Unread says whether the config's type is one the key isn't for: an
// incremental table's uniqueKey in a view's config.
func (k ConfigKey) Unread() bool {
	return len(k.For) > 0 && slices.Contains(actionTypes, k.ConfigType) && !slices.Contains(k.For, k.ConfigType)
}
