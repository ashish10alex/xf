package check

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreflightQueryHistoryProbe(t *testing.T) {
	var probed []string
	p := goodProbes()
	p.DefaultProject = "acme-dev"
	p.ProdProject = "acme-prod"
	p.Location = "EU"
	p.QueryHistoryProjects = []string{"auto", "acme-bi"}
	p.QueryHistoryExtra = []string{"acme-extra"}
	p.QueryHistoryProbe = func(_ context.Context, billing, view, loc string, orgView bool) error {
		probed = append(probed, billing+":"+view+":"+loc)
		if view == "acme-bi" {
			return errors.New("Access Denied: bigquery.jobs.listAll")
		}
		return nil
	}

	out := Run(context.Background(), p)
	if out.Stops() {
		t.Fatalf("query-history warn should not stop preflight: %+v", out.Lines)
	}
	if len(probed) != 3 {
		t.Fatalf("probed = %v, want 3 (acme-prod, acme-extra, acme-bi)", probed)
	}
	var passFound, warnFound bool
	for _, l := range out.Lines {
		if l.Name != "Query History" {
			continue
		}
		if l.Status == Pass && strings.Contains(l.Detail, "acme-prod") {
			passFound = true
		}
		if l.Status == Warn && strings.Contains(l.Detail, "acme-bi") && strings.Contains(l.Fix, "roles/bigquery.resourceViewer") {
			warnFound = true
		}
	}
	if !passFound || !warnFound {
		t.Fatalf("expected one Pass and one Warn Query History line, got: %+v", out.Lines)
	}
}
