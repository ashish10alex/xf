package dataform

import (
	"slices"
	"testing"
)

func TestActionsInFile(t *testing.T) {
	g := loadGraph(t, "basic.json")
	tests := []struct {
		file string
		want []string
	}{
		{"definitions/daily_orders.sqlx", []string{
			"demo-project.analytics.daily_orders",
			"demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
			"demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0",
		}},
		{"definitions/customers_source.js", []string{"raw-project.shop.customers"}},
		{"includes/nothing.js", []string{}},
	}
	for _, tt := range tests {
		if got := ids(g.ActionsInFile(tt.file)); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.file, got, tt.want)
		}
	}
}
