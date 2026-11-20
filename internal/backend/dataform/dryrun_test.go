package dataform

import (
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func TestDryRunQueries(t *testing.T) {
	basic := loadGraph(t, "basic.json")
	inc := loadGraph(t, "incremental.json")
	type q struct{ label, prefix, contains string }
	tests := []struct {
		name   string
		action *project.Action
		want   []q
	}{
		{"table", basic.Actions["demo-project.analytics.daily_orders"], []q{{"", "SELECT", "GROUP BY"}}},
		{"assertion", basic.Actions["demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0"], []q{{"", "SELECT", "index_row_count > 1"}}},
		{"operation joins its statements", basic.Actions["demo-project.analytics.refresh_permissions"], []q{{"", "GRANT", "analysts@example.com\";\nSELECT 1;"}}},
		{"incremental with pre_operations", inc.Actions["demo-project.reporting.daily_events"], []q{
			{"full", "DECLARE checkpoint", ");\n"},
			{"incremental", "DECLARE checkpoint", "MAX(event_date)"},
		}},
		{"incremental without ops", inc.Actions["demo-project.reporting.append_only_log"], []q{{"full", "", ""}, {"incremental", "", "MAX(event_ts)"}}},
		{"declaration", basic.Actions["raw-project.shop.orders"], nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.action == nil {
				t.Fatal("no such action in the fixture")
			}
			got := tt.action.DryRunQueries()
			if len(got) != len(tt.want) {
				t.Fatalf("got %d queries, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Label != w.label || !strings.HasPrefix(g.SQL, w.prefix) || !strings.Contains(g.SQL, w.contains) {
					t.Errorf("query %d = %q %q, want label %q, prefix %q, containing %q", i, g.Label, g.SQL, w.label, w.prefix, w.contains)
				}
				if strings.TrimSpace(g.SQL) != g.SQL {
					t.Errorf("query %d not trimmed: %q", i, g.SQL)
				}
				if strings.Contains(g.SQL, "ALTER TABLE") {
					t.Errorf("query %d includes post_operations: %q", i, g.SQL)
				}
				// A query after pre_operations keeps them apart, to be dry
				// run without some (cost leaves out DML it may not write).
				hasPre := strings.HasPrefix(w.prefix, "DECLARE")
				if hasPre != (len(g.PreOps) > 0) {
					t.Errorf("query %d pre_operations %q", i, g.PreOps)
				}
				if hasPre {
					if sql, _ := project.Script(append(append([]string{}, g.PreOps...), g.Query)); sql != g.SQL || g.Query == "" {
						t.Errorf("query %d: pre_operations %q and query %q make %q, not its SQL", i, g.PreOps, g.Query, sql)
					}
				}
			}
		})
	}
}

func TestGraphProjectDefaults(t *testing.T) {
	g := loadGraph(t, "basic.json")
	if g.DefaultDatabase != "demo-project" || g.DefaultLocation != "EU" {
		t.Errorf("defaults = %q %q", g.DefaultDatabase, g.DefaultLocation)
	}
}
