package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/session"
)

// Every tab hears of the open projects as they open, close and change,
// the projects list's page too.
func TestProjectSummaries(t *testing.T) {
	opened := func(root string) *Project {
		return &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: root}}
	}
	s, err := New(Config{Token: "secret", Project: opened("/work/proj"),
		Projects: func() []session.Project { return []session.Project{{Root: "/work/proj"}, {Root: "/work/other"}} },
		Open:     func(p session.Project) (*Project, error) { return opened(p.Root), nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()

	list := openEvents(t, c, ts.URL, "")
	postJSON(t, c, ts.URL+"/api/pick", map[string]string{"root": "/work/other"}, nil)
	list.until(t, "projects", `"key":"other"`)

	postJSON(t, c, ts.URL+"/api/unsaved", map[string]any{"tab": "a", "unsaved": map[string][]string{"/work/other": {"x.sqlx"}}}, nil)
	list.until(t, "projects", `"unsaved":["x.sqlx"]`)
	var sums []projectSummary
	getJSON(t, c, ts.URL+"/api/projects", &sums)
	if len(sums) != 2 {
		t.Fatalf("summaries %+v, want proj and other", sums)
	}

	postJSON(t, c, ts.URL+"/api/close", map[string]string{"key": "other"}, nil)
	if ev := list.until(t, "projects", `"key":"proj"`); strings.Contains(ev, `"other"`) {
		t.Errorf("after closing other: %s", ev)
	}
}
