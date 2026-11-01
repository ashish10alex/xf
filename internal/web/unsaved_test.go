package web

import (
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
)

// A tab's unsaved files reach the other tabs, not itself, while the tab
// is there and for a while after its streams go; then they are forgotten.
func TestUnsavedFiles(t *testing.T) {
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	var got struct{ Files []string }
	postJSON(t, c, ts.URL+"/api/unsaved", map[string]any{"tab": "a", "unsaved": map[string][]string{
		"/work/proj": {"definitions/orders.sqlx"}, "/work/other": {"x.sqlx"},
	}}, nil)
	getJSON(t, c, ts.URL+"/api/unsaved?tab=b", &got)
	if strings.Join(got.Files, ",") != "definitions/orders.sqlx" {
		t.Errorf("another tab sees %v, want orders.sqlx", got.Files)
	}
	getJSON(t, c, ts.URL+"/api/unsaved?tab=a", &got)
	if len(got.Files) != 0 {
		t.Errorf("the tab itself sees %v, want none", got.Files)
	}

	// Its stream gone a while ago, the tab is forgotten.
	page := streamEvents(t, c, ts.URL, "/api/events?tab=a")
	streams := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.tabs["a"].streams
	}
	waitFor(t, func() bool { return streams() == 1 })
	page.close()
	waitFor(t, func() bool { return streams() == 0 })
	getJSON(t, c, ts.URL+"/api/unsaved?tab=b", &got)
	if len(got.Files) != 1 {
		t.Errorf("a tab just gone: %v, want its file still", got.Files)
	}
	s.mu.Lock()
	s.tabs["a"].gone = time.Now().Add(-2 * tabGrace)
	s.mu.Unlock()
	getJSON(t, c, ts.URL+"/api/unsaved?tab=b", &got)
	if len(got.Files) != 0 {
		t.Errorf("a gone tab's files: %v, want none", got.Files)
	}
}
