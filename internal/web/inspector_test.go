package web

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/session"
)

func TestInspectorSQL(t *testing.T) {
	if got := inspectorSQL("p.d.t", "  "); got != "SELECT * FROM `p.d.t`" {
		t.Errorf("no filter: %q", got)
	}
	if got := inspectorSQL("p.d.t", "id = 1"); got != "SELECT * FROM `p.d.t` WHERE id = 1" {
		t.Errorf("filter: %q", got)
	}
}

// The view walks upstream, leaving the assertion out; the page's state
// is kept, and a row's run keeps its job when the page saves again.
func TestInspectorViewAndState(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)

	var v inspectorView
	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
	if v.Error != "" || v.Depth != defaultInspectorDepth || v.Key != "mart.orders" {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Nodes) != 2 || v.Nodes[1].ID != "p.raw.orders" || v.Nodes[1].Read != "p.raw.orders" || !v.Nodes[1].Table {
		t.Errorf("nodes = %+v", v.Nodes)
	}
	if len(v.Edges) != 1 || v.State != nil {
		t.Errorf("edges %+v, state %+v", v.Edges, v.State)
	}

	state := session.InspectorState{Depth: 5, Global: "id = 1", Rows: map[string]session.InspectorRow{"raw.orders": {Filter: "x > 0"}}}
	postJSON(t, c, ts.URL+"/api/inspector/state", map[string]any{"key": v.Key, "state": state}, nil)
	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
	if v.State == nil || v.State.Global != "id = 1" || v.Depth != 5 || v.State.Rows["raw.orders"].Filter != "x > 0" {
		t.Errorf("state not kept: depth %d, %+v", v.Depth, v.State)
	}

	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.none", &v)
	if v.Error == "" {
		t.Error("an unknown model has no error")
	}
}

// A table added by hand follows the walk at AddedDepth, survives the page
// saving its state, and is refused when it has no table; one the walk
// already lists isn't listed twice.
func TestInspectorAdded(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var ok okView
	postJSON(t, c, ts.URL+"/api/inspector/added", map[string]any{"key": "mart.orders", "add": "p.mart.customers"}, &ok)
	postJSON(t, c, ts.URL+"/api/inspector/added", map[string]any{"key": "mart.orders", "added": []string{"mart.customers"}, "add": "p.raw.orders"}, &ok)
	var bad errorView
	postJSON(t, c, ts.URL+"/api/inspector/added", map[string]any{"key": "mart.orders", "add": "p.mart.orders_assertions_uniqueKey_0"}, &bad)
	if !strings.Contains(bad.Error, "lists tables") {
		t.Errorf("an assertion was added: %+v", bad)
	}
	postJSON(t, c, ts.URL+"/api/inspector/state", map[string]any{"key": "mart.orders", "state": session.InspectorState{Global: "x"}}, nil)

	var v inspectorView
	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
	var ids []string
	for _, n := range v.Nodes {
		ids = append(ids, string(n.ID)+":"+strconv.Itoa(n.Depth))
	}
	if strings.Join(ids, " ") != "p.mart.orders:0 p.raw.orders:1 p.mart.customers:-1" {
		t.Errorf("nodes = %v", ids)
	}
	if v.State == nil || strings.Join(v.State.Added, " ") != "mart.customers raw.orders" {
		t.Errorf("added = %+v", v.State)
	}

	postJSON(t, c, ts.URL+"/api/inspector/added", map[string]any{"key": "mart.orders", "added": []string{"mart.gone"}}, nil)
	v = inspectorView{}
	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
	if len(v.Nodes) != 2 || strings.Join(v.Missing, " ") != "mart.gone" {
		t.Errorf("nodes %d, missing %v", len(v.Nodes), v.Missing)
	}
}

