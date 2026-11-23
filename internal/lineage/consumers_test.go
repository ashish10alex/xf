package lineage

import (
	"strings"
	"testing"
)

func TestParseConsumer(t *testing.T) {
	tests := []struct {
		name      string
		sig       QuerySignal
		wantTool  string
		wantAsset string
		wantUser  string
		wantLabel string
	}{
		{
			name: "looker comment with explore and dashboard",
			sig: QuerySignal{
				User:  "looker-prod@acme.iam.gserviceaccount.com",
				Query: "-- Looker Query Context '{\"user_id\":12,\"explore\":\"orders\",\"dashboard_id\":\"412\"}'\nSELECT customer_tier FROM acme.marts.fct_orders",
			},
			wantTool:  "Looker",
			wantAsset: "explore orders, dashboard 412",
			wantUser:  "looker-prod@acme.iam.gserviceaccount.com",
			wantLabel: "Looker (explore orders, dashboard 412)",
		},
		{
			name: "looker snowflake query tag with look_id",
			sig: QuerySignal{
				User:     "LOOKER_SVC",
				QueryTag: `{"looker":{"model":"finance"},"explore":"revenue","look_id":99}`,
			},
			wantTool:  "Looker",
			wantAsset: "explore revenue, look 99",
			wantUser:  "LOOKER_SVC",
			wantLabel: "Looker (explore revenue, look 99)",
		},
		{
			name: "lightdash query tag with explore key does NOT become Looker",
			sig: QuerySignal{
				User:     "bi@acme.com",
				QueryTag: `{"lightdash":true,"explore":"orders","chart_name":"revenue_by_day"}`,
			},
			wantTool:  "Lightdash",
			wantAsset: "revenue_by_day",
			wantUser:  "bi@acme.com",
			wantLabel: "Lightdash (revenue_by_day)",
		},
		{
			name: "tableau query origins with workbook luid",
			sig: QuerySignal{
				User:  "tableau-prod@acme.iam.gserviceaccount.com",
				Query: `/* "tableau-query-origins": {"workbook-luid": "a3f9c2e1-1111-2222-3333-444455556666"} */ SELECT * FROM acme.marts.fct_orders`,
			},
			wantTool:  "Tableau",
			wantAsset: "workbook a3f9c2e1",
			wantUser:  "tableau-prod@acme.iam.gserviceaccount.com",
			wantLabel: "Tableau (workbook a3f9c2e1)",
		},
		{
			name: "tableau luid in SQL WHERE clause is NOT misclassified as Tableau",
			sig: QuerySignal{
				User:  "alice@acme.com",
				Query: "SELECT * FROM metadata.assets WHERE datasource_luid = 'abc12345'",
			},
			wantTool:  "",
			wantAsset: "",
			wantUser:  "alice@acme.com",
			wantLabel: "alice@acme.com",
		},
		{
			name: "tableau sql hint fallback",
			sig: QuerySignal{
				User:  "alice@acme.com",
				Query: "SELECT `Custom SQL Query`.`customer_tier` FROM (SELECT customer_tier FROM marts.fct_orders) `Custom SQL Query`",
			},
			wantTool:  "Tableau",
			wantAsset: "",
			wantUser:  "alice@acme.com",
			wantLabel: "Tableau (alice@acme.com)",
		},
		{
			name: "tableau bigquery vizql calculation alias without labels or comments",
			sig: QuerySignal{
				User:  "analyst@acme.com",
				Query: "SELECT COUNT(DISTINCT `A500_TBL`.`ORDER_ID`) AS `TEMP_Calculation_1192328018925068291__1592790816__0_`, 1.1000000000000001 AS `X__alias__0` FROM `p.d`.`t` `A500_TBL` GROUP BY 2",
			},
			wantTool:  "Tableau",
			wantAsset: "",
			wantUser:  "analyst@acme.com",
			wantLabel: "Tableau (analyst@acme.com)",
		},
		{
			name: "tableau bigquery vizql date alias without labels or comments",
			sig: QuerySignal{
				User:  "analyst@acme.com",
				Query: "SELECT EXTRACT(DAYOFWEEK FROM `T`.`REPORT_DATE`) AS `wd_REPORT_DATE_ok` FROM `p.d`.`t` `T` GROUP BY 1",
			},
			wantTool:  "Tableau",
			wantAsset: "",
			wantUser:  "analyst@acme.com",
			wantLabel: "Tableau (analyst@acme.com)",
		},
		{
			name: "tableau service account only",
			sig: QuerySignal{
				User:  "tableau-prod@acme.iam.gserviceaccount.com",
				Query: "SELECT customer_tier FROM acme.marts.fct_orders",
			},
			wantTool:  "Tableau",
			wantAsset: "",
			wantUser:  "tableau-prod@acme.iam.gserviceaccount.com",
			wantLabel: "Tableau (tableau-prod@acme.iam.gserviceaccount.com)",
		},
		{
			name: "metabase comment",
			sig: QuerySignal{
				User:  "metabase@acme.iam.gserviceaccount.com",
				Query: "-- Metabase:: userID: 7 queryType: MBQL dashboardID: 15 cardID: 42\nSELECT customer_tier FROM marts.fct_orders",
			},
			wantTool:  "Metabase",
			wantAsset: "dashboard 15, card 42",
			wantUser:  "metabase@acme.iam.gserviceaccount.com",
			wantLabel: "Metabase (dashboard 15, card 42)",
		},
		{
			name: "looker studio label with report id",
			sig: QuerySignal{
				User:   "bob@acme.com",
				Labels: map[string]string{"requestor": "looker_studio", "looker_studio_report_id": "9f8e7d6c-5555-4444"},
				Query:  "SELECT customer_tier FROM marts.fct_orders",
			},
			wantTool:  "Looker Studio",
			wantAsset: "report 9f8e7d6c",
			wantUser:  "bob@acme.com",
			wantLabel: "Looker Studio (report 9f8e7d6c)",
		},
		{
			name: "bigquery scheduled query label",
			sig: QuerySignal{
				User:   "sched@acme.iam.gserviceaccount.com",
				Labels: map[string]string{"data_source_id": "scheduled_query"},
			},
			wantTool:  "Scheduled query",
			wantUser:  "sched@acme.iam.gserviceaccount.com",
			wantLabel: "Scheduled query (sched@acme.iam.gserviceaccount.com)",
		},
		{
			name: "databricks client application power bi",
			sig: QuerySignal{
				User:      "svc_bi@acme.com",
				ClientApp: "Microsoft Power BI",
			},
			wantTool:  "Power BI",
			wantUser:  "svc_bi@acme.com",
			wantLabel: "Power BI (svc_bi@acme.com)",
		},
		{
			name: "hightouch service account",
			sig: QuerySignal{
				User: "hightouch-sync@acme.iam.gserviceaccount.com",
			},
			wantTool:  "Hightouch",
			wantUser:  "hightouch-sync@acme.iam.gserviceaccount.com",
			wantLabel: "Hightouch (hightouch-sync@acme.iam.gserviceaccount.com)",
		},
		{
			name: "ordinary comments do NOT false-trigger Mode/Hex/Sigma/Omni",
			sig: QuerySignal{
				User:  "alex.smith@acme.com",
				Query: "-- mode: incremental\n/* hex-encoded id */\n-- sigma = stddev(x)\nSELECT '-- Looker Query Context ''{\"explore\":\"fake\"}''' FROM marts.fct_orders",
			},
			wantTool:  "",
			wantUser:  "alex.smith@acme.com",
			wantLabel: "alex.smith@acme.com",
		},
		{
			name: "substring words in tags/labels/clientApp do NOT false-trigger",
			sig: QuerySignal{
				User:      "analyst@acme.com",
				QueryTag:  `{"pipeline":"omnichannel_us_census_data","mode":"hexadecimal"}`,
				ClientApp: "internal_preset_runner",
				Labels:    map[string]string{"dataset": "us_census_data", "channel": "omnichannel"},
			},
			wantTool:  "",
			wantUser:  "analyst@acme.com",
			wantLabel: "analyst@acme.com",
		},
		{
			name: "snowflake role fallback when user empty",
			sig: QuerySignal{
				Role: "SIGMA_READER",
			},
			wantTool:  "Sigma",
			wantUser:  "SIGMA_READER",
			wantLabel: "Sigma (SIGMA_READER)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseConsumer(tc.sig)
			if got.Tool != tc.wantTool {
				t.Errorf("Tool = %q, want %q", got.Tool, tc.wantTool)
			}
			if got.Asset != tc.wantAsset {
				t.Errorf("Asset = %q, want %q", got.Asset, tc.wantAsset)
			}
			if got.User != tc.wantUser {
				t.Errorf("User = %q, want %q", got.User, tc.wantUser)
			}
			if got.Label() != tc.wantLabel {
				t.Errorf("Label() = %q, want %q", got.Label(), tc.wantLabel)
			}
			if got.ID() == "" {
				t.Error("ID() returned empty string")
			}
		})
	}
}

