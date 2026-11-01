package gcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/testutil"
)

// fakeGoogle stands in for Google's token and tokeninfo endpoints: a refresh
// with the token "good" gets an access token, which is email's.
func fakeGoogle(t *testing.T, email string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if r.Form.Get("refresh_token") == "rapt" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": "invalid_grant", "error_description": "reauth related error (invalid_rapt)", "error_subtype": "invalid_rapt"}`))
				return
			}
			if r.Form.Get("refresh_token") != "good" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": "invalid_grant", "error_description": "Token has been expired or revoked."}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token": "access", "token_type": "Bearer", "expires_in": 3600}`))
		case "/tokeninfo":
			if r.URL.RawQuery != "" || r.Form.Get("access_token") != "access" {
				t.Errorf("tokeninfo asked with query %q, token %q", r.URL.RawQuery, r.Form.Get("access_token"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"email": email})
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeCreds makes file the Application Default Credentials.
func writeCreds(t *testing.T, file map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "creds.json")
	data, _ := json.Marshal(file)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvFile, path)
	return path
}

func TestADCUserLogin(t *testing.T) {
	srv := fakeGoogle(t, "me@work.example")
	path := writeCreds(t, map[string]string{
		"type": "authorized_user", "client_id": "id", "client_secret": "secret", "refresh_token": "good",
		"token_uri": srv.URL + "/token", "quota_project_id": "sandbox",
	})
	got := Lookup{TokenInfoURL: srv.URL + "/tokeninfo"}.ADC(context.Background())
	want := ADC{Account: "me@work.example", Type: "authorized_user", Source: path, SourceFrom: "$" + EnvFile, QuotaProject: "sandbox"}
	if got != want {
		t.Errorf("ADC = %+v, want %+v", got, want)
	}
	rows := got.Rows(true)
	wantRows := [][3]string{
		{"xf uses", "me@work.example", ""},
		{"", "user login", "for xf's own calls; dbt signs in as profiles.yml says"},
		{"", path, "$" + EnvFile},
		{"quota", "sandbox", ""},
	}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Errorf("rows = %q, want %q", rows, wantRows)
	}
}

func TestADCExpiredLogin(t *testing.T) {
	srv := fakeGoogle(t, "")
	path := writeCreds(t, map[string]string{
		"type": "authorized_user", "client_id": "id", "client_secret": "secret", "refresh_token": "stale",
		"token_uri": srv.URL + "/token",
	})
	got := Lookup{TokenInfoURL: srv.URL + "/tokeninfo"}.ADC(context.Background())
	if got.Error != "Token has been expired or revoked." || got.Type != "authorized_user" || got.Source != path {
		t.Errorf("ADC = %+v", got)
	}
	rows := got.Rows(false)
	if rows[0] != [3]string{"xf uses", "(sign-in failed)", "Token has been expired or revoked."} || rows[1][1] != LoginHint || len(rows) != 5 {
		t.Errorf("rows = %q", rows)
	}
}

func TestADCNamesTheAccountInTheFile(t *testing.T) {
	// The key signs nothing, but the file says whose it is.
	const account = "runner@proj.iam.gserviceaccount.com"
	writeCreds(t, map[string]string{"type": "service_account", "client_email": account, "private_key": "not a key"})
	got := Lookup{}.ADC(context.Background())
	if got.Account != account || got.Type != "service_account" || got.Error == "" {
		t.Errorf("ADC = %+v", got)
	}
	if row := got.Rows(false)[0]; row[1] != account+" (sign-in failed)" {
		t.Errorf("first row = %q", row)
	}
	if got := impersonated("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@p.iam.gserviceaccount.com:generateAccessToken"); got != "sa@p.iam.gserviceaccount.com" {
		t.Errorf("impersonated = %q", got)
	}
}

func TestADCWithoutCredentials(t *testing.T) {
	t.Setenv(EnvFile, filepath.Join(t.TempDir(), "missing.json"))
	got := Lookup{}.ADC(context.Background())
	if got.Type != "" || !strings.HasPrefix(got.Error, "$"+EnvFile+": ") {
		t.Errorf("ADC = %+v", got)
	}
	rows := got.Rows(false)
	if len(rows) != 2 || rows[0][1] != "(not signed in)" || rows[1][1] != LoginHint {
		t.Errorf("rows = %q", rows)
	}
}

func TestGcloud(t *testing.T) {
	l := Lookup{GcloudBin: testutil.FakeCLI(t, "gcloud")}
	t.Setenv("FAKE_GCLOUD_CONFIG", `{"core": {"account": "me@home.example", "project": "my-proj"}}`)
	got := l.Gcloud(context.Background())
	if want := (CLI{Account: "me@home.example", Project: "my-proj"}); got != want {
		t.Errorf("Gcloud = %+v, want %+v", got, want)
	}
	// Another account than xf's is said, in whatever case.
	wantRows := [][3]string{{"gcloud", "me@home.example", "project my-proj"}, {"", "differs from the account xf uses", ""}}
	if rows := got.Rows(ADC{Account: "me@work.example"}); !reflect.DeepEqual(rows, wantRows) {
		t.Errorf("rows = %q, want %q", rows, wantRows)
	}
	if rows := got.Rows(ADC{Account: "Me@Home.example"}); len(rows) != 1 {
		t.Errorf("rows for the same account = %q", rows)
	}

	// A gcloud with nothing set.
	t.Setenv("FAKE_GCLOUD_CONFIG", `{}`)
	if rows := l.Gcloud(context.Background()).Rows(ADC{Account: "me@work.example"}); !reflect.DeepEqual(rows, [][3]string{{"gcloud", "(no account)", ""}}) {
		t.Errorf("rows with nothing set = %q", rows)
	}

	t.Setenv("FAKE_GCLOUD_ERROR", "ERROR: (gcloud.config.list) broken\nmore")
	if got := l.Gcloud(context.Background()); got.Error != "broken" {
		t.Errorf("failing gcloud = %+v", got)
	}

	missing := Lookup{GcloudBin: filepath.Join(t.TempDir(), "no-gcloud")}.Gcloud(context.Background())
	if rows := missing.Rows(ADC{}); missing.Error == "" || rows[0][1] != "(unknown)" {
		t.Errorf("missing gcloud = %+v, rows %q", missing, rows)
	}
	t.Setenv("PATH", t.TempDir())
	if got := (Lookup{}).Gcloud(context.Background()); got.Error != "gcloud not found on PATH" {
		t.Errorf("gcloud off PATH = %+v", got)
	}
}

func TestADCNeedsReauthentication(t *testing.T) {
	srv := fakeGoogle(t, "")
	writeCreds(t, map[string]string{
		"type": "authorized_user", "client_id": "id", "client_secret": "secret", "refresh_token": "rapt",
		"token_uri": srv.URL + "/token",
	})
	got := Lookup{TokenInfoURL: srv.URL + "/tokeninfo"}.ADC(context.Background())
	if !got.Reauth || got.Error != "reauth related error (invalid_rapt)" {
		t.Errorf("ADC = %+v", got)
	}
	rows := got.Rows(false)
	if rows[0] != [3]string{"xf uses", "(reauthentication required)", "reauth related error (invalid_rapt)"} || rows[1][1] != LoginHint {
		t.Errorf("rows = %q", rows)
	}
	if !Warn(rows[0]) || !Warn(rows[1]) || Warn(rows[2]) {
		t.Errorf("amber rows of %q are wrong", rows)
	}
	want := []Alert{{Message: "xf's Google sign-in needs reauthentication", Command: LoginCommand,
		Note: "xf's BigQuery, lineage and Dataform API calls fail until then"}}
	if alerts := Alerts(got, CLI{Account: "me@home.example"}); !reflect.DeepEqual(alerts, want) {
		t.Errorf("alerts = %+v, want %+v", alerts, want)
	}
}

func TestGcloudNeedsReauthentication(t *testing.T) {
	l := Lookup{GcloudBin: testutil.FakeCLI(t, "gcloud")}
	t.Setenv("FAKE_GCLOUD_CONFIG", `{"core": {"account": "me@home.example", "project": "my-proj"}}`)
	t.Setenv("FAKE_GCLOUD_TOKEN_ERROR", "ERROR: (gcloud.auth.print-access-token) There was a problem refreshing your current auth tokens: Reauthentication failed. cannot prompt during non-interactive execution.\nPlease run:")
	got := l.Gcloud(context.Background())
	if !got.Reauth || got.SignInError != "There was a problem refreshing your current auth tokens: Reauthentication failed. cannot prompt during non-interactive execution." {
		t.Errorf("Gcloud = %+v", got)
	}
	adc := ADC{Account: "me@home.example", Type: "authorized_user"}
	wantRows := [][3]string{
		{"gcloud", "me@home.example (reauthentication required)", "project my-proj"},
		{"", CLILoginHint, "xf's own calls still work; bq and gcloud commands don't"},
	}
	rows := got.Rows(adc)
	if !reflect.DeepEqual(rows, wantRows) {
		t.Errorf("rows = %q, want %q", rows, wantRows)
	}
	if !Warn(rows[0]) || !Warn(rows[1]) {
		t.Errorf("rows %q aren't amber", rows)
	}
	want := []Alert{{Message: "gcloud needs reauthentication", Command: CLILoginCommand, Note: "xf's own calls still work; bq and gcloud commands don't"}}
	if alerts := Alerts(adc, got); !reflect.DeepEqual(alerts, want) {
		t.Errorf("alerts = %+v, want %+v", alerts, want)
	}

	// Both failing: the gcloud one doesn't say xf's calls work.
	alerts := Alerts(ADC{Error: "no Application Default Credentials"}, got)
	if len(alerts) != 2 || alerts[0].Command != LoginCommand || alerts[1].Note != "bq and gcloud commands fail until then" {
		t.Errorf("alerts with no ADC = %+v", alerts)
	}

	// Another failure is said, but isn't worth a banner.
	t.Setenv("FAKE_GCLOUD_TOKEN_ERROR", "ERROR: (gcloud.auth.print-access-token) Your current active account does not have any valid credentials")
	got = l.Gcloud(context.Background())
	if got.Reauth || got.SignInError != "Your current active account does not have any valid credentials" {
		t.Errorf("Gcloud = %+v", got)
	}
	if rows := got.Rows(adc); rows[0][1] != "me@home.example (sign-in failed)" || rows[2][2] != got.SignInError {
		t.Errorf("rows = %q", rows)
	}
	if alerts := Alerts(adc, got); len(alerts) != 0 {
		t.Errorf("alerts = %+v", alerts)
	}

	// No account: no token asked for, and nothing to warn of.
	t.Setenv("FAKE_GCLOUD_CONFIG", `{}`)
	if got := l.Gcloud(context.Background()); got.SignInError != "" || len(Alerts(adc, got)) != 0 {
		t.Errorf("Gcloud with no account = %+v", got)
	}
}
