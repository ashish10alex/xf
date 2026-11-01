package bigquery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/gcpauth"
)

// A client made while signed in follows the credentials: once they are
// gone (gcloud auth application-default revoke), its calls fail with
// ErrAuth, which says to log in again.
func TestClientFollowsTheCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.json")
	t.Setenv(gcpauth.EnvFile, path)
	body := `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"token","quota_project_id":"quota"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if q := c.QuotaProject(); q != "quota" {
		t.Fatalf("quota project = %q", q)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DryRun(context.Background(), "p", "", "select 1"); !errors.Is(err, ErrAuth) {
		t.Fatalf("with the credentials gone: %v, want ErrAuth", err)
	}
}
