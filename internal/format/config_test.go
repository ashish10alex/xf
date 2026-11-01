package format

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConfigFormatsTheBody(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{
			name: "one line",
			in:   `config { type: "table", tags: ["a","b"], disabled: true }` + "\nselect 1\n",
			want: "config {\n  type: \"table\",\n  tags: [\"a\", \"b\"],\n  disabled: true\n}\nselect 1\n",
		},
		{
			name: "nested objects open, trailing comma goes, key spacing",
			in: `config {
type:"incremental",
    bigquery: { partitionBy: "d", clusterBy: ["a"], },
  assertions: {uniqueKey: ["id"]},
}`,
			want: `config {
  type: "incremental",
  bigquery: {
    partitionBy: "d",
    clusterBy: ["a"]
  },
  assertions: {
    uniqueKey: ["id"]
  }
}`,
		},
		{
			name: "an array that doesn't fit takes a line per item",
			in:   `config { tags: ["aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc", "dddddddddddddddd"] }`,
			want: "config {\n  tags: [\n    \"aaaaaaaaaaaaaaaa\",\n    \"bbbbbbbbbbbbbbbbbbbb\",\n    \"cccccccccccccccccccc\",\n    \"dddddddddddddddd\"\n  ]\n}",
		},
		{
			name: "an array that was spread out but fits goes on one line",
			in:   "config {\n  tags: [\n    \"a\",\n    \"b\"\n  ]\n}",
			want: "config {\n  tags: [\"a\", \"b\"]\n}",
		},
		{
			name: "comments stay, and an array with one stays spread out",
			in: `config {
  // what it is
  type: "table", // a table
  tags: [
    "a", // first
    "b"
  ],
  /* the end */
}`,
			want: `config {
  // what it is
  type: "table", // a table
  tags: [
    "a", // first
    "b"
  ]
  /* the end */
}`,
		},
		{
			name: "one blank line between properties stays",
			in:   "config {\n  type: \"table\",\n\n\n  tags: [\"a\"],\n\n  // disabled: true\n}",
			want: "config {\n  type: \"table\",\n\n  tags: [\"a\"]\n\n  // disabled: true\n}",
		},
		{
			name: "other values are kept as written",
			in: "config {\n  type: \"table\",\n  schema: dataform.projectConfig.vars.env === \"prod\" ? \"p\" : \"d\",\n" +
				"  description: `rows of ${ctx.self()}`,\n  hasOutput: !false,\n  ...common,\n  shorthand,\n  n: -1\n}",
			want: "config {\n  type: \"table\",\n  schema: dataform.projectConfig.vars.env === \"prod\" ? \"p\" : \"d\",\n" +
				"  description: `rows of ${ctx.self()}`,\n  hasOutput: !false,\n  ...common,\n  shorthand,\n  n: -1\n}",
		},
		{
			name: "an array of objects",
			in:   `config { columns: [{name: "a"}, {}], e: [] }`,
			want: "config {\n  columns: [\n    {\n      name: \"a\"\n    },\n    {}\n  ],\n  e: []\n}",
		},
		{
			name: "quoted keys and quotes stay",
			in:   `config { "type": 'view', 'my-key': "x" }`,
			want: "config {\n  \"type\": 'view',\n  'my-key': \"x\"\n}",
		},
		{
			name: "an indented config block",
			in:   "  config {type: \"view\"}",
			want: "  config {\n    type: \"view\"\n  }",
		},
		{
			name: "CRLF line endings stay",
			in:   "config {\r\ntype: \"view\"\r\n}\r\nselect 1\r\n",
			want: "config {\r\n  type: \"view\"\r\n}\r\nselect 1\r\n",
		},
		{
			name: "an empty body is left as it is",
			in:   "config {}\nselect 1",
			want: "config {}\nselect 1",
		},
		{
			name: "a multi-line value is kept as written",
			in:   "config {\n  type: \"table\",\n  description: `one\n two`,\n  f: fn(a,\n    b)\n}",
			want: "config {\n  type: \"table\",\n  description: `one\n two`,\n  f: fn(a,\n    b)\n}",
		},
		{
			name: "a regex is one value, commas in it included",
			in:   "config {\n  re: /a,b[/]/g.test(s), half: n / 2, c: x?.y ?? \"d\"\n}",
			want: "config {\n  re: /a,b[/]/g.test(s),\n  half: n / 2,\n  c: x?.y ?? \"d\"\n}",
		},
		{
			name: "a comment before a comma, and one after the brace",
			in:   "config { // top\n  a: 1 /* one */, b: 2\n}",
			want: "config {\n  // top\n  a: 1, /* one */\n  b: 2\n}",
		},
		{
			name: "methods, arrow functions and calls on arrays are kept as written",
			in:   "config {\n  dependencies: [\"a\"].concat(more),\n  f() { return 1 },\n  g: (x) => { return x },\n}",
			want: "config {\n  dependencies: [\"a\"].concat(more),\n  f() { return 1 },\n  g: (x) => { return x }\n}",
		},
		{
			name: "a list with only a comment stays open",
			in:   "config {\n  tags: [ /* none */ ],\n  bigquery: {\n  // later\n  }\n}",
			want: "config {\n  tags: [\n    /* none */\n  ],\n  bigquery: {\n    // later\n  }\n}",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Config([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, c.want)
			}
			again, err := Config(got)
			if err != nil || string(again) != string(got) {
				t.Errorf("formatting twice changed it again (%v):\n%s", err, again)
			}
		})
	}
}

