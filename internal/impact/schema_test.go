package impact

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

func TestDiffSchemasBigQueryRepeatedRangeAndNestedStructs(t *testing.T) {
	prod := []warehouse.Column{
		{Name: "tags", Type: "STRING", Repeated: false},
		{Name: "window", Type: "RANGE", RangeElement: "DATE"},
		{Name: "id", Type: "INT64", Required: true},
		{Name: "user", Type: "RECORD", Fields: []warehouse.Column{
			{Name: "name", Type: "STRING"},
			{Name: "address", Type: "RECORD", Fields: []warehouse.Column{
				{Name: "city", Type: "STRING"},
				{Name: "zip", Type: "STRING"},
			}},
		}},
		{Name: "items", Type: "RECORD", Repeated: true, Fields: []warehouse.Column{
			{Name: "sku", Type: "STRING"},
			{Name: "price", Type: "INT64"},
		}},
	}
	dev := []warehouse.Column{
		{Name: "tags", Type: "STRING", Repeated: true},
		{Name: "window", Type: "RANGE", RangeElement: "TIMESTAMP"},
		// Required -> Nullable difference is ignored on dry runs.
		{Name: "id", Type: "INT64", Required: false},
		{Name: "user", Type: "STRUCT", Fields: []warehouse.Column{
			{Name: "name", Type: "STRING"},
			{Name: "address", Type: "STRUCT", Fields: []warehouse.Column{
				{Name: "city", Type: "STRING"},
			}},
		}},
		{Name: "items", Type: "RECORD", Repeated: true, Fields: []warehouse.Column{
			{Name: "sku", Type: "STRING"},
			{Name: "price", Type: "STRING"},
		}},
	}
	got := DiffSchemas(dev, prod)
	want := []ColumnImpact{
		{Column: "tags", Change: ColumnChange{Kind: TypeChanged, From: "STRING", To: "ARRAY<STRING>"}},
		{Column: "window", Change: ColumnChange{Kind: TypeChanged, From: "RANGE<DATE>", To: "RANGE<TIMESTAMP>"}},
		{Column: "user.address.zip", Change: ColumnChange{Kind: Dropped}},
		{Column: "items.price", Change: ColumnChange{Kind: TypeChanged, From: "INT64", To: "STRING"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffSchemas:\n got %+v\nwant %+v", got, want)
	}
}

func TestDiffSchemasDatabricksTypesAndWidening(t *testing.T) {
	// Synonyms (integer==int, long==bigint, decimal defaults, struct field order,
	// added struct fields, NOT NULL, COMMENT) must produce zero diffs.
	same := DiffSchemasFor(project.Databricks,
		[]warehouse.Column{
			f("a", "integer"),
			f("b", "long"),
			f("c", "boolean"),
			f("d", "float"),
			f("e", "decimal(10, 2)"),
			f("d0", "decimal(10)"),
			f("d1", "decimal"),
			f("s", "STRUCT<y: ARRAY<BIGINT>, x: INT, added_col: STRING>"),
			f("s2", "struct<a: int not null comment 'primary, key', b: string>"),
		},
		[]warehouse.Column{
			f("a", "int"),
			f("b", "bigint"),
			f("c", "bool"),
			f("d", "real"),
			f("e", "numeric(10,2)"),
			f("d0", "decimal(10,0)"),
			f("d1", "dec(10, 0)"),
			f("s", "struct<X:integer,y:array<long>>"),
			f("s2", "STRUCT<a: INT, b: STRING>"),
		},
	)
	if len(same) != 0 {
		t.Fatalf("Databricks synonyms/structs reported diffs: %+v", same)
	}

	// Dropping or retyping a field inside a Databricks STRUCT diffs per-field.
	structDiff := DiffSchemasFor(project.Databricks,
		[]warehouse.Column{
			f("user", "struct<id: bigint, city: string>"),
			f("items", "array<struct<sku: string>>"),
		},
		[]warehouse.Column{
			f("user", "STRUCT<id: INT, city: STRING, zip: STRING>"),
			f("items", "ARRAY<STRUCT<sku: STRING, price: DOUBLE>>"),
		},
	)
	wantStruct := []ColumnImpact{
		{Column: "user.id", Change: ColumnChange{Kind: TypeChanged, From: "INT", To: "BIGINT", Widening: true}},
		{Column: "user.zip", Change: ColumnChange{Kind: Dropped}},
		{Column: "items.price", Change: ColumnChange{Kind: Dropped}},
	}
	if !reflect.DeepEqual(structDiff, wantStruct) {
		t.Fatalf("Databricks struct diff:\n got %+v\nwant %+v", structDiff, wantStruct)
	}

	// Widening vs breaking retypes, and FLOAT stays FLOAT (not BigQuery's FLOAT64).
	got := DiffSchemasFor(project.Databricks,
		[]warehouse.Column{
			f("widen_int", "bigint"),
			f("widen_flt", "double"),
			f("widen_dec", "decimal(18,2)"),
			f("widen_str", "string"),
			f("narrow_int", "int"),
			f("narrow_flt", "float"),
			f("scale_dec", "decimal(18,4)"),
			f("int_to_flt", "double"),
		},
		[]warehouse.Column{
			f("widen_int", "int"),
			f("widen_flt", "float"),
			f("widen_dec", "decimal(10,2)"),
			f("widen_str", "varchar(50)"),
			f("narrow_int", "bigint"),
			f("narrow_flt", "double"),
			f("scale_dec", "decimal(18,2)"),
			f("int_to_flt", "bigint"),
		},
	)
	want := []ColumnImpact{
		{Column: "widen_int", Change: ColumnChange{Kind: TypeChanged, From: "INT", To: "BIGINT", Widening: true}},
		{Column: "widen_flt", Change: ColumnChange{Kind: TypeChanged, From: "FLOAT", To: "DOUBLE", Widening: true}},
		{Column: "widen_dec", Change: ColumnChange{Kind: TypeChanged, From: "DECIMAL(10,2)", To: "DECIMAL(18,2)", Widening: true}},
		{Column: "widen_str", Change: ColumnChange{Kind: TypeChanged, From: "VARCHAR(50)", To: "STRING", Widening: true}},
		{Column: "narrow_int", Change: ColumnChange{Kind: TypeChanged, From: "BIGINT", To: "INT"}},
		{Column: "narrow_flt", Change: ColumnChange{Kind: TypeChanged, From: "DOUBLE", To: "FLOAT"}},
		{Column: "scale_dec", Change: ColumnChange{Kind: TypeChanged, From: "DECIMAL(18,2)", To: "DECIMAL(18,4)"}},
		{Column: "int_to_flt", Change: ColumnChange{Kind: TypeChanged, From: "BIGINT", To: "DOUBLE"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Databricks diff:\n got %+v\nwant %+v", got, want)
	}
}

func TestDiffSchemasSnowflakeNormalization(t *testing.T) {
	// LIMIT 0 inferred VARCHAR(3)/NUMBER(1,0) vs DESCRIBE TABLE VARCHAR(16777216)/NUMBER(38,0)
	// must NOT produce false-positive type changes.
	same := DiffSchemasFor(project.Snowflake,
		[]warehouse.Column{
			f("status", "VARCHAR(3)"),
			f("flag", "NUMBER(1,0)"),
			f("active", "BOOLEAN"),
			f("rate", "FLOAT"),
			f("ts", "TIMESTAMP_NTZ(6)"),
			f("raw", "BINARY(16)"),
			f("arr", "ARRAY"),
			f("obj", "OBJECT"),
		},
		[]warehouse.Column{
			f("status", "VARCHAR(16777216)"),
			f("flag", "NUMBER(38,0)"),
			f("active", "BOOL"),
			f("rate", "DOUBLE PRECISION"),
			f("ts", "TIMESTAMP_NTZ(9)"),
			f("raw", "BINARY(8388608)"),
			f("arr", "ARRAY(NUMBER)"),
			f("obj", "OBJECT(a VARCHAR)"),
		},
	)
	if len(same) != 0 {
		t.Fatalf("Snowflake equivalent types reported diffs: %+v", same)
	}

	// Real changes (scale change, family change, timezone kind change) ARE reported
	// using Snowflake's own names (not BigQuery's BOOL/FLOAT64).
	got := DiffSchemasFor(project.Snowflake,
		[]warehouse.Column{
			f("amount", "NUMBER(10,2)"),
			f("ts", "TIMESTAMP_TZ(9)"),
			f("flag", "VARCHAR(5)"),
			f("score", "FLOAT"),
		},
		[]warehouse.Column{
			f("amount", "NUMBER(38,0)"),
			f("ts", "TIMESTAMP_NTZ(9)"),
			f("flag", "BOOLEAN"),
			f("score", "NUMBER(38,0)"),
		},
	)
	want := []ColumnImpact{
		{Column: "amount", Change: ColumnChange{Kind: TypeChanged, From: "NUMBER(38,0)", To: "NUMBER(10,2)"}},
		{Column: "ts", Change: ColumnChange{Kind: TypeChanged, From: "TIMESTAMP_NTZ(9)", To: "TIMESTAMP_TZ(9)"}},
		{Column: "flag", Change: ColumnChange{Kind: TypeChanged, From: "BOOLEAN", To: "VARCHAR(5)"}},
		{Column: "score", Change: ColumnChange{Kind: TypeChanged, From: "NUMBER(38,0)", To: "FLOAT"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snowflake diff:\n got %+v\nwant %+v", got, want)
	}
}

func TestSafeWideningSeverityLedgerAndJSON(t *testing.T) {
	reader := Reader{Table: "main.fin.margins", Column: "id", DependencyType: lineage.Other}
	widen := ColumnChange{Kind: TypeChanged, From: "INT", To: "BIGINT", Widening: true}
	if got := ImpactSeverity(widen, []Reader{reader}, ""); got != SeverityLow {
		t.Fatalf("ImpactSeverity(widening) = %v, want SeverityLow", got)
	}
	if got := ImpactSeverity(widen, nil, "lineage error"); got != SeverityLow {
		t.Fatalf("ImpactSeverity(widening with error) = %v, want SeverityLow", got)
	}
	r := &Report{ChangedCount: 1, AtRisk: []Table{{
		Table: "main.shop.orders", Type: "table",
		Columns: []Column{{Column: "id", Change: widen, Readers: []Reader{reader}}},
	}}}
	if md := Markdown(r); !strings.Contains(md, "(`INT` → `BIGINT`, safe widening)") {
		t.Errorf("Markdown missing safe widening annotation:\n%s", md)
	}
	data, err := JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AtRisk []struct {
			Severity string `json:"severity"`
			Columns  []struct {
				Severity string `json:"severity"`
				Change   struct {
					Widening bool `json:"widening"`
				} `json:"change"`
			} `json:"columns"`
		} `json:"atRisk"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.AtRisk) != 1 || doc.AtRisk[0].Severity != "low" || doc.AtRisk[0].Columns[0].Severity != "low" || !doc.AtRisk[0].Columns[0].Change.Widening {
		t.Errorf("JSON = %s", data)
	}
}

func TestNestedStructFallbackLineageAndMissingColumn(t *testing.T) {
	src := &fakeSource{columnLinks: map[string][]lineage.ColumnLink{
		"p.marts.orders#user": {{Table: "p.marts.daily", Column: "user_zip", DependencyType: lineage.ExactCopy}},
	}}
	res, err := Build(context.Background(), []Candidate{{
		Table: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table",
		Prod: []warehouse.Column{{Name: "user", Type: "RECORD", Fields: []warehouse.Column{
			{Name: "id", Type: "INT64"},
			{Name: "zip", Type: "STRING"},
		}}},
		Dev: []warehouse.Column{{Name: "user", Type: "RECORD", Fields: []warehouse.Column{
			{Name: "id", Type: "INT64"},
		}}},
	}}, src, readerContext([]string{"p.marts.daily"}, map[string]string{
		"p.marts.daily": "SELECT u.zip FROM `p.marts.orders`, UNNEST([user]) u",
	}), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AtRisk) != 1 || len(res.AtRisk[0].Columns) != 1 {
		t.Fatalf("atRisk = %+v", res.AtRisk)
	}
	col := res.AtRisk[0].Columns[0]
	if col.Column != "user.zip" || len(col.Readers) != 1 || col.Readers[0].DependencyType != lineage.Other || !col.Readers[0].StillNamed {
		t.Fatalf("nested column impact = %+v", col)
	}
	if !missingColumn("Field name zip does not exist in STRUCT<id INT64> at [1:10]", "user.zip") {
		t.Error("missingColumn should match STRUCT field error for nested column")
	}
}

func TestDataformOnSchemaChangeInBreaksRun(t *testing.T) {
	// Real Dataform compile output writes onSchemaChange:"IGNORE" on default
	// incremental tables — verify testdata/incremental.json still sets ownRun=true.
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "incremental.json"))
	if err != nil {
		t.Fatal(err)
	}
	incGraph, err := dataform.ParseCompileJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []project.ActionID{"demo-project.reporting.append_only_log", "demo-project.reporting.daily_events"} {
		a := incGraph.Actions[id]
		if a == nil {
			t.Fatalf("missing %s in testdata/incremental.json", id)
		}
		if own, _, _ := incrementalRun(a); !own {
			t.Errorf("%s (onSchemaChange=%q): ownRun = false, want true", id, a.OnSchemaChange)
		}
	}

	g, err := dataform.ParseCompileJSON([]byte(`{"tables":[
		{"type":"incremental","target":{"database":"p","schema":"marts","name":"synced"},"fileName":"definitions/synced.sqlx",
		 "query":"SELECT id FROM src","incrementalQuery":"SELECT id FROM src","uniqueKey":["id","dropped_key"],"onSchemaChange":"SYNCHRONIZE"},
		{"type":"incremental","target":{"database":"p","schema":"marts","name":"ignored"},"fileName":"definitions/ignored.sqlx",
		 "query":"SELECT id FROM src","incrementalQuery":"SELECT id FROM src","onSchemaChange":"IGNORE"},
		{"type":"incremental","target":{"database":"p","schema":"marts","name":"strict"},"fileName":"definitions/strict.sqlx",
		 "query":"SELECT id FROM src","incrementalQuery":"SELECT id FROM src","onSchemaChange":"FAIL"}
	],"projectConfig":{"defaultDatabase":"p"}}`))
	if err != nil {
		t.Fatal(err)
	}
	res := &changes.Result{BaseRef: "origin/main", HeadRef: "feat", Diff: changes.Diff{Changed: []changes.Changed{
		{ID: "p.marts.synced", FileName: "definitions/synced.sqlx", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
		{ID: "p.marts.ignored", FileName: "definitions/ignored.sqlx", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
		{ID: "p.marts.strict", FileName: "definitions/strict.sqlx", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
	}}}
	prod := []warehouse.Column{f("id", "INT64"), f("dropped_key", "INT64"), f("note", "STRING")}
	deps := Deps{
		Schema: func(context.Context, string) ([]warehouse.Column, error) { return prod, nil },
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			return &warehouse.Check{Schema: []warehouse.Column{f("id", "STRING")}}, nil
		},
		Readers: &fakeSource{},
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string]map[string]string{}
	for _, tb := range report.AtRisk {
		m := map[string]string{}
		for _, c := range tb.Columns {
			m[c.Column] = c.Change.BreaksRun
		}
		byTable[tb.Table] = m
	}
	// SYNCHRONIZE: non-key drop does NOT break run, but dropped uniqueKey STILL does.
	if want := map[string]string{"id": "", "dropped_key": BreaksRunUniqueKey, "note": ""}; !reflect.DeepEqual(byTable["p.marts.synced"], want) {
		t.Errorf("synced breaks = %v, want %v", byTable["p.marts.synced"], want)
	}
	// IGNORE (Dataform default): drops break the incremental run; retypes don't.
	if want := map[string]string{"id": "", "dropped_key": BreaksRunTable, "note": BreaksRunTable}; !reflect.DeepEqual(byTable["p.marts.ignored"], want) {
		t.Errorf("ignored breaks = %v, want %v", byTable["p.marts.ignored"], want)
	}
	// FAIL: both drops and retypes break the incremental run.
	if want := map[string]string{"id": BreaksRunTable, "dropped_key": BreaksRunTable, "note": BreaksRunTable}; !reflect.DeepEqual(byTable["p.marts.strict"], want) {
		t.Errorf("strict breaks = %v, want %v", byTable["p.marts.strict"], want)
	}
}

func TestDbtIncrementalOnSchemaChangeAndUniqueKey(t *testing.T) {
	manifestJSON := []byte(`{
		"metadata": {"dbt_version": "1.9.0", "project_name": "shop", "adapter_type": "databricks"},
		"nodes": {
			"model.shop.orders_default": {
				"unique_id": "model.shop.orders_default", "resource_type": "model", "name": "orders_default",
				"package_name": "shop", "database": "p", "schema": "marts", "original_file_path": "models/orders_default.sql",
				"compiled_code": "SELECT id FROM src",
				"config": {"materialized": "incremental", "unique_key": "dropped_key"}
			},
			"model.shop.orders_sync": {
				"unique_id": "model.shop.orders_sync", "resource_type": "model", "name": "orders_sync",
				"package_name": "shop", "database": "p", "schema": "marts", "original_file_path": "models/orders_sync.sql",
				"compiled_code": "SELECT id FROM src",
				"config": {"materialized": "incremental", "on_schema_change": "sync_all_columns", "unique_key": "dropped_key"}
			},
			"model.shop.orders_overwrite": {
				"unique_id": "model.shop.orders_overwrite", "resource_type": "model", "name": "orders_overwrite",
				"package_name": "shop", "database": "p", "schema": "marts", "original_file_path": "models/orders_overwrite.sql",
				"compiled_code": "SELECT id FROM src",
				"config": {"materialized": "incremental", "on_schema_change": "append_new_columns", "incremental_strategy": "insert_overwrite", "unique_key": "dropped_key"}
			},
			"model.shop.orders_fail": {
				"unique_id": "model.shop.orders_fail", "resource_type": "model", "name": "orders_fail",
				"package_name": "shop", "database": "p", "schema": "marts", "original_file_path": "models/orders_fail.sql",
				"compiled_code": "SELECT id FROM src",
				"config": {"materialized": "incremental", "on_schema_change": "fail", "unique_key": ["id", "dropped_key"]}
			}
		}
	}`)
	g, err := (&dbt.Backend{}).Parse(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	res := &changes.Result{BaseRef: "origin/main", HeadRef: "feat", Diff: changes.Diff{Changed: []changes.Changed{
		{ID: "p.marts.orders_default", FileName: "models/orders_default.sql", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
		{ID: "p.marts.orders_sync", FileName: "models/orders_sync.sql", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
		{ID: "p.marts.orders_overwrite", FileName: "models/orders_overwrite.sql", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
		{ID: "p.marts.orders_fail", FileName: "models/orders_fail.sql", Type: "incremental", Reasons: []changes.Reason{changes.ReasonSQL}},
	}}}
	prod := []warehouse.Column{f("id", "int"), f("dropped_key", "int"), f("note", "string")}
	deps := Deps{
		Warehouse: project.Databricks,
		OnDemand:  true,
		Schema:    func(context.Context, string) ([]warehouse.Column, error) { return prod, nil },
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			return &warehouse.Check{Schema: []warehouse.Column{f("id", "bigint")}}, nil
		},
		Readers: &fakeSource{},
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string]Table{}
	breaksByTable := map[string]map[string]string{}
	for _, tb := range report.AtRisk {
		byTable[tb.Table] = tb
		m := map[string]string{}
		for _, c := range tb.Columns {
			m[c.Column] = c.Change.BreaksRun
		}
		breaksByTable[tb.Table] = m
	}
	// Default (ignore): drops break run (and widening INT->BIGINT is NOT downgraded on OwnRun).
	if want := map[string]string{"id": "", "dropped_key": BreaksRunUniqueKey, "note": BreaksRunTable}; !reflect.DeepEqual(breaksByTable["p.marts.orders_default"], want) {
		t.Errorf("orders_default breaks = %v, want %v", breaksByTable["p.marts.orders_default"], want)
	}
	for _, c := range byTable["p.marts.orders_default"].Columns {
		if c.Column == "id" && c.Change.Widening {
			t.Error("INT -> BIGINT on an incremental OwnRun table should not be marked low-severity Widening")
		}
	}
	// sync_all_columns: non-key drops don't break run, but dropped unique_key still does.
	if want := map[string]string{"id": "", "dropped_key": BreaksRunUniqueKey, "note": ""}; !reflect.DeepEqual(breaksByTable["p.marts.orders_sync"], want) {
		t.Errorf("orders_sync breaks = %v, want %v", breaksByTable["p.marts.orders_sync"], want)
	}
	// append_new_columns + insert_overwrite: neither non-key drop nor unique_key breaks run.
	if want := map[string]string{"id": "", "dropped_key": "", "note": ""}; !reflect.DeepEqual(breaksByTable["p.marts.orders_overwrite"], want) {
		t.Errorf("orders_overwrite breaks = %v, want %v", breaksByTable["p.marts.orders_overwrite"], want)
	}
	// fail: dropped unique_key, dropped note, AND retyped id all break run.
	if want := map[string]string{"id": BreaksRunTable, "dropped_key": BreaksRunUniqueKey, "note": BreaksRunTable}; !reflect.DeepEqual(breaksByTable["p.marts.orders_fail"], want) {
		t.Errorf("orders_fail breaks = %v, want %v", breaksByTable["p.marts.orders_fail"], want)
	}
	// dbt wording in ledger uses unique_key and --full-refresh, not Dataform.
	md := Markdown(report)
	for _, want := range []string{"`unique_key`", "dbt's incremental run requires", "`--full-refresh`"} {
		if !strings.Contains(md, want) {
			t.Errorf("dbt Markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Dataform's") {
		t.Errorf("dbt Markdown mentioned Dataform:\n%s", md)
	}
}
