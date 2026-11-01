package web

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

func TestGlobs(t *testing.T) {
	cases := []struct {
		glob string
		yes  []string
		no   []string
	}{
		{"*.md", []string{"README.md", "docs/a.md"}, []string{"a.mdx", "md/x.js"}},
		{"includes/", []string{"includes/x.js", "a/includes/y.js"}, []string{"includes.js"}},
		{"node_modules", []string{"node_modules/a.js", "x/node_modules/b.js"}, []string{"node_modules_x/a.js"}},
		{"definitions/**", []string{"definitions/a.sqlx", "definitions/m/b.sqlx"}, []string{"x/definitions/a.sqlx"}},
		{"definitions/*.sqlx", []string{"definitions/a.sqlx"}, []string{"definitions/m/b.sqlx"}},
		{"**/*.{js,json}", []string{"a.js", "x/y/b.json"}, []string{"c.sqlx"}},
		{"definitions/**/orders.sqlx", []string{"definitions/orders.sqlx", "definitions/m/orders.sqlx"}, []string{"orders.sqlx"}},
	}
	for _, c := range cases {
		gs, err := globsOf(c.glob)
		if err != nil {
			t.Fatalf("%s: %v", c.glob, err)
		}
		for _, p := range c.yes {
			if !gs.match(p) {
				t.Errorf("%s doesn't match %s", c.glob, p)
			}
		}
		for _, p := range c.no {
			if gs.match(p) {
				t.Errorf("%s matches %s", c.glob, p)
			}
		}
	}
	gs, err := globsOf("*.md, includes/ ,{a,b}.js")
	if err != nil || len(gs) != 3 || !gs.match("b.js") || !gs.match("includes/x") {
		t.Errorf("list: %d globs, %v", len(gs), err)
	}
	if _, err := globsOf("{a,b"); err == nil {
		t.Error("an open brace is a glob")
	}
}

func searchTest(t *testing.T, files map[string]string) (string, func(searchRequest) searchView, func(any, any)) {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	search := func(req searchRequest) searchView {
		t.Helper()
		var v searchView
		postJSON(t, c, ts.URL+"/api/search", req, &v)
		return v
	}
	post := func(body, v any) {
		t.Helper()
		path := "/api/search/replace"
		if body == nil {
			path, body = "/api/search/undo", map[string]any{}
		}
		postJSON(t, c, ts.URL+path, body, v)
	}
	return root, search, post
}

func q(query string) searchRequest { return searchRequest{searchOptions: searchOptions{Query: query}} }

// summary is a search's matches as "path:line:col+len", in order.
func summary(v searchView) string {
	var out []string
	for _, f := range v.Files {
		for _, m := range f.Matches {
			out = append(out, f.Path+":"+strconv.Itoa(m.Line)+":"+strconv.Itoa(m.Col)+"+"+strconv.Itoa(m.Len))
		}
	}
	return strings.Join(out, " ")
}

func TestSearchMatches(t *testing.T) {
	_, search, _ := searchTest(t, map[string]string{
		"definitions/orders.sqlx": "SELECT order_id FROM ${ref(\"Orders\")}\n-- orders\n",
		"definitions/émoji.sqlx":  "-- 🙂 orders here\n",
		"README.md":               "orders_v2 and reorders\n",
		"bin.dat":                 "orders\x00\x01",
		"node_modules/a.js":       "orders\n",
	})
	// Case doesn't count, a file of bytes and packages are left out, and
	// columns count UTF-16 units.
	got := summary(search(q("orders")))
	want := "README.md:0:0+6 README.md:0:16+6 definitions/orders.sqlx:0:28+6 definitions/orders.sqlx:1:3+6 definitions/émoji.sqlx:0:6+6"
	if got != want {
		t.Errorf("orders:\n got %s\nwant %s", got, want)
	}
	r := q("Orders")
	r.Case = true
	if got := summary(search(r)); got != "definitions/orders.sqlx:0:28+6" {
		t.Errorf("case: %s", got)
	}
	r = q("orders")
	r.Word = true
	if got := summary(search(r)); got != "definitions/orders.sqlx:0:28+6 definitions/orders.sqlx:1:3+6 definitions/émoji.sqlx:0:6+6" {
		t.Errorf("word: %s", got)
	}
	// ^ is a line's start.
	r = q(`^-- (\w+)`)
	r.Regex, r.Replacing, r.Replace = true, true, "// $1!"
	v := search(r)
	if got := summary(v); got != "definitions/orders.sqlx:1:0+9" {
		t.Errorf("regex: %s", got)
	}
	if m := v.Files[0].Matches[0]; m.With != "// orders!" || m.Text != "-- orders" || m.LineText != "-- orders" || m.At != 0 {
		t.Errorf("regex match = %+v", m)
	}
	r = q("(")
	r.Regex = true
	if v := search(r); v.Error == "" || len(v.Files) != 0 {
		t.Errorf("bad regex = %+v", v)
	}
	// The globs narrow the files.
	r = q("orders")
	r.Include, r.Exclude = "definitions/", "émoji*"
	if got := summary(search(r)); got != "definitions/orders.sqlx:0:28+6 definitions/orders.sqlx:1:3+6" {
		t.Errorf("globs: %s", got)
	}
	// The source pane's unsaved text is searched in place of the file's.
	r = q("orders")
	r.Include = "*.sqlx"
	r.Shown = &struct {
		File string `json:"file"`
		Text string `json:"text"`
	}{"definitions/orders.sqlx", "nothing\n\nnew orders\n"}
	if got := summary(search(r)); got != "definitions/orders.sqlx:2:4+6 definitions/émoji.sqlx:0:6+6" {
		t.Errorf("shown: %s", got)
	}
}

