package gcpauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeADC writes an authorized_user credential file naming quota, which
// Google's library reads without going to the network, and dates it at,
// so two writes in the same instant still differ.
func writeADC(t *testing.T, path, quota string, at time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"token","quota_project_id":%q}`, quota)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationFollowsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.json")
	t.Setenv(EnvFile, path)
	at := time.Date(2026, 10, 26, 22, 31, 0, 0, time.UTC)
	writeADC(t, path, "one", at)

	g := Generation()
	if Generation() != g {
		t.Fatal("the generation moved with the file unchanged")
	}
	writeADC(t, path, "two", at.Add(time.Minute))
	g2 := Generation()
	if g2 == g {
		t.Fatal("a rewritten file kept its generation")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if Generation() == g2 {
		t.Fatal("a removed file kept its generation")
	}
}

// A new login is used from the next call; a file gone fails with
// ErrNoCredentials, and one back again is used at once.
func TestCredentialsFollowTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.json")
	t.Setenv(EnvFile, path)
	at := time.Date(2026, 10, 26, 22, 31, 0, 0, time.UTC)
	writeADC(t, path, "one", at)

	c, err := NewCredentials(scope)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.QuotaProject(); q != "one" {
		t.Fatalf("quota project = %q, want one", q)
	}
	writeADC(t, path, "two", at.Add(time.Minute))
	if q := c.QuotaProject(); q != "two" {
		t.Fatalf("after a new login, quota project = %q, want two", q)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Client().Get("http://127.0.0.1:1/never")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("with the file gone: %v, want ErrNoCredentials", err)
	}
	if q := c.QuotaProject(); q != "" {
		t.Fatalf("with the file gone, quota project = %q", q)
	}

	writeADC(t, path, "three", at.Add(2*time.Minute))
	if q := c.QuotaProject(); q != "three" {
		t.Fatalf("with the file back, quota project = %q, want three", q)
	}
}

func TestNewCredentialsFailsWithNone(t *testing.T) {
	t.Setenv(EnvFile, filepath.Join(t.TempDir(), "missing.json"))
	if _, err := NewCredentials(scope); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("got %v, want ErrNoCredentials", err)
	}
}
