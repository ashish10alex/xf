package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

func snowflakeGraph() *project.Graph {
	orders := project.Target{Database: "analytics", Schema: "xf_example", Name: "fct_orders"}
	g := project.NewGraph([]*project.Action{
		{ID: orders.ID(), Target: orders, Kind: project.KindTable, FileName: "models/marts/fct_orders.sql", Query: "select 1 as order_id"},
	})
	g.Warehouse = project.Snowflake
	return g
}

func TestSnowflakeWebSupport(t *testing.T) {
	sf := &fakeDatabricks{}
	chosen := "COMPUTE_WH"
	deps := frontend.Deps{
		Backend: "dbt",
		Engine:  newFakeEngine(snowflakeGraph()),
		Now:     time.Now,
		Root:    t.TempDir(),
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			t.Error("Snowflake project must not auto dry-run")
			return &warehouse.Check{}, nil
		},
		QueryCost: func(context.Context, engine.CostRequest, func(cost.Progress)) (*cost.Report, error) { return nil, nil },
		ColumnImpact: func(context.Context, engine.ImpactRequest, func(impact.Progress)) (*impact.Report, error) {
			return nil, nil
		},
		Table: func(_ context.Context, db, schema, table string) (*warehouse.Table, error) {
			return &warehouse.Table{Type: "TABLE", LastModified: time.Now(), Schema: []warehouse.Column{{Name: "ORDER_ID", Type: "NUMBER(38,0)"}}}, nil
		},
	}
	p := &Project{
		Deps: deps,
		Snowflake: &Snowflake{
			TargetType: project.Snowflake,
			Client:     func(context.Context) (warehouse.Warehouse, error) { return sf, nil },
			Host:       func() string { return "https://acme-prod.snowflakecomputing.com" },
			TableURL: func(tg project.Target) string {
				return "https://app.snowflake.com/acme/prod/#/data/databases/" + tg.Database + "/schemas/" + tg.Schema + "/table/" + tg.Name
			},
			Warehouses: func(context.Context) ([]SQLWarehouse, error) {
				return []SQLWarehouse{{ID: "COMPUTE_WH", Name: "COMPUTE_WH", State: "SUSPENDED", Size: "X-Small", Serverless: true}}, nil
			},
			Choose: func(id string) (string, error) {
				chosen = id
				return ".xf/config.json", nil
			},
			Chosen:         func() bool { return chosen != "" },
			WarehouseID:    func() string { return chosen },
			ConfigKey:      "snowflakeWarehouse",
			CatalogName:    "Snowflake",
			WarehouseLabel: "virtual warehouse",
			PaletteCommand: "Snowflake: choose the virtual warehouse",
			AuthHint:       "set SNOWFLAKE_TOKEN",
		},
	}
	srv, ts, c := newTestProject(t, p)

	caps := srv.only().boot().Caps
	if caps.Warehouse != project.Snowflake || !caps.Check || caps.DryRun || caps.Defer || !caps.Scratch || !caps.Inspector {
		t.Fatalf("caps = %+v", caps)
	}

	// Action view shows built table columns from Snowflake and Snowsight link without auto-checking.
	getAction := func() actionView {
		var v actionView
		getJSON(t, c, ts.URL+"/api/action?id=analytics.xf_example.fct_orders", &v)
		return v
	}
	av := waitView(t, getAction, func(v actionView) bool { return v.Columns.Note != "" })
	if !strings.Contains(av.ConsoleURL, "app.snowflake.com/acme/prod") {
		t.Errorf("consoleUrl = %q, want Snowsight link", av.ConsoleURL)
	}
	if !strings.Contains(av.Columns.Note, "from Snowflake") {
		t.Errorf("columns note = %q", av.Columns.Note)
	}
	if len(sf.checked) != 0 {
		t.Fatalf("checked before asking: %v", sf.checked)
	}

	// Explicit POST /api/check checks on the virtual warehouse.
	var ok okView
	postJSON(t, c, ts.URL+"/api/check", map[string]string{"id": "analytics.xf_example.fct_orders"}, &ok)
	if !ok.OK {
		t.Fatalf("check = %+v", ok)
	}
	av = waitView(t, getAction, func(v actionView) bool { return strings.Contains(v.Columns.Note, "from the check") })
	if !strings.Contains(av.Columns.Note, "from the check") {
		t.Fatalf("check columns never arrived: %+v", av.Columns)
	}

	// Warehouse list & choice endpoints work for Snowflake virtual warehouses.
	var whResp struct {
		Warehouses []SQLWarehouse `json:"warehouses"`
		Current    string         `json:"current"`
	}
	getJSON(t, c, ts.URL+"/api/databricks/warehouses", &whResp)
	if len(whResp.Warehouses) != 1 || whResp.Warehouses[0].Name != "COMPUTE_WH" || whResp.Current != "COMPUTE_WH" {
		t.Fatalf("warehouses = %+v", whResp)
	}
	var chooseResp okView
	postJSON(t, c, ts.URL+"/api/databricks/warehouse", map[string]string{"id": "BI_WH"}, &chooseResp)
	if !chooseResp.OK || !strings.Contains(chooseResp.Message, "snowflakeWarehouse") || chosen != "BI_WH" {
		t.Fatalf("choose = %+v, chosen=%q", chooseResp, chosen)
	}

	// Scratch dry run reports noDryRun and never wakes the warehouse.
	var dry scratchDryView
	postJSON(t, c, ts.URL+"/api/scratch/dryrun", map[string]string{"sql": "SELECT 1"}, &dry)
	if !dry.OK || !dry.NoDryRun || dry.Writes {
		t.Fatalf("dry = %+v", dry)
	}

	// Scratch functions endpoint returns empty list for snowflake dialect.
	var fns []any
	getJSON(t, c, ts.URL+"/api/scratch/functions?dialect=snowflake", &fns)
	if len(fns) != 0 {
		t.Fatalf("snowflake functions = %v, want empty", fns)
	}
}

func TestStandaloneScratchSnowflake(t *testing.T) {
	srv, err := New(Config{
		OpenScratch: func(wh string) (*Project, error) {
			return &Project{
				Standalone:   true,
				StartScratch: true,
				ScratchDir:   t.TempDir(),
				Deps:         frontend.Deps{Root: t.TempDir(), Engine: newFakeEngine(project.NewGraph(nil)), Now: time.Now},
				Snowflake: &Snowflake{
					TargetType:  project.Snowflake,
					Client:      func(context.Context) (warehouse.Warehouse, error) { return &fakeDatabricks{}, nil },
					Host:        func() string { return "https://acme-prod.snowflakecomputing.com" },
					WarehouseID: func() string { return "COMPUTE_WH" },
					Profiles: func() ([]DatabricksProfile, string) {
						return []DatabricksProfile{{Name: "prod", Host: "acme-prod"}}, "prod"
					},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var openSF struct {
		OK  bool   `json:"ok"`
		Key string `json:"key"`
	}
	postJSON(t, ts.Client(), ts.URL+"/api/scratch/open", map[string]string{"warehouse": project.Snowflake}, &openSF)
	if !openSF.OK || openSF.Key != ScratchKeySnowflake {
		t.Fatalf("openSF = %+v", openSF)
	}
	var boot bootView
	getJSON(t, ts.Client(), ts.URL+"/api/boot?project="+ScratchKeySnowflake, &boot)
	if boot.Project == nil || boot.Project.Name != "Scratch · Snowflake" || boot.Project.Caps.Warehouse != project.Snowflake || boot.Project.Scratch.Profile != "prod" {
		t.Fatalf("sf boot = %+v", boot.Project)
	}
}