func TestConfigLeavesTheRestOfTheFile(t *testing.T) {
	src := "-- config { not: a block }\njs {\n  const a = {b:1};\n}\nconfig {type:\"view\"}\npre_operations {\n  select   1\n}\nselect '${x}'   from y\n"
	got, err := Config([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, `config {type:"view"}`, "config {\n  type: \"view\"\n}", 1)
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestConfigRefusesWhatItCantRead(t *testing.T) {
	for _, c := range []struct {
		in        string
		line, col int
		msg       string
	}{
		{"config {\n  type: \"table\"\n  tags: []\n}", 3, 3, "a , is missing before this"},
		{"config {\n  type: // the type\n    \"table\"\n}", 2, 9, "a comment between a key and its value"},
		{"config {\n  type: \"table\n}", 2, 9, "a string is never closed"},
		{"config {\n  a: 1,,\n}", 2, 8, "a value is missing"},
		{"config {\n  a: 1;\n}", 2, 7, "expected a , here"},
	} {
		_, err := Config([]byte(c.in))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%q: got %v, want a ParseError", c.in, err)
			continue
		}
		if pe.Line != c.line || pe.Col != c.col || !strings.Contains(pe.Msg, c.msg) {
			t.Errorf("%q: got %v, want line %d, column %d: …%s", c.in, pe, c.line, c.col, c.msg)
		}
	}
	if _, err := Config([]byte("config {\n  type: \"table\"\n")); err == nil {
		t.Error("a config block that's never closed was formatted")
	}
}

func TestSQLXFormatsConfigAndSQL(t *testing.T) {
	r := &recorder{}
	out, err := SQLX(context.Background(), []byte("config {type:\"view\"}\nselect 1\n"), opts(r.run))
	if err != nil {
		t.Fatal(err)
	}
	if want := "config {\n  type: \"view\"\n}\nSELECT 1\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSQLXFormatsConfigWithoutSQLFluff(t *testing.T) {
	src := "config {type:\"view\"}\nselect 1\n"
	out, err := SQLX(context.Background(), []byte(src), Options{})
	var pe *PartialError
	if !errors.As(err, &pe) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want a PartialError for sqlfluff not found", err)
	}
	if want := "config {\n  type: \"view\"\n}\nselect 1\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if !strings.HasPrefix(err.Error(), "formatted the config block, not the SQL: sqlfluff not found") {
		t.Errorf("message: %v", err)
	}

	// A config already formatted: nothing changed, so the error alone.
	out, err = SQLX(context.Background(), []byte("config {\n  type: \"view\"\n}\nselect 1\n"), Options{})
	if out != nil || !errors.Is(err, ErrNotFound) || errors.As(err, &pe) {
		t.Errorf("got %q, %v; want no text and sqlfluff not found", out, err)
	}

	// No SQL at all: sqlfluff isn't needed.
	out, err = SQLX(context.Background(), []byte("config {type:\"declaration\"}\n"), Options{})
	if err != nil || string(out) != "config {\n  type: \"declaration\"\n}\n" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestSQLXFormatsSQLWhenTheConfigCantBeRead(t *testing.T) {
	r := &recorder{}
	out, err := SQLX(context.Background(), []byte("config {type:\"view\";}\nselect 1\n"), opts(r.run))
	var pe *PartialError
	if !errors.As(err, &pe) || pe.Skipped != "the config block" {
		t.Fatalf("got %v, want a PartialError for the config block", err)
	}
	if want := "config {type:\"view\";}\nSELECT 1\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}

	// Neither: both errors, and no text.
	out, err = SQLX(context.Background(), []byte("config {type:\"view\";}\nselect 1\n"), Options{})
	if out != nil || !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "config block") {
		t.Errorf("got %q, %v", out, err)
	}
}