// A run is queued, run in BigQuery, counted and kept with the session.
func TestInspectorRun(t *testing.T) {
	bq, calls := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	body := map[string]any{"key": "mart.orders", "rows": []map[string]string{{"key": "raw.orders", "table": "p.raw.orders", "filter": "id = 7"}}}
	postJSON(t, c, ts.URL+"/api/inspector/run", body, nil)
	var v inspectorView
	deadline := time.Now().Add(5 * time.Second)
	for {
		v = inspectorView{}
		getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
		if r := v.Runs["raw.orders"]; r != nil && r.Phase != "queued" && r.Phase != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never ended: %+v", v.Runs["raw.orders"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	r := v.Runs["raw.orders"]
	if r.Phase != "done" || r.Rows != 2 || r.Job == nil || r.SQL != "SELECT * FROM `p.raw.orders` WHERE id = 7" {
		t.Errorf("run = %+v", r)
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "WHERE id = 7") {
		t.Errorf("the query never reached BigQuery: %v", *calls)
	}
	if v.State == nil || v.State.Rows["raw.orders"].Job == nil {
		t.Errorf("the job isn't kept with the session: %+v", v.State)
	}
	// Saving the page's state keeps the server's job.
	postJSON(t, c, ts.URL+"/api/inspector/state", map[string]any{"key": "mart.orders", "state": session.InspectorState{Rows: map[string]session.InspectorRow{"raw.orders": {Filter: "id = 8"}}}}, nil)
	getJSON(t, c, ts.URL+"/api/inspector?model=p.mart.orders", &v)
	if j := v.State.Rows["raw.orders"].Job; j == nil || j.Rows != 2 {
		t.Errorf("job after a save = %+v", j)
	}
}

// With defer to prod on, a row not built in dev reads prod's table and
// says so, once the Deferral of the node that reads it is worked out; one
// built in dev reads dev.
func TestInspectorDefer(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	fd := &fakeDeferral{}
	_, ts, c := newScratchTest(t, frontend.Deps{Engine: deferEngine(deferGraph(), true), Deferral: fd.ask}, bq)
	var v inspectorView
	deadline := time.Now().Add(5 * time.Second)
	for {
		v = inspectorView{} // a false deferLoading isn't sent
		getJSON(t, c, ts.URL+"/api/inspector?model=dev.s.orders", &v)
		if v.Defer && !v.DeferLoading {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Deferral never arrived: %+v", v)
		}
		time.Sleep(30 * time.Millisecond)
	}
	read := map[string]inspectorNode{}
	for _, n := range v.Nodes {
		read[n.Name] = n
	}
	if n := read["customers"]; n.Read != "prod.s.customers" || n.Label != "→ prod" {
		t.Errorf("customers = read %s, label %q", n.Read, n.Label)
	}
	if n := read["payments"]; n.Read != "dev.s.payments" || !strings.HasPrefix(n.Label, "✓ dev") {
		t.Errorf("payments = read %s, label %q", n.Read, n.Label)
	}
	if n := read["orders"]; n.Read != "dev.s.orders" || n.Label != "" {
		t.Errorf("the model = read %s, label %q", n.Read, n.Label)
	}
}

func TestInspectorDryRun(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var d scratchDryView
	postJSON(t, c, ts.URL+"/api/inspector/dryrun", map[string]string{"table": "p.raw.orders", "filter": "id = 1"}, &d)
	if !d.OK || d.Bytes != 1000 || d.Cost == "" {
		t.Errorf("dry run = %+v", d)
	}
}

// Presets go to the git root's .xf/inspector, which ignores itself, by
// the project's folder there; a save over one keeps its id and creation.
func TestInspectorPresets(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, "examples", "df")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, ts, c := newScratchTest(t, frontend.Deps{Root: root}, bq)
	if path, folder := presetsOf(root); path != filepath.Join(top, ".xf", "inspector", "filters.json") || folder != "examples/df" {
		t.Fatalf("presetsOf = %s, %s", path, folder)
	}

	var saved inspectorPreset
	postJSON(t, c, ts.URL+"/api/inspector/presets", map[string]any{"key": "mart.orders", "preset": inspectorPreset{Description: " qa ", Global: "id = 1",
		Rows: map[string]presetRow{"raw.orders": {On: true, Filter: "x"}}}}, &saved)
	if saved.ID == "" || saved.Description != "qa" {
		t.Fatalf("saved = %+v", saved)
	}
	again := saved
	again.Global = "id = 2"
	postJSON(t, c, ts.URL+"/api/inspector/presets", map[string]any{"key": "mart.orders", "preset": again}, nil)
	var list presetsView
	getJSON(t, c, ts.URL+"/api/inspector/presets?key=mart.orders", &list)
	if len(list.Presets) != 1 || list.Presets[0].Global != "id = 2" || !list.Presets[0].Created.Equal(saved.Created) {
		t.Errorf("after a save over it: %+v", list)
	}
	if data, err := os.ReadFile(filepath.Join(top, ".xf", "inspector", ".gitignore")); err != nil || !strings.Contains(string(data), "*") {
		t.Errorf(".gitignore = %q, %v", data, err)
	}

	postJSON(t, c, ts.URL+"/api/inspector/presets/delete", map[string]string{"key": "mart.orders", "id": saved.ID}, &list)
	if len(list.Presets) != 0 {
		t.Errorf("after delete: %+v", list.Presets)
	}
}

func TestTrimInspected(t *testing.T) {
	m := map[string]session.InspectorState{}
	at := time.Unix(1e9, 0)
	for i := range session.MaxInspected + 3 {
		m[string(rune('a'+i))] = session.InspectorState{Used: at.Add(time.Duration(i) * time.Minute)}
	}
	session.TrimInspected(m)
	if _, ok := m["a"]; ok || len(m) != session.MaxInspected {
		t.Errorf("kept %d, the oldest too: %v", len(m), ok)
	}
}
