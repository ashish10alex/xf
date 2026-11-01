package navigate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const parametersJS = `// Dates the reports start from.
var START_DATE = "2025-03-29";
const COUNTRIES = [
  "gb",
  "us",
];
let LONG = "a" +
  "b"
const EXPR = dataform.projectConfig.vars.start
  .trim()
const NEXT = 1

/**
 * Picks the platform of a model.
 * @param id the model's id
 */
function platform(id) {
  const START_DATE = "inner";
  return id;
}

const arrow = (a, b) => {
  return a + b;
};

class Thing extends Base {
  go() {}
}

exports.LATER = "later";

module.exports = {
  START_DATE, COUNTRIES, platform,
  // A key with its own value.
  KEYED: {
    deep: 1,
  },
  LAST: 3
};
`

func includeProject(t *testing.T) Project {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "includes", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"includes/parameters.js": parametersJS,
		"includes/sub/hidden.js": "const H = 1;\nmodule.exports = { H };\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Project{Root: root}
}

func TestIncludeAt(t *testing.T) {
	p := includeProject(t)
	tests := []struct {
		name, file, src, needle string
		code, doc               string
		line                    int // 0-based line of the name in parameters.js
	}{
		{"var in a ${}", "definitions/a.sqlx", `SELECT '${parameters.START|_DATE}'`, "START|_DATE", `var START_DATE = "2025-03-29"`, "Dates the reports start from.", 1},
		{"cursor at the name's end", "definitions/a.sqlx", `SELECT ${parameters.START_DATE}`, "START_DATE|}", `var START_DATE = "2025-03-29"`, "Dates the reports start from.", 1},
		{"array over lines", "definitions/a.js", `publish("x").query(parameters.COUN|TRIES)`, "COUN|TRIES", "const COUNTRIES = [\n  \"gb\",\n  \"us\",\n]", "", 2},
		{"value over a trailing operator", "definitions/a.js", `parameters.LO|NG`, "LO|NG", "let LONG = \"a\" +\n  \"b\"", "", 6},
		{"value over a leading dot", "definitions/a.js", `parameters.EX|PR`, "EX|PR", "const EXPR = dataform.projectConfig.vars.start\n  .trim()", "", 8},
		{"function signature with jsdoc", "definitions/a.sqlx", "js { const p = parameters.plat|form(1); }", "plat|form", "function platform(id)", "Picks the platform of a model.\n@param id the model's id", 16},
		{"arrow function", "definitions/a.sqlx", "config { x: parameters.arr|ow }", "arr|ow", "const arrow = (a, b) =>", "", 21},
		{"class", "definitions/a.js", "parameters.Th|ing", "Th|ing", "class Thing extends Base", "", 25},
		{"exports assignment", "definitions/a.js", "parameters.LA|TER", "LA|TER", `exports.LATER = "later"`, "", 29},
		{"key of module.exports", "definitions/a.js", "parameters.KEY|ED", "KEY|ED", "KEYED: {\n  deep: 1,\n}", "A key with its own value.", 34},
		{"last key without a comma", "definitions/a.js", "parameters.LA|ST", "LA|ST", "LAST: 3", "", 37},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := []byte(strings.Replace(tt.src, "|", "", 1))
			inc, ok := IncludeAt(p, filepath.Join(p.Root, tt.file), src, cursorAt(t, src, tt.needle))
			if !ok {
				t.Fatal("found nothing")
			}
			if inc.Code != tt.code {
				t.Errorf("code %q, want %q", inc.Code, tt.code)
			}
			if inc.Doc != tt.doc {
				t.Errorf("doc %q, want %q", inc.Doc, tt.doc)
			}
			if want := filepath.Join(p.Root, "includes", "parameters.js"); inc.File != want || inc.Line != tt.line {
				t.Errorf("at %s:%d, want %s:%d", inc.File, inc.Line, want, tt.line)
			}
			if got := string(src[inc.Start:inc.End]); got[:len("parameters.")] != "parameters." {
				t.Errorf("range %q, want parameters.…", got)
			}
		})
	}
}

func TestIncludeAtNothing(t *testing.T) {
	p := includeProject(t)
	tests := []struct{ name, file, src, needle string }{
		{"sql outside javascript", "definitions/a.sqlx", "SELECT parameters.START|_DATE FROM t", "START|_DATE"},
		{"the file's own name", "definitions/a.sqlx", "${parame|ters.START_DATE}", "parame|ters"},
		{"no such include", "definitions/a.sqlx", "${nope.START|_DATE}", "START|_DATE"},
		{"not declared", "definitions/a.sqlx", "${parameters.MISS|ING}", "MISS|ING"},
		{"declared only inside a function", "definitions/a.js", "parameters.retu|rn", "retu|rn"},
		{"a nested key", "definitions/a.js", "parameters.de|ep", "de|ep"},
		{"a member of a member", "definitions/a.js", "x.parameters.START|_DATE", "START|_DATE"},
		{"includes in a folder are no globals", "definitions/a.js", "sub.H|", "H|"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := []byte(strings.Replace(tt.src, "|", "", 1))
			if inc, ok := IncludeAt(p, filepath.Join(p.Root, tt.file), src, cursorAt(t, src, tt.needle)); ok {
				t.Fatalf("found %+v", inc)
			}
		})
	}
}

func TestIncludeAtPrefersTopLevel(t *testing.T) {
	p := includeProject(t)
	src := []byte("parameters.START_DATE")
	inc, ok := IncludeAt(p, filepath.Join(p.Root, "definitions/a.js"), src, 12)
	if !ok || inc.Line != 1 {
		t.Fatalf("got %+v, want line 1, not the function's own const", inc)
	}
}

func TestDefinitionOfInclude(t *testing.T) {
	p := navProject(t)
	for _, tt := range []struct{ needle, want string }{
		{"constants.SCH|EMA", "includes/constants.js:0:6"},
		{"helpers.dateFil|ter", "includes/helpers.js:2:9"},
	} {
		if got := define(t, p, stgEvents, nil, tt.needle); !slices.Equal(got, []string{tt.want}) {
			t.Errorf("%s: got %v, want %s", tt.needle, got, tt.want)
		}
	}
}
