package dbt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
)

// stateManifestJSON builds a whole manifest from nodes, each a unique ID
// and the node's fields; unit tests go in unit_tests.
func stateManifestJSON(t *testing.T, nodes map[string]map[string]any) []byte {
	t.Helper()
	m := map[string]map[string]any{"nodes": {}, "unit_tests": {}, "sources": {}}
	for id, n := range nodes {
		n["unique_id"] = id
		n["name"] = id[strings.LastIndexByte(id, '.')+1:]
		n["database"], n["schema"] = "proj", "ds"
		switch n["resource_type"] {
		case "unit_test":
			m["unit_tests"][id] = n
		case "source":
			m["sources"][id] = n
		default:
			m["nodes"][id] = n
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func model(file, checksum string, config map[string]any) map[string]any {
	return map[string]any{
		"resource_type": "model", "original_file_path": file, "fqn": []string{"p", "m"},
		"checksum": map[string]any{"name": "sha256", "checksum": checksum}, "config": config,
	}
}

func TestDiffManifests(t *testing.T) {
	table := map[string]any{"materialized": "table", "tags": []string{"a"}}
	base := stateManifestJSON(t, map[string]map[string]any{
		"model.p.orders":    model("models/orders.sql", "1", table),
		"model.p.customers": model("models/customers.sql", "2", table),
		"model.p.payments":  model("models/payments.sql", "3", table),
		"model.p.daily":     model("models/daily.sql", "4", table),
		"model.p.gone":      model("models/gone.sql", "5", table),
		"model.p.eph_gone":  model("models/eph_gone.sql", "6", map[string]any{"materialized": "ephemeral"}),
	})
	head := stateManifestJSON(t, map[string]map[string]any{
		"model.p.orders":    model("models/orders.sql", "1b", table),
		"model.p.customers": model("models/customers.sql", "2", map[string]any{"tags": []string{"a"}, "materialized": "view"}),
		// Same config as the base, keys in another order: unchanged.
		"model.p.payments": model("models/payments.sql", "3", map[string]any{"tags": []string{"a"}, "materialized": "table"}),
		"model.p.daily":    model("models/daily.sql", "4", table),
		"model.p.new":      model("models/new.sql", "7", table),
		"model.p.eph":      model("models/eph.sql", "8", map[string]any{"materialized": "ephemeral"}),
		"source.p.raw.t":   {"resource_type": "source", "original_file_path": "models/sources.yml", "config": map[string]any{}},
		"unit_test.p.orders.sums": {"resource_type": "unit_test", "original_file_path": "models/orders.yml", "fqn": []string{"p", "orders", "sums"},
			"checksum": "u1", "config": map[string]any{}},
	})
	modified := []string{"model.p.orders", "model.p.customers", "model.p.payments", "model.p.new", "model.p.eph",
		"source.p.raw.t", "unit_test.p.orders.sums", "model.p.orders"}
	d, err := diffManifests(base, head, modified)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range d.Changed {
		got = append(got, c.FileName+" "+string(c.ID)+" "+c.Type+" "+c.ReasonsText())
	}
	want := []string{
		"models/customers.sql proj.ds.customers view config",
		"models/new.sql proj.ds.new table new",
		"models/orders.sql proj.ds.orders table sql",
		"models/orders.yml unit_test.sums test new",
		// dbt listed it, with the same file and config: a macro it calls.
		"models/payments.sql proj.ds.payments table upstream",
	}
	if !slices.Equal(got, want) {
		t.Errorf("changed:\n got %q\nwant %q", got, want)
	}
	var deleted []string
	for _, del := range d.Deleted {
		deleted = append(deleted, string(del.ID)+" "+del.Type)
	}
	if want := []string{"proj.ds.gone table"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted = %q, want %q", deleted, want)
	}
}

func TestPrintedIDs(t *testing.T) {
	out := strings.Join([]string{
		`{"data": {"log_version": 3}, "info": {"level": "info", "msg": "Running with dbt=1.12.5", "name": "MainReportVersion"}}`,
		`{"data": {"msg": "{\"unique_id\": \"model.p.a\"}"}, "info": {"code": "Z052", "level": "info", "msg": "{\"unique_id\": \"model.p.a\"}", "name": "PrintEvent"}}`,
		`{"data":{"msg":"{\"unique_id\":\"test.p.b.1234\"}"},"info":{"code":"Z052","level":"info","msg":"{\"unique_id\":\"test.p.b.1234\"}","name":"PrintEvent"}}`,
		`{"data":{"msg":"dbt 2.0.6"},"info":{"level":"info","msg":"dbt 2.0.6","name":"Generic"}}`,
		`not json`,
	}, "\n")
	if got, want := printedIDs([]byte(out)), []string{"model.p.a", "test.p.b.1234"}; !slices.Equal(got, want) {
		t.Errorf("printedIDs = %q, want %q", got, want)
	}
}

// Bases are written once, by content, and only the latest few kept.
func TestWriteState(t *testing.T) {
	parent := t.TempDir()
	first, err := writeState(parent, []byte(`{"n": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	again, err := writeState(parent, []byte(`{"n": 1}`))
	if err != nil || again != first {
		t.Fatalf("same base: %q, %v; want %q", again, err, first)
	}
	if data, _ := os.ReadFile(filepath.Join(first, "manifest.json")); string(data) != `{"n": 1}` {
		t.Errorf("manifest.json = %q", data)
	}
	for i := 2; i <= maxStates+2; i++ {
		if _, err := writeState(parent, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != maxStates {
		t.Errorf("%d states kept, want %d", len(entries), maxStates)
	}
}

// The reasons of a node dbt listed that isn't new.
func TestReasonsBoth(t *testing.T) {
	before := &fullManifest{nodes: map[string]node{"m": {}}, state: map[string]stateNode{"m": {Checksum: json.RawMessage(`"a"`), Config: json.RawMessage(`{"x":1}`)}}}
	after := &fullManifest{nodes: map[string]node{"m": {}}, state: map[string]stateNode{"m": {Checksum: json.RawMessage(`"b"`), Config: json.RawMessage(`{"x":2}`)}}}
	if got := reasons(before, after, "m"); !slices.Equal(got, []changes.Reason{changes.ReasonSQL, changes.ReasonConfig}) {
		t.Errorf("reasons = %v", got)
	}
}

// An ephemeral model is inlined into the models that ref it, so its change
// is theirs: dbt lists only the ephemeral one, which can't be built.
func TestDiffManifestsEphemeral(t *testing.T) {
	eph := map[string]any{"materialized": "ephemeral"}
	table := map[string]any{"materialized": "table"}
	withDeps := func(n map[string]any, deps ...string) map[string]any {
		n["depends_on"] = map[string]any{"nodes": deps}
		return n
	}
	nodes := func(ephSum string) map[string]map[string]any {
		return map[string]map[string]any{
			"model.p.eph":    model("models/eph.sql", ephSum, eph),
			"model.p.eph2":   withDeps(model("models/eph2.sql", "2", eph), "model.p.eph"),
			"model.p.direct": withDeps(model("models/direct.sql", "3", table), "model.p.eph"),
			"model.p.deep":   withDeps(model("models/deep.sql", "4", table), "model.p.eph2"),
			"model.p.other":  model("models/other.sql", "5", table),
			"test.p.t": withDeps(map[string]any{"resource_type": "test", "original_file_path": "tests/t.sql", "fqn": []string{"p", "t"},
				"checksum": map[string]any{"checksum": "6"}, "config": map[string]any{}}, "model.p.eph"),
		}
	}
	d, err := diffManifests(stateManifestJSON(t, nodes("1")), stateManifestJSON(t, nodes("1b")), []string{"model.p.eph"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range d.Changed {
		got = append(got, string(c.ID)+" "+c.ReasonsText())
	}
	if want := []string{"proj.ds.deep upstream", "proj.ds.direct upstream"}; !slices.Equal(got, want) {
		t.Errorf("changed = %q, want %q", got, want)
	}
}

func TestCompiledBase(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": plainManifest, "FAKE_DBT_COMPILE": plainManifest})
	data, err := b.CompiledBase(t.Context(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// What comes back is a graph's JSON: Parse reads it.
	if g, err := b.Parse(data); err != nil || len(g.Actions) == 0 {
		t.Fatalf("parsed %+v, %v", g, err)
	}
	// dbt v2 is parsed first, to see that compiling it runs no hooks.
	if got := calls(); !slices.Equal(got, []string{"parse", "compile"}) {
		t.Errorf("ran %v", got)
	}
}

func TestCompiledBaseRefusesASnapshotWithHooks(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": hookManifest, "FAKE_DBT_COMPILE": plainManifest})
	_, err := b.CompiledBase(t.Context(), t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "the snapshot has 1: set dbtCompileWithHooks") {
		t.Fatalf("got %v", err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ran %v, want only parse", got)
	}

	// With dbtCompileWithHooks, and for dbt-core, it compiles straight away.
	b, calls = fakeBackend(t, true, map[string]string{"FAKE_DBT_COMPILE": hookManifest})
	if _, err := b.CompiledBase(t.Context(), t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"compile"}) {
		t.Errorf("ran %v, want only compile", got)
	}
}

func TestCompiledBaseReportsACompileThatFails(t *testing.T) {
	b, _ := fakeBackend(t, true, map[string]string{"FAKE_DBT_COMPILE_EXIT": "1",
		"FAKE_DBT_LOG": `{"data":{},"info":{"level":"error","msg":"[error] boom"}}`})
	if _, err := b.CompiledBase(t.Context(), t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}