func TestParseLabelsDeterministic(t *testing.T) {
	labels := map[string]string{
		"requestor": "looker_studio",
		"team":      "hightouch",
		"extra":     "tableau",
	}
	firstTool, firstAsset, _ := parseLabels(labels)
	for range 50 {
		tool, asset, _ := parseLabels(labels)
		if tool != firstTool || asset != firstAsset {
			t.Fatalf("non-deterministic parseLabels: got (%q,%q), want (%q,%q)", tool, asset, firstTool, firstAsset)
		}
	}
	if firstTool != "Looker Studio" {
		t.Errorf("got %q, want Looker Studio", firstTool)
	}
}

func TestConsumerUnicodeIDsDoNotCollide(t *testing.T) {
	a := Consumer{Tool: "Looker", Asset: "売上レポート"}
	b := Consumer{Tool: "Looker", Asset: "顧客分析"}
	if a.ID() == b.ID() {
		t.Fatalf("distinct non-ASCII consumers collided on ID %q", a.ID())
	}
}

func TestConsumerNoteAndSanitization(t *testing.T) {
	c := Consumer{
		Tool:     "Looker",
		Asset:    "explore `orders` | **bad** [click](https://evil) <script>\x1b[31mred\x1b[0m\u202ebidi\\end\nline",
		User:     "looker-prod@acme.iam.gserviceaccount.com",
		Queries:  840,
		Days:     14,
		LastSeen: "2026-11-20T14:32:00Z",
	}
	lbl := c.Label()
	for _, bad := range []string{"`", "*", "<", ">", "[", "]", "\\", "\x1b", "\u202e", "\n", "|"} {
		if strings.Contains(lbl, bad) {
			t.Errorf("Label() leaked unsafe char %q in %q", bad, lbl)
		}
	}
	if got := c.Note(); got != "840 queries in 14d · last 2026-11-20 · looker-prod@acme.iam.gserviceaccount.com" {
		t.Errorf("Note() = %q", got)
	}

	plain := Consumer{User: "alice@acme.com", Queries: 1, Days: 14}
	if got := plain.Note(); got != "1 query in 14d · external query" {
		t.Errorf("plain.Note() = %q", got)
	}

	saOnly := Consumer{Tool: "Tableau", User: "tableau@acme.iam.gserviceaccount.com", Queries: 24, Days: 14}
	if got := saOnly.Note(); got != "24 queries in 14d" {
		t.Errorf("saOnly.Note() = %q", got)
	}
}

