// Package gcpauth says which Google accounts are in use, for debugging:
// the Application Default Credentials xf's own Google calls sign in with,
// and the gcloud CLI's active account, which can be another one.
package gcpauth

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// LoginCommand signs xf's credentials in again, and CLILoginCommand the
// gcloud CLI's; the hints say to run them.
const (
	LoginCommand    = "gcloud auth application-default login"
	CLILoginCommand = "gcloud auth login"
	LoginHint       = "run `" + LoginCommand + "`"
	CLILoginHint    = "run `" + CLILoginCommand + "`"
)

// reauthText is the value that says an account must sign in again.
const reauthText = "(reauthentication required)"

// MetadataServer is the Type of credentials that come from no file but
// from the machine, as in Cloud Shell or on a VM. The other types are the
// ones Google's credential files name, e.g. authorized_user.
const MetadataServer = "metadata_server"

// EnvFile is the variable that names a credential file to use in place of
// the gcloud CLI's.
const EnvFile = "GOOGLE_APPLICATION_CREDENTIALS"

const (
	scope        = "https://www.googleapis.com/auth/cloud-platform"
	tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"
)

// ADC is the Application Default Credentials: what xf's BigQuery, lineage
// and Dataform API calls sign in with.
type ADC struct {
	// Account is the email they sign in as, "" when Google doesn't say.
	Account string `json:"account"`
	// Type is the credential file's type, or MetadataServer.
	Type string `json:"type"`
	// Source is the credential file and SourceFrom what chose it:
	// "$GOOGLE_APPLICATION_CREDENTIALS" or "gcloud's default file".
	Source     string `json:"source"`
	SourceFrom string `json:"sourceFrom"`
	// QuotaProject is the project xf's calls are counted against.
	QuotaProject string `json:"quotaProject"`
	// Error is why there are no credentials, or why they don't work, and
	// Reauth whether that is because they must sign in again.
	Error  string `json:"error,omitempty"`
	Reauth bool   `json:"reauth,omitempty"`
}

// CLI is the gcloud CLI's active account and project.
type CLI struct {
	Account string `json:"account"`
	Project string `json:"project"`
	// Error is why gcloud didn't say.
	Error string `json:"error,omitempty"`
	// SignInError is why the account didn't get a token, and Reauth
	// whether that is because it must sign in again. bq and gcloud's own
	// commands use this account; xf's calls use the ADC.
	SignInError string `json:"signInError,omitempty"`
	Reauth      bool   `json:"reauth,omitempty"`
}

// Lookup finds the accounts. The zero value asks Google and runs gcloud.
type Lookup struct {
	TokenInfoURL string       // "" for Google's
	GcloudBin    string       // the gcloud binary, "" for gcloud on PATH
	HTTP         *http.Client // nil for http.DefaultClient
}

// ADC reads the Application Default Credentials and signs in with them,
// which says whether they still work, and as whom.
func (l Lookup) ADC(ctx context.Context) ADC {
	var a ADC
	creds, err := google.FindDefaultCredentials(ctx, scope)
	if err != nil {
		a.Error = "no Application Default Credentials"
		if os.Getenv(EnvFile) != "" {
			a.Error = fmt.Sprintf("$%s: %v", EnvFile, err)
		}
		return a
	}
	var file struct {
		Type             string `json:"type"`
		ClientEmail      string `json:"client_email"`
		QuotaProject     string `json:"quota_project_id"`
		ImpersonationURL string `json:"service_account_impersonation_url"`
	}
	_ = json.Unmarshal(creds.JSON, &file) // metadata-server credentials have no JSON
	a.Type, a.QuotaProject = file.Type, file.QuotaProject
	if len(creds.JSON) == 0 {
		a.Type = MetadataServer
	} else {
		a.Source, a.SourceFrom = sourceFile()
	}
	a.Account = cmp.Or(file.ClientEmail, impersonated(file.ImpersonationURL))

	tok, err := creds.TokenSource.Token()
	if err != nil {
		a.Error = tokenError(ctx, err)
		a.Reauth = IsReauth(a.Error)
		return a
	}
	if a.Account == "" {
		// Best effort: a login without the email scope has no email.
		a.Account, _ = l.email(ctx, tok.AccessToken)
	}
	return a
}

