package format

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// recorder is a Runner that stands in for sqlfluff: it upper-cases what it
// is given, as a capitalisation rule would, and notes it.
type recorder struct {
	mu    sync.Mutex
	calls []string
	args  [][]string
}

func (r *recorder) run(_ context.Context, _ string, args []string, _ string, stdin string) (string, string, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, stdin)
	r.args = append(r.args, args)
	r.mu.Unlock()
	return strings.ToUpper(stdin) + "\n", "", 0, nil
}

func opts(r Runner) Options { return Options{Bin: "sqlfluff", Run: r, Config: "cfg"} }

const sqlx = `config {
  type: "table",
  name: "orders"
}

js {
  const cols = ["a", "b"];
}

pre_operations {
  select 1 from ${ref("x")}
}

-- a comment ${not_an_island}
select ${cols[0]}, b from ${ref("orders")}   where b = ${ctx.name}
`

func TestSQLXFormatsEachSQLPartInPlace(t *testing.T) {
	r := &recorder{}
	out, err := SQLX(context.Background(), []byte(sqlx), opts(r.run))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(sqlx, "select 1 from ${ref(\"x\")}", "SELECT 1 FROM ${ref(\"x\")}", 1)
	want = strings.Replace(want, "-- a comment ${not_an_island}\nselect ${cols[0]}, b from ${ref(\"orders\")}   where b = ${ctx.name}",
		"-- A COMMENT ${NOT_AN_ISLAND}\nSELECT ${cols[0]}, B FROM ${ref(\"orders\")}   WHERE B = ${ctx.name}", 1)
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if len(r.calls) != 2 {
		t.Errorf("sqlfluff ran %d times, want 2 (the pre_operations body and the main query): %q", len(r.calls), r.calls)
	}
}

func TestSQLFluffNeverSeesASubstitution(t *testing.T) {
	r := &recorder{}
	if _, err := SQLX(context.Background(), []byte(sqlx), opts(r.run)); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		// A ${ in a SQL comment is not a substitution and is left as it is.
		for _, l := range strings.Split(c, "\n") {
			if !strings.HasPrefix(l, "--") && strings.Contains(l, "${") {
				t.Errorf("sqlfluff was given a ${: %q", l)
			}
		}
	}
}

func TestHiddenNameIsAsLongAsTheSubstitution(t *testing.T) {
	text := `select ${ref("orders")} as a from t`
	h := hide(text, [][2]int{{7, 23}}, false)
	if len(h.islands[0].name) != len(`${ref("orders")}`) {
		t.Errorf("name %q is %d long, want %d", h.islands[0].name, len(h.islands[0].name), len(`${ref("orders")}`))
	}
	short := hide(`a ${x} b`, [][2]int{{2, 6}}, false)
	if got := short.islands[0].name; len(got) != 5 { // _xf0_ is the least a name takes
		t.Errorf("short name %q", got)
	}
	back, ok := h.restore(strings.ToUpper(h.text))
	if !ok || back != strings.ToUpper(text[:7])+text[7:23]+strings.ToUpper(text[23:]) {
		t.Errorf("restore = %q, %v", back, ok)
	}
}

func TestRestoreFindsLongNamesBeforeTheirPrefixes(t *testing.T) {
	var spans [][2]int
	var b strings.Builder
	for i := 0; i < 12; i++ {
		start := b.Len()
		b.WriteString("${a}")
		spans = append(spans, [2]int{start, b.Len()})
		b.WriteString(" ")
	}
	h := hide(b.String(), spans, false)
	back, ok := h.restore(h.text)
	if !ok || back != b.String() {
		t.Errorf("restore = %q, %v", back, ok)
	}
}

func TestRestoreRefusesALostSubstitution(t *testing.T) {
	h := hide("select ${ref('a')}", [][2]int{{7, 18}}, false)
	if _, ok := h.restore("select 1"); ok {
		t.Error("a missing substitution was accepted")
	}
	if _, ok := h.restore(h.text + " " + h.text); ok {
		t.Error("a doubled substitution was accepted")
	}
}

