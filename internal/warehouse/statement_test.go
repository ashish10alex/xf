package warehouse

import "testing"

func TestStatementType(t *testing.T) {
	for sql, want := range map[string]string{
		"":                    "",
		"  -- just a comment": "",
		"select 1":            "SELECT",
		"-- top\n/* note */ WITH a AS (select 1) select * from a": "SELECT",
		"(select 1) union all (select 2)":                         "SELECT",
		"describe table x":                                        "SELECT",
		"show tables":                                             "SELECT",
		"insert into t values (1)":                                "INSERT",
		"MERGE INTO t USING s ON …":                               "MERGE",
		"create or replace temporary view v as select 1":          "CREATE_VIEW",
		"CREATE TABLE IF NOT EXISTS t (a int)":                    "CREATE_TABLE",
		"create materialized view m as select 1":                  "CREATE_MATERIALIZED_VIEW",
		"replace table t as select 1":                             "CREATE_TABLE",
		"drop table if exists t":                                  "DROP_TABLE",
		"alter table t add column b int":                          "ALTER_TABLE",
		"optimize t":                                              "OPTIMIZE",
	} {
		if got := StatementType(sql); got != want {
			t.Errorf("StatementType(%q) = %q, want %q", sql, got, want)
		}
	}
}