func TestIsOwnXFQueryAndTransformToolQuery(t *testing.T) {
	if !IsOwnXFQuery("xf_scratch_abc123", "SELECT 1", nil) {
		t.Error("expected xf_scratch_ job to be recognized")
	}
	if !IsOwnXFQuery("xf_impact_abc123", "SELECT 1", nil) {
		t.Error("expected xf_impact_ job to be recognized")
	}
	if !IsOwnXFQuery("job1", "SELECT 1", map[string]string{"app": "xf"}) {
		t.Error("expected app=xf label to be recognized")
	}
	if !IsOwnXFQuery("job1", "/* xf:query-history */ SELECT 1", nil) {
		t.Error("expected xf:query-history marker to be recognized")
	}
	if !IsOwnXFQuery("job1", "/* xf:scratch */ SELECT 1", nil) {
		t.Error("expected xf:scratch marker to be recognized")
	}
	if IsOwnXFQuery("bquxjob_123", "SELECT customer_tier FROM marts.fct_orders", map[string]string{"requestor": "looker_studio"}) {
		t.Error("unexpectedly matched external BI query")
	}

	if !IsTransformToolQuery(`/* {"app": "dbt", "node_id": "test.pkg.not_null"} */ SELECT * FROM marts.fct_orders`, "", nil) {
		t.Error("expected dbt header comment to be recognized as transform tool query")
	}
	if !IsTransformToolQuery("SELECT 1", "", map[string]string{"dataform_workflow_invocation": "inv-1"}) {
		t.Error("expected Dataform label to be recognized as transform tool query")
	}
	if IsTransformToolQuery("SELECT customer_tier FROM marts.fct_orders", "", nil) {
		t.Error("unexpectedly matched ordinary BI query as transform tool")
	}
}

