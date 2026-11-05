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
	detail string   // what it holds, shown beside it
	types  string   // the action types that take it, space-separated; "" for all
	values []string // the strings it takes, when they are a fixed set
}

const (
	tableTypes  = "table view incremental"
	outputTypes = "table view incremental operations"
	runTypes    = "table view incremental operations assertion"
)

var configKeys = []configKey{
	{name: "type", detail: "string · the kind of action", values: []string{"table", "view", "incremental", "operations", "assertion", "declaration"}},
	{name: "schema", detail: "string · the dataset it writes to"},
	{name: "name", detail: "string · its table's name (default: the file's)"},
	{name: "database", detail: "string · the Google Cloud project it writes to"},
	{name: "description", detail: "string · its table's description"},
	{name: "tags", detail: "string[] · tags to run it by", types: runTypes},
	{name: "dependencies", detail: "string[] · actions it runs after", types: runTypes},
	{name: "columns", detail: "object · column descriptions", types: outputTypes + " declaration"},
	{name: "bigquery", detail: "object · partitioning, clustering, labels, options", types: tableTypes},
	{name: "assertions", detail: "object · uniqueKey, nonNull, rowConditions", types: tableTypes},
	{name: "uniqueKey", detail: "string[] · the columns MERGE matches rows by", types: "incremental"},
	{name: "protected", detail: "bool · never rebuild it from scratch", types: "incremental"},
	{name: "onSchemaChange", detail: "string · what a new or dropped column does", types: "incremental", values: []string{"IGNORE", "FAIL", "EXTEND", "SYNCHRONIZE"}},
	{name: "incrementalStrategy", detail: "string · how new rows are written", types: "incremental", values: []string{"MERGE", "INSERT_OVERWRITE"}},
	{name: "materialized", detail: "bool · a materialized view", types: "view"},
	{name: "hasOutput", detail: "bool · the operations make a table others can ref", types: "operations"},
	{name: "disabled", detail: "bool · compiled but not run", types: runTypes},
	{name: "dependOnDependencyAssertions", detail: "bool · also wait for its dependencies' assertions", types: runTypes},
	{name: "metadata", detail: "object · overview, extraProperties", types: tableTypes + " assertion"},
	{name: "reservation", detail: "string · the BigQuery reservation it runs in", types: runTypes},
	{name: "hermetic", detail: "bool · fail on a dependency not declared", types: runTypes},

	{in: "bigquery", name: "partitionBy", detail: "string · PARTITION BY expression, e.g. DATE(ts)", types: tableTypes},
	{in: "bigquery", name: "clusterBy", detail: "string[] · CLUSTER BY columns, up to 4", types: tableTypes},
	{in: "bigquery", name: "requirePartitionFilter", detail: "bool · queries must filter on the partition", types: "table incremental"},
	{in: "bigquery", name: "partitionExpirationDays", detail: "number · days a partition is kept", types: "table incremental"},
	{in: "bigquery", name: "updatePartitionFilter", detail: "string · limits the partitions MERGE scans", types: "incremental"},
	{in: "bigquery", name: "incrementalPredicates", detail: "string[] · conditions MERGE adds to its ON", types: "incremental"},
	{in: "bigquery", name: "labels", detail: "object · the table's labels", types: tableTypes},
	{in: "bigquery", name: "additionalOptions", detail: "object · more OPTIONS(), e.g. friendly_name", types: tableTypes},
	{in: "bigquery", name: "iceberg", detail: "object · a BigLake Iceberg table", types: "table incremental"},

	{in: "iceberg", name: "connection", detail: "string · the BigQuery connection", types: "table incremental"},
	{in: "iceberg", name: "bucketName", detail: "string · the Cloud Storage bucket", types: "table incremental"},
	{in: "iceberg", name: "tableFolderRoot", detail: "string · the folder in the bucket", types: "table incremental"},
	{in: "iceberg", name: "tableFolderSubpath", detail: "string · the table's folder under the root", types: "table incremental"},
	{in: "iceberg", name: "fileFormat", detail: "string · the files' format", types: "table incremental", values: []string{"PARQUET"}},

	{in: "assertions", name: "uniqueKey", detail: "string[] · columns no two rows share", types: tableTypes},
	{in: "assertions", name: "uniqueKeys", detail: "string[][] · several unique keys", types: tableTypes},
	{in: "assertions", name: "nonNull", detail: "string[] · columns never null", types: tableTypes},
	{in: "assertions", name: "rowConditions", detail: "string[] · SQL conditions every row meets", types: tableTypes},

	{in: "metadata", name: "overview", detail: "string · a longer description", types: tableTypes + " assertion"},
	{in: "metadata", name: "extraProperties", detail: "object · any other properties", types: tableTypes + " assertion"},
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
	// The config's own type: a string after type: in the config itself.
	depth := 0
	for i, t := range toks {
		switch t.kind {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		}
		if depth == 0 && keyAt(toks, i) && t.text == "type" && i+2 < len(toks) && toks[i+2].kind == 's' {
			place.typ = toks[i+2].text
		}
	}

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

	// The objects, arrays and parentheses open at the cursor. An object's
	// key is the key it is the value of; "" for the config itself and "?"
	// for an object that is no key's value (in an array, say).
	type frame struct {
		open byte
		key  string
		from int // its first token
	}
	stack := []frame{{open: '{'}}
	for i := 0; i < before; i++ {
		switch t := toks[i]; t.kind {
		case '{':
			key := "?"
			if top := stack[len(stack)-1]; top.open == '{' && top.key != "?" && i >= 2 && toks[i-1].kind == ':' && keyAt(toks, i-2) {
				key = toks[i-2].text
			}
			stack = append(stack, frame{open: '{', key: key, from: i + 1})
		case '[', '(':
			stack = append(stack, frame{open: t.kind})
		case '}', ']', ')':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	top := stack[len(stack)-1]
	if top.open != '{' || top.key == "?" {
		return place
	}
	for _, f := range stack[1:] {
		place.path = append(place.path, f.key)
	}

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
	depth = 0
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