func TestCRLFFileKeepsItsLineEndings(t *testing.T) {
	r := &recorder{}
	src := "config {\r\n  type: \"view\"\r\n}\r\n\r\nselect a,\r\n b from t\r\n"
	out, err := SQLX(context.Background(), []byte(src), opts(r.run))
	if err != nil {
		t.Fatal(err)
	}
	want := "config {\r\n  type: \"view\"\r\n}\r\n\r\nSELECT A,\r\n B FROM T\r\n"
	if string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "\r") {
			t.Errorf("sqlfluff was given a CR: %q", c)
		}
	}
}

func TestWhitespaceBetweenBlocksIsKept(t *testing.T) {
	r := &recorder{}
	src := "config {}\n\n\n\n  \nselect 1\n\n\n"
	out, err := SQLX(context.Background(), []byte(src), opts(r.run))
	if err != nil {
		t.Fatal(err)
	}
	if want := "config {}\n\n\n\n  \nSELECT 1\n\n\n"; string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
	if len(r.calls) != 1 {
		t.Errorf("sqlfluff ran %d times, want 1", len(r.calls))
	}
}

func TestPreOperationsKeepTheirIndent(t *testing.T) {
	run := func(_ context.Context, _ string, _ []string, _ string, stdin string) (string, string, int, error) {
		return "select\n  a,\n  '''x\n  y'''\nfrom t\n", "", 0, nil
	}
	src := "pre_operations {\n  select a, '''x\n  y''' from t\n}\nselect 1"
	out, err := SQLX(context.Background(), []byte(src), opts(run))
	if err != nil {
		t.Fatal(err)
	}
	want := "pre_operations {\n  select\n    a,\n    '''x\n  y'''\n  from t\n}\nselect\n  a,\n  '''x\n  y'''\nfrom t"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestParseErrorIsPlacedInTheFile(t *testing.T) {
	run := func(_ context.Context, _ string, args []string, _ string, stdin string) (string, string, int, error) {
		if args[0] == "format" {
			return stdin, "  [1 templating/parsing errors found]\n", 1, nil
		}
		// sqlfluff saw the query after the 2-line island became one line:
		// the error is on its second line, at the character of "))".
		return `[{"filepath":"stdin","violations":[{"code":"PRS","description":"Line 2, Position 7: Found unparsable section: '))'","start_line_no":2,"start_line_pos":7}]}]`, "", 1, nil
	}
	src := "config {}\nselect ${ref(\n  \"a\")} from t\nwhere ))"
	_, err := SQLX(context.Background(), []byte(src), opts(run))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a ParseError", err)
	}
	// "select _xf0_ from t" is line 1 of the part and "where ))" its second:
	// the part starts on line 2 of the file, so the error is on line 4.
	if pe.Line != 4 || pe.Col != 7 || pe.Msg != "Found unparsable section: '))'" {
		t.Errorf("got %+v", pe)
	}
}

