package databricks

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Setup is what says where a Databricks project connects and as whom
// (ADR 0042): xf's settings, the dbt target xf compiles with, and the
// environment.
type Setup struct {
	// Warehouse is the databricksWarehouse setting: a SQL warehouse's ID or
	// HTTP path; Profile is databricksProfile, a ~/.databrickscfg profile.
	Warehouse, Profile string
	// Host, HTTPPath, Token and Catalog are the dbt target's, "" where it
	// says nothing, or says it in Jinja xf doesn't read.
	Host, HTTPPath, Token, Catalog string
	// Getenv reads the environment; nil is os.Getenv.
	Getenv func(string) string
	// ConfigFile is the Databricks CLI's config; "" is
	// $DATABRICKS_CONFIG_FILE, else ~/.databrickscfg.
	ConfigFile string
	// CLI runs the Databricks CLI with args and returns its stdout; nil
	// runs databricks from PATH.
	CLI func(ctx context.Context, args ...string) ([]byte, error)
}

// Connection is where a Databricks project connects, and as whom.
type Connection struct {
	Config
	// Profile is the ~/.databrickscfg profile the token or host comes
	// from, "" for none.
	Profile string
	// TokenFrom says where the token comes from: $DATABRICKS_TOKEN,
	// profiles.yml, ~/.databrickscfg or the Databricks CLI.
	TokenFrom string
}

// Resolve works out the connection s describes. The workspace is the dbt
// target's host, else the profile's, else $DATABRICKS_HOST. The warehouse
// is the setting's, else the dbt target's http_path. The token is
// $DATABRICKS_TOKEN, else the dbt target's, else the profile's, else what
// `databricks auth token` gives for the profile, which refreshes an OAuth
// login. The profile is the setting's, else the one whose host is the
// workspace's, else DEFAULT.
func Resolve(s Setup) Connection {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	file := cmp.Or(s.ConfigFile, getenv("DATABRICKS_CONFIG_FILE"))
	if file == "" {
		if home, err := os.UserHomeDir(); err == nil {
			file = filepath.Join(home, ".databrickscfg")
		}
	}
	profiles := readProfiles(file)
	host := plain(s.Host)
	name := s.Profile
	// The profile is the one for the target's host, else $DATABRICKS_HOST's:
	// a target that gives its host in Jinja leaves only that.
	if want := cmp.Or(host, getenv("DATABRICKS_HOST")); name == "" && want != "" {
		for _, p := range profiles {
			if sameHost(p.values["host"], want) {
				name = p.name
				break
			}
		}
	}
	if name == "" && profiles.find("DEFAULT") != nil {
		name = "DEFAULT"
	}
	prof := profiles.find(name)
	if prof == nil {
		name = ""
		prof = &cfgProfile{values: map[string]string{}}
	}
	host = cmp.Or(host, prof.values["host"], getenv("DATABRICKS_HOST"))
	c := Connection{Config: Config{Host: NormalizeHost(host), Warehouse: WarehouseID(cmp.Or(s.Warehouse, plain(s.HTTPPath))),
		Catalog: plain(s.Catalog)}, Profile: name}
	switch {
	case getenv("DATABRICKS_TOKEN") != "":
		c.Token, c.TokenFrom = static(getenv("DATABRICKS_TOKEN")), "$DATABRICKS_TOKEN"
	case plain(s.Token) != "":
		c.Token, c.TokenFrom = static(plain(s.Token)), "profiles.yml"
	case prof.values["token"] != "":
		c.Token, c.TokenFrom = static(prof.values["token"]), "~/.databrickscfg"
	default:
		// The CLI is asked by profile when the profile names the host,
		// else by host: an empty [DEFAULT] has none to sign in to.
		args := []string{"auth", "token", "--host", c.Host}
		if name != "" && (prof.values["host"] != "" || c.Host == "") {
			args = []string{"auth", "token", "--profile", name}
		}
		c.TokenFrom = "databricks " + strings.Join(args, " ")
		c.Token = cliToken(s.CLI, args)
	}
	return c
}

// WarehouseID is the ID of the SQL warehouse s names: an ID, or an HTTP
// path /sql/1.0/warehouses/<id>; "" for a cluster's path, which is no
// SQL warehouse.
func WarehouseID(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/warehouses/"); i >= 0 {
		return strings.Trim(s[i+len("/warehouses/"):], "/")
	}
	if strings.Contains(s, "/") {
		return ""
	}
	return s
}

// plain is v unless it is Jinja xf didn't read.
func plain(v string) string {
	if strings.Contains(v, "{{") || strings.Contains(v, "{%") {
		return ""
	}
	return strings.TrimSpace(v)
}

func sameHost(a, b string) bool {
	return a != "" && strings.EqualFold(NormalizeHost(a), NormalizeHost(b))
}

func static(tok string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return tok, nil }
}

// cliToken asks the Databricks CLI for a token, keeping it until a minute
// before it expires.
func cliToken(run func(ctx context.Context, args ...string) ([]byte, error), args []string) func(context.Context) (string, error) {
	if run == nil {
		run = runCLI
	}
	var mu sync.Mutex
	var tok string
	var until time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if tok != "" && time.Now().Before(until) {
			return tok, nil
		}
		out, err := run(ctx, args...)
		if err != nil {
			return "", authErr(err)
		}
		var t struct {
			AccessToken string `json:"access_token"`
			Expiry      string `json:"expiry"`
		}
		if err := json.Unmarshal(out, &t); err != nil || t.AccessToken == "" {
			return "", authErr(fmt.Errorf("databricks %s gave no token", strings.Join(args, " ")))
		}
		tok, until = t.AccessToken, time.Now().Add(5*time.Minute)
		if exp, err := time.Parse(time.RFC3339Nano, t.Expiry); err == nil {
			until = exp.Add(-time.Minute)
		}
		return tok, nil
	}
}

// errNoCLI is why there is no token when nothing gives one.
var errNoCLI = errors.New("no token: set DATABRICKS_TOKEN, or install the Databricks CLI and run `databricks auth login`")

func runCLI(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("databricks")
	if err != nil {
		return nil, errNoCLI
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("databricks %s: %s", strings.Join(args, " "), cmp.Or(msg, err.Error()))
	}
	return out, nil
}

// cfgProfile is a section of ~/.databrickscfg.
type cfgProfile struct {
	name   string
	values map[string]string
}

type cfgProfiles []cfgProfile

func (ps cfgProfiles) find(name string) *cfgProfile {
	for i := range ps {
		if ps[i].name == name {
			return &ps[i]
		}
	}
	return nil
}

// readProfiles reads the profiles of an INI file; none when it can't.
func readProfiles(file string) cfgProfiles {
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out cfgProfiles
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case l == "" || l[0] == '#' || l[0] == ';':
		case l[0] == '[' && strings.HasSuffix(l, "]"):
			out = append(out, cfgProfile{name: strings.TrimSpace(l[1 : len(l)-1]), values: map[string]string{}})
		case len(out) > 0:
			if k, v, ok := strings.Cut(l, "="); ok {
				out[len(out)-1].values[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}