func TestSearchSnippetAndCap(t *testing.T) {
	long := strings.Repeat("x", 100) + " orders " + strings.Repeat("y", 300)
	many := strings.Repeat("a\n", maxSearchMatches+10)
	_, search, _ := searchTest(t, map[string]string{"long.sql": long + "\n", "many.txt": many})
	v := search(q("orders"))
	m := v.Files[0].Matches[0]
	if !strings.HasPrefix(m.Before, "…") || len([]rune(m.Before)) != snippetBefore+1 || !strings.HasSuffix(m.After, "…") {
		t.Errorf("snippet = %q / %q", m.Before, m.After)
	}
	v = search(q("a"))
	if !v.Truncated || v.Count != maxSearchMatches {
		t.Errorf("cap: count %d, truncated %v", v.Count, v.Truncated)
	}
}

type replaceFileReq struct {
	Path    string        `json:"path"`
	Hash    string        `json:"hash"`
	Matches []searchMatch `json:"matches"`
}

func TestReplaceAndUndo(t *testing.T) {
	root, search, post := searchTest(t, map[string]string{
		"a.sqlx": "ref(\"orders\")\nref(\"orders\")\n",
		"d.sqlx": "orders\norders\n",
		"b.sqlx": "x orders\r\ny orders\r\n",
		"c.sqlx": "orders\n",
	})
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := q("orders")
	r.Replacing, r.Replace = true, "orders_v2"
	v := search(r)
	if v.Count != 7 {
		t.Fatalf("count %d", v.Count)
	}
	byPath, hashes := map[string][]searchMatch{}, map[string]string{}
	for _, f := range v.Files {
		byPath[f.Path], hashes[f.Path] = f.Matches, f.Hash
	}
	// a.sqlx gains a line above its matches, and c.sqlx's match goes,
	// after the search; d.sqlx stays as it was.
	write("a.sqlx", "-- new\n"+strings.Replace(read("a.sqlx"), "ref(", "x(", 1))
	write("c.sqlx", "customers\n")
	req := map[string]any{
		"query": "orders", "replacing": true, "replace": "orders_v2",
		"files": []replaceFileReq{
			{"a.sqlx", hashes["a.sqlx"], byPath["a.sqlx"][1:]}, // the second one only
			{"b.sqlx", hashes["b.sqlx"], byPath["b.sqlx"]},
			{"c.sqlx", hashes["c.sqlx"], byPath["c.sqlx"]},
			{"d.sqlx", hashes["d.sqlx"], byPath["d.sqlx"][1:]},
		},
	}
	var rv replaceView
	post(req, &rv)
	if rv.Replaced != 4 || rv.Files != 3 || rv.Skipped != 1 || !rv.Undo {
		t.Errorf("replace = %+v", rv)
	}
	if got := read("a.sqlx"); got != "-- new\nx(\"orders\")\nref(\"orders_v2\")\n" {
		t.Errorf("a.sqlx = %q", got)
	}
	// Of two lines alike, the one asked for.
	if got := read("d.sqlx"); got != "orders\norders_v2\n" {
		t.Errorf("d.sqlx = %q", got)
	}
	// The file's line ends are kept.
	if got := read("b.sqlx"); got != "x orders_v2\r\ny orders_v2\r\n" {
		t.Errorf("b.sqlx = %q", got)
	}
	// An undo puts back the files that didn't change since.
	write("b.sqlx", "edited\n")
	var uv undoView
	post(nil, &uv)
	if uv.Restored != 2 || len(uv.Skipped) != 1 || uv.Skipped[0] != "b.sqlx" {
		t.Errorf("undo = %+v", uv)
	}
	if got := read("a.sqlx"); got != "-- new\nx(\"orders\")\nref(\"orders\")\n" {
		t.Errorf("a.sqlx after undo = %q", got)
	}
}
