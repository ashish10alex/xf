package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
)

// A page's notes are appended to the page log, a line each with its time,
// tab and project, and the answer says where the log is.
func TestPageLog(t *testing.T) {
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	path := filepath.Join(t.TempDir(), "state", "web-page.log")
	s.pageLog.path = path
	at := time.Date(2026, 10, 23, 12, 20, 5, 123e6, time.Local).UnixMilli()
	var got struct{ Path string }
	for range 2 {
		postJSON(t, c, ts.URL+"/api/pagelog", map[string]any{"tab": "xf-tab-abcdefgh", "project": "dataform", "lines": []map[string]any{
			{"t": at, "text": "key j → used"},
			{"t": at, "text": "draw failed: TypeError\nat CostReport"},
		}}, &got)
	}
	if got.Path != path {
		t.Errorf("answered path %q, want %q", got.Path, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-23 12:20:05.123 abcdef dataform key j → used\n" +
		"2026-10-23 12:20:05.123 abcdef dataform draw failed: TypeError\n    at CostReport\n"
	if string(b) != want+want {
		t.Errorf("log is\n%s\nwant twice\n%s", b, want)
	}
}

// A log grown past its size starts again, the old one kept as .1.
func TestPageLogStartsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-page.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", pageLogMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	l := pageLog{path: path}
	if err := l.write(pageLogBody{Lines: []pageLogLine{{T: 0, Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(b), " - - hello\n") || len(b) > 100 {
		t.Errorf("log is %q, want it started again", b)
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != pageLogMax+1 {
		t.Errorf("old log not kept as .1: %v", err)
	}
}

// With no log file, notes are taken and dropped.
func TestPageLogNone(t *testing.T) {
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	postJSON(t, c, ts.URL+"/api/pagelog", map[string]any{"tab": "a", "lines": []map[string]any{{"t": 1, "text": "x"}}}, nil)
}
