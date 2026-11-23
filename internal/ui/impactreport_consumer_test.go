package ui

import (
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
)

func TestImpactReportLinesStripsConsumerMarkdownLinks(t *testing.T) {
	url := lineage.BigQueryJobURL("acme-bi", "EU", "job_xyz")
	cons := &lineage.Consumer{
		Tool:         "Tableau",
		User:         "mm@acme.com",
		Queries:      8,
		Days:         1,
		LastSeen:     "2026-11-23T12:00:00Z",
		SampleJobID:  "job_xyz",
		SampleJobURL: url,
	}
	rep := &impact.Report{
		AtRisk: []impact.Table{{
			Table: "acme-prod.marts.fct_orders",
			Columns: []impact.Column{{
				Column:  "platform",
				Change:  impact.ColumnChange{Kind: impact.Dropped},
				Readers: []impact.Reader{{Table: cons.ID(), DependencyType: lineage.Other, Consumer: cons}},
			}},
		}},
	}

	s := newStyles(true)
	out := strings.Join(s.impactReportLines(rep, 140), "\n")
	if strings.Contains(out, "https://") || strings.Contains(out, "[last 2026-11-23]") {
		t.Errorf("TUI impact report leaked raw Markdown link:\n%s", out)
	}
	if !strings.Contains(out, "last 2026-11-23") {
		t.Errorf("TUI impact report missing plain recency date:\n%s", out)
	}
}
