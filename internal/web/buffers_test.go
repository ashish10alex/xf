package web

import (
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

func TestBuffersKept(t *testing.T) {
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	list := []buffer{{N: 1, File: "definitions/a.sqlx", Action: "p.mart.orders"}, {N: 3, File: "includes/u.js"}}
	postJSON(t, c, ts.URL+"/api/session", resumeView{Selected: "p.mart.orders"}, nil)
	postJSON(t, c, ts.URL+"/api/buffers", buffersView{List: list, Next: 4}, nil)
	postJSON(t, c, ts.URL+"/api/session", resumeView{Selected: "p.mart.orders"}, nil) // keeps the buffers
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &b)
	got := b.Project.Resume.Buffers
	if got == nil || got.Next != 4 || !slices.Equal(got.List, list) {
		t.Errorf("buffers = %+v, want %v next 4", got, list)
	}
}

func TestBufferFiles(t *testing.T) {
	_, ts, c := newSourceTest(t, map[string]string{
		"definitions/orders.sqlx": "config { type: \"table\" }\nSELECT 1",
		"includes/u.js":           "module.exports = {}",
	})
	postJSON(t, c, ts.URL+"/api/compile", nil, nil)
	var v struct{ Files []bufferFile }
	postJSON(t, c, ts.URL+"/api/buffers/files", map[string]any{"files": []string{"definitions/orders.sqlx", "includes/u.js", "definitions/gone.sqlx", "../outside.sqlx"}}, &v)
	ids := func(f bufferFile) []project.ActionID {
		var out []project.ActionID
		for _, a := range f.Actions {
			out = append(out, a.ID)
		}
		return out
	}
	if len(v.Files) != 4 {
		t.Fatalf("files = %+v", v.Files)
	}
	if f := v.Files[0]; f.Gone || !slices.Equal(ids(f), []project.ActionID{"p.mart.orders", "p.mart.orders_assertions_uniqueKey_0"}) || f.Actions[0].Name != "orders" {
		t.Errorf("orders.sqlx = %+v", f)
	}
	if f := v.Files[1]; f.Gone || len(f.Actions) != 0 {
		t.Errorf("u.js = %+v, want there with no actions", f)
	}
	if !v.Files[2].Gone || !v.Files[3].Gone {
		t.Errorf("a missing file and one outside the project = %+v, %+v, want gone", v.Files[2], v.Files[3])
	}
}