func TestQueryReadsColumn(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		col       string
		dblString bool
		wantRead  bool
		wantStar  bool
	}{
		{
			name:      "explicit column read",
			sql:       "SELECT order_id, customer_tier FROM marts.fct_orders WHERE amount > 0",
			col:       "customer_tier",
			dblString: true,
			wantRead:  true,
		},
		{
			name:      "case insensitive backticked column",
			sql:       "SELECT `CUSTOMER_TIER` FROM `acme.marts.fct_orders`",
			col:       "customer_tier",
			dblString: true,
			wantRead:  true,
		},
		{
			name:      "bigquery double-quoted string literal ignored",
			sql:       `SELECT "customer_tier", order_id FROM marts.fct_orders`,
			col:       "customer_tier",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
		{
			name:      "snowflake double-quoted identifier matched",
			sql:       `SELECT "customer_tier", order_id FROM marts.fct_orders`,
			col:       "customer_tier",
			dblString: false,
			wantRead:  true,
		},
		{
			name:      "column only in comment or single-quoted string ignored",
			sql:       "-- customer_tier\nSELECT 'customer_tier', order_id FROM marts.fct_orders",
			col:       "customer_tier",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
		{
			name:      "select star",
			sql:       "SELECT * FROM marts.fct_orders LIMIT 100",
			col:       "customer_tier",
			dblString: true,
			wantStar:  true,
		},
		{
			name:      "select a, star",
			sql:       "SELECT 1 AS x, * FROM marts.fct_orders",
			col:       "customer_tier",
			dblString: true,
			wantStar:  true,
		},
		{
			name:      "select a, qualified star",
			sql:       "SELECT a.id, `p.d.t`.* FROM `p.d.t`",
			col:       "customer_tier",
			dblString: true,
			wantStar:  true,
		},
		{
			name:      "select as struct star",
			sql:       "SELECT AS STRUCT * FROM marts.fct_orders",
			col:       "customer_tier",
			dblString: true,
			wantStar:  true,
		},
		{
			name:      "select top n star",
			sql:       "SELECT TOP 10 * FROM marts.fct_orders",
			col:       "customer_tier",
			dblString: false,
			wantStar:  true,
		},
		{
			name:      "count star and arithmetic multiply are NOT select star",
			sql:       "SELECT COUNT(*), price * qty FROM marts.fct_orders",
			col:       "customer_tier",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
		{
			name:      "nested struct leaf column match",
			sql:       "SELECT address.zip FROM marts.fct_orders",
			col:       "user.address.zip",
			dblString: true,
			wantRead:  true,
		},
		{
			name:      "malformed trailing dot column never matches",
			sql:       "SELECT order_id FROM marts.fct_orders",
			col:       "user.",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
		{
			name:      "whole word boundary prevents substring false match",
			sql:       "SELECT tier_id FROM marts.fct_orders",
			col:       "tier",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
		{
			name:      "unicode boundary prevents false match next to non-ascii letter",
			sql:       "SELECT tieré FROM marts.fct_orders",
			col:       "tier",
			dblString: true,
			wantRead:  false,
			wantStar:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := StripQueryForMatch(tc.sql, tc.dblString)
			reads, star := NewColumnMatcher(tc.col).Match(q)
			if reads != tc.wantRead || star != tc.wantStar {
				t.Errorf("Match(%q) = (%v, %v), want (%v, %v)", tc.col, reads, star, tc.wantRead, tc.wantStar)
			}
		})
	}
}
