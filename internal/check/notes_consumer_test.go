package check

import (
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
)

type stubSource struct {
	text map[string]string
	diff map[string]string
}

func (s stubSource) Text(file string) string { return s.text[file] }
func (s stubSource) Diff(file string) string { return s.diff[file] }

func TestNotesFormatsExternalConsumerNamesCleanly(t *testing.T) {
	looker := &lineage.Consumer{
		Tool:    "Looker",
		Asset:   "explore orders, dashboard 412",
		User:    "looker-prod@acme.iam.gserviceaccount.com",
		Queries: 840,
		Days:    14,
	}
	r := &Report{
		Impact: &impact.Report{
			AtRisk: []impact.Table{{
				Table:    "acme.marts.fct_orders",
				FileName: "definitions/marts/fct_orders.sqlx",
				Type:     "table",
				Columns: []impact.Column{{
					Column: "customer_tier",
					Change: impact.ColumnChange{Kind: impact.Dropped},
					Readers: []impact.Reader{{
						Table:          looker.ID(),
						DependencyType: lineage.Other,
						Consumer:       looker,
					}},
				}},
			}},
		},
	}
	notes := Notes(r, Options{Gate: true, FailOn: impact.SeverityCritical}, stubSource{
		diff: map[string]string{"definitions/marts/fct_orders.sqlx": "@@ -3,1 +3,0 @@\n-  customer_tier,\n"},
	}, "")
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	if notes[0].Level != Warning {
		t.Errorf("Level under --fail-on critical = %q, want warning", notes[0].Level)
	}
	if !strings.Contains(notes[0].Text, "Looker (explore orders, dashboard 412)") {
		t.Errorf("note text = %q, want Looker label", notes[0].Text)
	}
	if strings.Contains(notes[0].Text, "external:") {
		t.Errorf("note text leaked raw consumer ID: %q", notes[0].Text)
	}

	// Under --fail-on warning, external consumers still escalate to error annotations.
	warnGate := Notes(r, Options{Gate: true, FailOn: impact.SeverityWarning}, stubSource{
		diff: map[string]string{"definitions/marts/fct_orders.sqlx": "@@ -3,1 +3,0 @@\n-  customer_tier,\n"},
	}, "")
	if len(warnGate) != 1 || warnGate[0].Level != Error {
		t.Errorf("Level under --fail-on warning = %+v, want single error note", warnGate)
	}
}
