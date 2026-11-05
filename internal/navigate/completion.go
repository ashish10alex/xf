package navigate

import (
	"cmp"
	"regexp"
	"slices"

	"github.com/ashish10alex/xf/internal/project"
)

// CompletionKind says what a completion inserts.
type CompletionKind int

const (
	CompletionRef CompletionKind = iota
	CompletionDependency
	CompletionTag
	// CompletionMacro is a dbt macro call.
	CompletionMacro
	// CompletionConfigKey is a key of a sqlx config, or of an object in
	// it (bigquery { }, assertions { }).
	CompletionConfigKey
	// CompletionConfigValue is one of the strings a config key takes.
	CompletionConfigValue
)

// Completion is one suggestion: InsertText replaces the source from Start to
// the cursor, and FilterText is what editors match the typed text against.
type Completion struct {
	Kind                                  CompletionKind
	Label, Detail, InsertText, FilterText string
	Start                                 int
}

// arrayKey finds the key an array value belongs to: `tags: [` or
// `"dependencies": [`.
var arrayKey = regexp.MustCompile(`(\w+)["']?\s*:\s*$`)

// Completions returns what can be typed at offset in a sqlx file: a
// ${ref("…")} for every action after a `$` in SQL, action names inside a
// config's dependencies array and the project's tags inside its tags
// array, and the config's keys and the values of those that take a fixed
// set (config.go), which need no compiled project.
// In a dbt project's file it is names inside ref('…') and source('…', '…'),
// macros in a tag, and whole ref() and source() calls at the start of a
// {{ }}.
func Completions(p Project, file string, src []byte, offset int) []Completion {
	if isJSFile(file) || offset < 0 || offset > len(src) {
		return nil
	}
	if p.Dbt {
		if p.Graph == nil {
			return nil
		}
		return dbtCompletions(p, file, src, offset)
	}
	if start, ok := dollarAt(src, offset); ok {
		if p.Graph == nil {
			return nil
		}
		return refCompletions(p.Graph, start)
	}
	r := Regions(src)
	if r.ConfigStart < 0 || offset < r.ConfigStart || offset > r.ConfigEnd {
		return nil
	}
	key, start, ok := configArrayString(src[r.ConfigStart:offset])
	if !ok {
		return configCompletions(src[r.ConfigStart:r.ConfigEnd], r.ConfigStart, offset, src[offset:])
	}
	if p.Graph == nil {
		return nil
	}
	start += r.ConfigStart
	switch key {
	case "dependencies":
		var out []Completion
		for _, a := range completableActions(p.Graph, true) {
			out = append(out, Completion{
				Kind: CompletionDependency, Label: a.CanonicalTarget.Name, Detail: actionDetail(a),
				InsertText: a.CanonicalTarget.Name, FilterText: a.CanonicalTarget.Name, Start: start,
			})
		}
		return out
	case "tags":
		var tags []string
		for _, a := range p.Graph.Actions {
			tags = append(tags, a.Tags...)
		}
		slices.Sort(tags)
		var out []Completion
		for _, tag := range slices.Compact(tags) {
			out = append(out, Completion{Kind: CompletionTag, Label: tag, Detail: "tag", InsertText: tag, FilterText: tag, Start: start})
		}
		return out
	}
	return nil
}

// dollarAt reports whether the cursor follows a `$` and an optional word in
// SQL (not in a config or js block, a ${...} or a comment), and where the $
// is.
func dollarAt(src []byte, offset int) (int, bool) {
	i := offset
	for i > 0 && isWordByte(src[i-1]) {
		i--
	}
	if i == 0 || src[i-1] != '$' || i < len(src) && src[i] == '{' {
		return 0, false
	}
	d := i - 1
	if commentChecker(src)(d) {
		return 0, false
	}
	for _, s := range jsSpans(src) {
		if d >= s.start && d < s.end {
			return 0, false
		}
	}
	return d, true
}

func refCompletions(g *project.Graph, start int) []Completion {
	actions := completableActions(g, false)
	count := map[string]int{}
	for _, a := range actions {
		count[a.CanonicalTarget.Name]++
	}
	var out []Completion
	for _, a := range actions {
		t := a.CanonicalTarget
		insert := `${ref("` + t.Name + `")}`
		if count[t.Name] > 1 {
			insert = `${ref("` + t.Schema + `", "` + t.Name + `")}`
		}
		out = append(out, Completion{
			Kind: CompletionRef, Label: t.Name, Detail: actionDetail(a),
			InsertText: insert, FilterText: "$" + t.Name, Start: start,
		})
	}
	return out
}

// completableActions are the actions one can depend on, sorted by name;
// assertions only when asked for (nothing reads from them, but dependencies
// can wait for them).
func completableActions(g *project.Graph, withAssertions bool) []*project.Action {
	var out []*project.Action
	for _, a := range g.Actions {
		if a.Kind == project.KindUnknown || a.Kind == project.KindAssertion && !withAssertions {
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y *project.Action) int {
		return cmp.Or(cmp.Compare(x.CanonicalTarget.Name, y.CanonicalTarget.Name), cmp.Compare(x.CanonicalTarget.Schema, y.CanonicalTarget.Schema))
	})
	return out
}

func actionDetail(a *project.Action) string {
	return a.CanonicalTarget.Schema + " · " + a.Kind.String()
}

// configArrayString reports whether the end of text (a config block up to
// the cursor) is inside a string in an array, which key the array belongs
// to, and where the string's contents start.
func configArrayString(text []byte) (key string, start int, ok bool) {
	var arrays []int // offsets of the open [s
	var quote byte
	strStart := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			case '\n':
				quote = 0 // an unterminated string ends with its line
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote, strStart = c, i+1
		case '[':
			arrays = append(arrays, i)
		case ']':
			if len(arrays) > 0 {
				arrays = arrays[:len(arrays)-1]
			}
		}
	}
	if quote == 0 || len(arrays) == 0 {
		return "", 0, false
	}
	m := arrayKey.FindSubmatch(text[:arrays[len(arrays)-1]])
	if m == nil {
		return "", 0, false
	}
	return string(m[1]), strStart, true
}
