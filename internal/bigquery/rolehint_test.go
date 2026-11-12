package bigquery

import (
	"errors"
	"testing"
)

func TestRoleHint(t *testing.T) {
	for msg, want := range map[string]string{
		"Access Denied: Project acme-dev: User does not have bigquery.jobs.create permission in project acme-dev.":                     "needs roles/bigquery.jobUser on acme-dev",
		"Access Denied: Table acme-prod:sales.orders: User does not have permission to query table acme-prod:sales.orders, or perhaps": "needs roles/bigquery.dataViewer on the dataset acme-prod.sales",
		"Access Denied: Table acme-prod:sales.orders: Permission bigquery.tables.get denied on table acme-prod:sales.orders (or it":    "needs roles/bigquery.dataViewer on the dataset acme-prod.sales",
		"Access Denied: Dataset acme-prod:sales: Permission bigquery.tables.list denied on dataset acme-prod:sales (or it may not":     "needs roles/bigquery.dataViewer on the dataset acme-prod.sales",
		"Quota exceeded": "",
	} {
		if got := RoleHint(msg); got != want {
			t.Errorf("RoleHint(%q) = %q, want %q", msg, got, want)
		}
	}
	err := apiError(403, []byte(`{"error":{"message":"Access Denied: Project p: User does not have bigquery.jobs.create permission in project p."}}`))
	var e *APIError
	if !errors.As(err, &e) || e.Message != "Access Denied: Project p: User does not have bigquery.jobs.create permission in project p. (needs roles/bigquery.jobUser on p)" {
		t.Errorf("apiError = %v", err)
	}
}