// sourceFile is the credential file the Google library reads, by its rules.
func sourceFile() (path, from string) {
	if f := os.Getenv(EnvFile); f != "" {
		return f, "$" + EnvFile
	}
	const name = "application_default_credentials.json"
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "gcloud", name), "gcloud's default file"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcloud", name), "gcloud's default file"
}

var impersonationRE = regexp.MustCompile(`/serviceAccounts/([^/:]+):`)

// impersonated is the service account an impersonation URL names.
func impersonated(u string) string {
	if m := impersonationRE.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

func tokenError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "timed out signing in"
	}
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		switch {
		case re.ErrorDescription != "":
			return re.ErrorDescription
		case re.ErrorCode != "":
			return re.ErrorCode
		}
		return "token refresh failed"
	}
	return err.Error()
}

// email asks Google whose access token it is. The token goes in the
// request's body, not its URL, so that no log holds it.
func (l Lookup) email(ctx context.Context, token string) (string, error) {
	body := strings.NewReader(url.Values{"access_token": {token}}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmp.Or(l.TokenInfoURL, tokenInfoURL), body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := l.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", err
	}
	return info.Email, nil
}

// Gcloud asks the gcloud CLI for its active account and project, and then
// for a token, which says whether the account still signs in: gcloud
// config list is happy with an account that needs reauthentication.
func (l Lookup) Gcloud(ctx context.Context) CLI {
	bin := cmp.Or(l.GcloudBin, "gcloud")
	out, err := cmdrun.Command(ctx, bin, "config", "list", "--format=json").Output()
	if err != nil {
		return CLI{Error: gcloudError(ctx, err)}
	}
	var cfg struct {
		Core struct {
			Account string `json:"account"`
			Project string `json:"project"`
		} `json:"core"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return CLI{Error: "gcloud config list printed no JSON"}
	}
	c := CLI{Account: cfg.Core.Account, Project: cfg.Core.Project}
	if c.Account == "" {
		return c
	}
	// The token goes nowhere (Output only keeps stderr for the error).
	// With no terminal to prompt on, gcloud says it needs
	// reauthentication rather than asking.
	if _, err := cmdrun.Command(ctx, bin, "auth", "print-access-token").Output(); err != nil {
		c.SignInError = gcloudError(ctx, err)
		c.Reauth = IsReauth(c.SignInError)
	}
	return c
}

// gcloudError says why a gcloud command failed: its first line on stderr
// when it said one.
func gcloudError(ctx context.Context, err error) string {
	switch exitErr, exited := errors.AsType[*exec.ExitError](err); {
	case errors.Is(err, exec.ErrNotFound):
		return "gcloud not found on PATH"
	case ctx.Err() != nil:
		return "gcloud timed out"
	case exited:
		if l := cmdrun.FirstLine(cmdrun.StripANSI(string(exitErr.Stderr))); l != "" {
			return strings.TrimSpace(gcloudPrefix.ReplaceAllString(l, ""))
		}
	}
	return err.Error()
}

// gcloudPrefix is how gcloud starts an error: "ERROR: (gcloud.auth.x) ".
var gcloudPrefix = regexp.MustCompile(`^ERROR: \([^)]*\) `)

// IsReauth reports whether a sign-in error, gcloud's or Google's token
// endpoint's, asks to sign in again: a reauthentication (RAPT) policy,
// as Google Workspace accounts often have, ends a login after some hours.
func IsReauth(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "reauth") || strings.Contains(msg, "invalid_rapt")
}

// TypeText says a credential type in words.
func TypeText(t string) string {
	switch t {
	case "authorized_user":
		return "user login"
	case "service_account":
		return "service account key"
	case "impersonated_service_account":
		return "impersonated service account"
	case "external_account":
		return "workload identity federation"
	case "external_account_authorized_user":
		return "workforce identity login"
	case MetadataServer:
		return "metadata server"
	}
	return t
}

// Rows are a's rows in the project info and in xf config --auth: a label,
// a value and a note each. dbt adds that dbt itself signs in its own way.
func (a ADC) Rows(dbt bool) [][3]string {
	if a.Error != "" && a.Type == "" {
		return [][3]string{{"xf uses", "(not signed in)", a.Error}, {"", LoginHint, ""}}
	}
	account, note := cmp.Or(a.Account, "(unknown account)"), ""
	if a.Error != "" {
		account, note = "(sign-in failed)", a.Error
		if a.Reauth {
			account = reauthText
		}
		if a.Account != "" {
			account = a.Account + " " + account
		}
	}
	rows := [][3]string{{"xf uses", account, note}}
	if a.Error != "" {
		rows = append(rows, [3]string{"", LoginHint, ""})
	}
	typeNote := ""
	if dbt {
		typeNote = "for xf's own calls; dbt signs in as profiles.yml says"
	}
	return append(rows,
		[3]string{"", TypeText(a.Type), typeNote},
		[3]string{"", a.Source, a.SourceFrom},
		[3]string{"quota", cmp.Or(a.QuotaProject, "(no quota project)"), ""},
	)
}

// Rows are c's rows, beside those of adc: they say when gcloud's account
// isn't the one xf uses, and when it can't sign in.
func (c CLI) Rows(adc ADC) [][3]string {
	if c.Error != "" {
		return [][3]string{{"gcloud", "(unknown)", c.Error}}
	}
	note := ""
	if c.Project != "" {
		note = "project " + c.Project
	}
	account := cmp.Or(c.Account, "(no account)")
	switch {
	case c.Reauth:
		account += " " + reauthText
	case c.SignInError != "":
		account += " (sign-in failed)"
	}
	rows := [][3]string{{"gcloud", account, note}}
	if c.SignInError != "" {
		rows = append(rows, [3]string{"", CLILoginHint, c.otherTools(adc)})
		if !c.Reauth {
			rows = append(rows, [3]string{"", "", c.SignInError})
		}
	}
	if c.Account != "" && adc.Account != "" && !strings.EqualFold(c.Account, adc.Account) {
		rows = append(rows, [3]string{"", "differs from the account xf uses", ""})
	}
	return rows
}

// otherTools says what c failing to sign in stops, beside adc.
func (c CLI) otherTools(adc ADC) string {
	if adc.Error == "" && adc.Type != "" {
		return "xf's own calls still work; bq and gcloud commands don't"
	}
	return "bq and gcloud commands fail until then"
}

// Warn reports whether a row of Rows is a problem to show in amber.
func Warn(row [3]string) bool {
	v := row[1]
	return v == LoginHint || v == CLILoginHint || v == "(not signed in)" ||
		strings.HasSuffix(v, reauthText) || strings.HasSuffix(v, "(sign-in failed)")
}

// Alert is a sign-in problem worth a banner: what is wrong, the command
// that mends it and what it stops.
type Alert struct {
	Message string `json:"message"`
	Command string `json:"command"`
	Note    string `json:"note,omitempty"`
}

// Alerts are adc's and c's problems worth a banner: xf's own credentials
// missing or failing, and the gcloud CLI needing reauthentication. gcloud
// missing, or failing some other way, is left to the settings page: xf
// doesn't need it.
func Alerts(adc ADC, c CLI) []Alert {
	var out []Alert
	const xfCalls = "xf's BigQuery, lineage and Dataform API calls fail until then"
	switch {
	case adc.Reauth:
		out = append(out, Alert{Message: "xf's Google sign-in needs reauthentication", Command: LoginCommand, Note: xfCalls})
	case adc.Error != "" && adc.Type == "":
		out = append(out, Alert{Message: "xf has no Google sign-in (Application Default Credentials)", Command: LoginCommand, Note: xfCalls})
	case adc.Error != "":
		out = append(out, Alert{Message: "xf's Google sign-in failed: " + adc.Error, Command: LoginCommand, Note: xfCalls})
	}
	if c.Reauth {
		out = append(out, Alert{Message: "gcloud needs reauthentication", Command: CLILoginCommand, Note: c.otherTools(adc)})
	}
	return out
}