func TestMissingSQLFluff(t *testing.T) {
	run := func(context.Context, string, []string, string, string) (string, string, int, error) {
		return "", "", 0, exec.ErrNotFound
	}
	if _, err := SQLX(context.Background(), []byte("select 1"), opts(run)); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestSQLFluffFailingIsReported(t *testing.T) {
	run := func(context.Context, string, []string, string, string) (string, string, int, error) {
		return "", "Error: Invalid value for '--config'\n", 2, nil
	}
	_, err := SQLX(context.Background(), []byte("select 1"), opts(run))
	if err == nil || !strings.Contains(err.Error(), "Invalid value for '--config'") {
		t.Errorf("err = %v", err)
	}
}

func TestAnEmptyAnswerDoesNotEmptyThePart(t *testing.T) {
	run := func(context.Context, string, []string, string, string) (string, string, int, error) {
		return "\n", "", 0, nil
	}
	if _, err := SQLX(context.Background(), []byte("select 1"), opts(run)); err == nil {
		t.Error("an empty answer was taken for the formatted SQL")
	}
}

func TestUnclosedBlockIsRefused(t *testing.T) {
	r := &recorder{}
	for _, src := range []string{"config {\n select 1", "select ${ref(\"a\""} {
		if _, err := SQLX(context.Background(), []byte(src), opts(r.run)); err == nil {
			t.Errorf("%q was formatted", src)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("sqlfluff ran for an unclosed block")
	}
}

func TestQueryFormatsTheWholeText(t *testing.T) {
	r := &recorder{}
	out, err := Query(context.Background(), []byte("\nselect a from t\n"), opts(r.run))
	if err != nil || string(out) != "\nSELECT A FROM T\n" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestCommandLine(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{}
	o := Options{Bin: "sqlfluff", Run: r.run, Dir: dir, Root: dir}
	if _, err := Query(context.Background(), []byte("select 1"), o); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(r.args[0], " ")
	for _, want := range []string{"format -", "--dialect bigquery", "--templater raw", "--ignore-local-config --config "} {
		if !strings.Contains(args, want) {
			t.Errorf("no project config: %q lacks %q", args, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".sqlfluff"), []byte("[sqlfluff]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := &recorder{}
	o.Run = r2.run
	if _, err := Query(context.Background(), []byte("select 1"), o); err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(r2.args[0], " "); strings.Contains(args, "--config") || !strings.Contains(args, "--templater raw") {
		t.Errorf("project config: %q", args)
	}

	// A config named is used in place of the project's own, not laid over it.
	r3 := &recorder{}
	o.Run, o.Config = r3.run, "/elsewhere/.sqlfluff"
	if _, err := Query(context.Background(), []byte("select 1"), o); err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(r3.args[0], " "); !strings.Contains(args, "--ignore-local-config --config /elsewhere/.sqlfluff") {
		t.Errorf("config named: %q", args)
	}
}

func TestProjectConfigIsLookedForUpToTheRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "definitions", "staging")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if hasProjectConfig(sub, root) {
		t.Error("a config was found in an empty project")
	}
	for name, content := range map[string]string{"tox.ini": "[flake8]\n", "pyproject.toml": "[tool.black]\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if hasProjectConfig(sub, root) {
		t.Error("a tox.ini without [sqlfluff] counted")
	}
	if err := os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte("[tool.sqlfluff]\ndialect='bigquery'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasProjectConfig(sub, root) {
		t.Error("[tool.sqlfluff] in the root's pyproject.toml was missed")
	}
}

func TestFindBin(t *testing.T) {
	root := t.TempDir()
	look := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	// Absolute on every OS: "/opt/sqlfluff" has no drive, so Windows takes it
	// as relative.
	abs := filepath.Join(t.TempDir(), "opt", "sqlfluff")
	if got, _ := FindBin(abs, root, look); got != abs {
		t.Errorf("configured: %q", got)
	}
	if got, _ := FindBin("tools/sqlfluff", root, look); got != filepath.Join(root, "tools", "sqlfluff") {
		t.Errorf("relative: %q", got)
	}
	if got, _ := FindBin("", root, look); got != "/usr/bin/sqlfluff" {
		t.Errorf("PATH: %q", got)
	}
	if _, err := FindBin("", root, none); !errors.Is(err, ErrNotFound) {
		t.Errorf("none: %v", err)
	}
	sub, name := "bin", "sqlfluff"
	if runtime.GOOS == "windows" {
		sub, name = "Scripts", "sqlfluff.exe"
	}
	bin := filepath.Join(root, ".venv", sub)
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindBin("", root, look); got != filepath.Join(bin, name) {
		t.Errorf("venv: %q", got)
	}
}

func TestStartsInside(t *testing.T) {
	cases := map[string][]bool{
		"a\nb":              {false, false},
		"'''x\ny'''\nz":     {false, true, false},
		"'x\ny'\nz":         {false, false, false}, // a quoted string ends at its line
		"/* a\nb */\nc":     {false, true, false},
		"'''a\nb'''\nc":     {false, true, false},
		"-- it's\nx":        {false, false},
		"select 'it''s'\nx": {false, false},
		"`a\nb`\nc":         {false, true, false},
		"x = 'a\\'b\nc":     {false, false},
	}
	for in, want := range cases {
		got := startsInside(in)
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", in, got, want)
				break
			}
		}
	}
}

// TestRealSQLFluff runs the sqlfluff on PATH, when there is one, over a
// file with the awkward ${ }s of a real project, with the built-in config.
func TestRealSQLFluff(t *testing.T) {
	bin, err := exec.LookPath("sqlfluff")
	if err != nil {
		t.Skip("sqlfluff isn't installed")
	}
	dir := t.TempDir()
	o := Options{Bin: bin, Dir: dir, Root: dir}
	src := "config {\n  type: \"table\"\n}\n\nselect  ${when(incremental(), \"ts\", \"0\")} as z, a.id,count(*) c  from ${self()} a left join `${ref(\"a\")}` b on a.id=b.id and a.k=${ctx.name} where ts > (select max(ts) from ${self()}) -- ${x}\ngroup by 1,2\n"
	out, err := SQLX(context.Background(), []byte(src), o)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	t.Log("\n" + got)
	for _, want := range []string{"config {\n  type: \"table\"\n}", "SELECT", "${when(incremental(), \"ts\", \"0\")}", "FROM ${self()}", "`${ref(\"a\")}`", "= ${ctx.name}", "-- ${x}", "GROUP BY 1, 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	again, err := SQLX(context.Background(), out, o)
	if err != nil || string(again) != got {
		t.Errorf("a second format changed it (%v):\n%s\n---\n%s", err, got, again)
	}

	_, err = SQLX(context.Background(), []byte("select a from (select b from\nwhere ))\n"), o)
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Line < 1 {
		t.Errorf("a broken query gave %v", err)
	}
}

func TestAClauseOnALineOfItsOwnIsHiddenAsAComment(t *testing.T) {
	var calls []string
	run := func(_ context.Context, _ string, args []string, _ string, stdin string) (string, string, int, error) {
		calls = append(calls, stdin)
		// sqlfluff can't read a bare name where a clause goes.
		if strings.Contains(stdin, "\n  _xf") {
			if args[0] == "lint" {
				return `[{"violations":[{"code":"PRS","description":"Line 3, Position 1: Found unparsable section: '_xf0____...'","start_line_no":3,"start_line_pos":1}]}]`, "", 1, nil
			}
			return stdin, "[1 templating/parsing errors found]\n", 1, nil
		}
		return strings.ToUpper(stdin) + "\n", "", 0, nil
	}
	src := "select a from t\n  ${when(incremental(), `where x`)}\ngroup by 1"
	out, err := SQLX(context.Background(), []byte(src), opts(run))
	if err != nil {
		t.Fatal(err)
	}
	if want := "SELECT A FROM T\n  ${when(incremental(), `where x`)}\nGROUP BY 1"; string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
	if len(calls) != 2 || !strings.Contains(calls[1], "\n  -- _xf0_\n") {
		t.Errorf("calls = %q", calls)
	}
}

func TestParseErrorNamesTheSubstitutionsPlainly(t *testing.T) {
	run := func(_ context.Context, _ string, args []string, _ string, stdin string) (string, string, int, error) {
		if args[0] == "format" {
			return stdin, "[1 templating/parsing errors found]\n", 1, nil
		}
		return `[{"violations":[{"code":"PRS","description":"Line 1, Position 8: Found unparsable section: '_xf0_____...'","start_line_no":1,"start_line_pos":8}]}]`, "", 1, nil
	}
	_, err := SQLX(context.Background(), []byte("select ${ref(\"a\")} ))"), opts(run))
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Msg != "Found unparsable section: '${…}'" || pe.Col != 8 {
		t.Errorf("err = %v", err)
	}
}
