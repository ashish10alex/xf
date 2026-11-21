package snowflake

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Setup is what says where a Snowflake project connects and as whom (ADR 0044):
// xf's settings, the dbt target xf compiles with, and the environment.
type Setup struct {
	// Warehouse is the snowflakeWarehouse setting; Connection is
	// snowflakeConnection, a section in ~/.snowflake/connections.toml.
	Warehouse, Connection string
	// Fields from the dbt target, "" where unset or Jinja xf doesn't read.
	Account, Host, User, Role, TargetWarehouse, Database, Schema string
	Authenticator, Token, Password                               string
	PrivateKeyPath, PrivateKey, PrivateKeyPassphrase             string
	// Getenv reads the environment; nil is os.Getenv.
	Getenv func(string) string
	// ConnectionsFile overrides ~/.snowflake/connections.toml (for tests).
	ConnectionsFile string
}

// Connection is where a Snowflake project connects, and as whom.
type Connection struct {
	Config
	// ConnectionName is the ~/.snowflake/connections.toml section used, "" for none.
	ConnectionName string
	// TokenFrom says where credentials came from, for diagnostics.
	TokenFrom string
}

// ConnectionInfo is a named connection section in ~/.snowflake/connections.toml.
type ConnectionInfo struct {
	Name    string
	Account string
	Host    string
}

// connectionsFile locates connections.toml: explicit file, else
// $SNOWFLAKE_CONNECTIONS_FILE, else $SNOWFLAKE_HOME/connections.toml, else
// ~/.snowflake/connections.toml (and on macOS, ~/Library/Application Support/snowflake/connections.toml
// when ~/.snowflake/connections.toml does not exist).
func connectionsFile(file string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if file != "" {
		return file
	}
	if f := getenv("SNOWFLAKE_CONNECTIONS_FILE"); f != "" {
		return f
	}
	if dir := getenv("SNOWFLAKE_HOME"); dir != "" {
		return filepath.Join(dir, "connections.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		dot := filepath.Join(home, ".snowflake", "connections.toml")
		if runtime.GOOS == "darwin" {
			if _, err := os.Stat(dot); os.IsNotExist(err) {
				mac := filepath.Join(home, "Library", "Application Support", "snowflake", "connections.toml")
				if _, err := os.Stat(mac); err == nil {
					return mac
				}
				// Also fall back to macOS directory if config.toml lives there.
				macCfg := filepath.Join(home, "Library", "Application Support", "snowflake", "config.toml")
				if _, err := os.Stat(macCfg); err == nil {
					return mac
				}
			}
		}
		return dot
	}
	return ""
}

// defaultConnectionName reads default_connection_name from config.toml beside
// connections.toml, or $SNOWFLAKE_DEFAULT_CONNECTION_NAME.
func defaultConnectionName(connFile string, getenv func(string) string) string {
	if n := strings.TrimSpace(getenv("SNOWFLAKE_DEFAULT_CONNECTION_NAME")); n != "" {
		return n
	}
	if connFile == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(connFile), "config.toml"))
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := stripTOMLComment(strings.TrimSpace(sc.Text()))
		if l == "" || l[0] == '[' {
			if len(l) > 0 && l[0] == '[' {
				break // top-level keys come before sections
			}
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && strings.EqualFold(strings.TrimSpace(k), "default_connection_name") {
			return unquoteTOML(strings.TrimSpace(v))
		}
	}
	return ""
}

// ListConnections returns the connections in connections.toml (and any
// [connections.<name>] sections in config.toml beside it) that name an account
// or host, in file order.
func ListConnections(file string, getenv func(string) string) []ConnectionInfo {
	var out []ConnectionInfo
	for _, s := range loadConnectionSections(connectionsFile(file, getenv)) {
		acct := s.values["account"]
		host := NormalizeHost(s.values["host"])
		if acct == "" && host == "" {
			continue
		}
		if host == "" {
			host = AccountHost(acct)
		}
		out = append(out, ConnectionInfo{Name: s.name, Account: acct, Host: host})
	}
	return out
}

// loadConnectionSections reads connections.toml and merges any
// [connections.<name>] sections from config.toml beside it (connections.toml
// takes precedence on name collisions).
func loadConnectionSections(connFile string) tomlSections {
	out := readTOMLSections(connFile, false)
	if connFile == "" {
		return out
	}
	cfgFile := filepath.Join(filepath.Dir(connFile), "config.toml")
	for _, sec := range readTOMLSections(cfgFile, true) {
		if out.find(sec.name) == nil {
			out = append(out, sec)
		}
	}
	return out
}

// Resolve works out the Snowflake connection s describes.
// Account comes from the dbt target, else the connection in connections.toml,
// else $SNOWFLAKE_ACCOUNT. Warehouse comes from the snowflakeWarehouse setting,
// else the dbt target's warehouse, else the connection's warehouse, else
// $SNOWFLAKE_WAREHOUSE. Auth resolves PAT ($SNOWFLAKE_TOKEN, target/connection
// token), OAuth ($SNOWFLAKE_OAUTH_TOKEN or authenticator=oauth), or key-pair
// JWT ($SNOWFLAKE_PRIVATE_KEY[_PATH], target/connection private_key[_path]).
func Resolve(s Setup) Connection {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	connFile := connectionsFile(s.ConnectionsFile, getenv)
	sections := loadConnectionSections(connFile)

	targetAcct := plain(s.Account)
	name := strings.TrimSpace(s.Connection)
	want := cmp.Or(targetAcct, getenv("SNOWFLAKE_ACCOUNT"))
	if name == "" && want != "" {
		for _, sec := range sections {
			if sameAccount(sec.values["account"], want) {
				name = sec.name
				break
			}
		}
	}
	if name == "" {
		var cand *tomlSection
		if def := defaultConnectionName(connFile, getenv); def != "" {
			cand = sections.find(def)
		} else {
			cand = sections.find("default")
		}
		// Never send a default connection's credentials to a different account
		// named by the dbt target or $SNOWFLAKE_ACCOUNT.
		if cand != nil && (want == "" || cand.values["account"] == "" || sameAccount(cand.values["account"], want)) {
			name = cand.name
		}
	}
	sec := sections.find(name)
	if sec == nil {
		name = ""
		sec = &tomlSection{values: map[string]string{}}
	}

	account := cmp.Or(targetAcct, sec.values["account"], getenv("SNOWFLAKE_ACCOUNT"))
	host := cmp.Or(plain(s.Host), sec.values["host"], getenv("SNOWFLAKE_HOST"))
	user := cmp.Or(plain(s.User), sec.values["user"], getenv("SNOWFLAKE_USER"))
	role := cmp.Or(plain(s.Role), sec.values["role"], getenv("SNOWFLAKE_ROLE"))
	database := cmp.Or(plain(s.Database), sec.values["database"], getenv("SNOWFLAKE_DATABASE"))
	schema := cmp.Or(plain(s.Schema), sec.values["schema"], getenv("SNOWFLAKE_SCHEMA"))
	wh := cmp.Or(strings.TrimSpace(s.Warehouse), plain(s.TargetWarehouse), sec.values["warehouse"], getenv("SNOWFLAKE_WAREHOUSE"))
	auth := strings.ToLower(cmp.Or(plain(s.Authenticator), sec.values["authenticator"], getenv("SNOWFLAKE_AUTHENTICATOR")))

	c := Connection{
		Config: Config{
			Account:   account,
			Host:      NormalizeHost(host),
			Warehouse: wh,
			Database:  database,
			Schema:    schema,
			Role:      role,
		},
		ConnectionName: name,
	}
	if c.Host == "" && c.Account != "" {
		c.Host = AccountHost(c.Account)
	}

	targetToken := plain(s.Token)
	targetPass := plain(s.Password)
	targetKey := plain(s.PrivateKey)
	targetKeyPath := plain(s.PrivateKeyPath)
	targetKeyPass := plain(s.PrivateKeyPassphrase)

	secToken := sec.values["token"]
	secPass := sec.values["password"]
	secKey := sec.values["private_key"]
	secKeyPath := cmp.Or(sec.values["private_key_path"], sec.values["private_key_file"])
	secKeyPass := sec.values["private_key_passphrase"]

	envToken := getenv("SNOWFLAKE_TOKEN")
	envOAuth := getenv("SNOWFLAKE_OAUTH_TOKEN")
	envKey := getenv("SNOWFLAKE_PRIVATE_KEY")
	envKeyPath := getenv("SNOWFLAKE_PRIVATE_KEY_PATH")
	envKeyPass := getenv("SNOWFLAKE_PRIVATE_KEY_PASSPHRASE")

	switch {
	case envToken != "":
		c.Token, c.TokenFrom = staticToken(envToken, TokenPAT), "$SNOWFLAKE_TOKEN"
	case envOAuth != "":
		c.Token, c.TokenFrom = staticToken(envOAuth, TokenOAuth), "$SNOWFLAKE_OAUTH_TOKEN"
	case envKey != "" || envKeyPath != "":
		c.Token = keyPairToken(account, user, envKey, envKeyPath, envKeyPass)
		c.TokenFrom = "key-pair (environment)"
	case targetToken != "":
		tt := TokenPAT
		if auth == "oauth" {
			tt = TokenOAuth
		}
		c.Token, c.TokenFrom = staticToken(targetToken, tt), "profiles.yml"
	case targetPass != "" && auth == "programmatic_access_token":
		c.Token, c.TokenFrom = staticToken(targetPass, TokenPAT), "profiles.yml"
	case targetKey != "" || targetKeyPath != "":
		c.Token = keyPairToken(account, user, targetKey, targetKeyPath, targetKeyPass)
		c.TokenFrom = "profiles.yml (key-pair)"
	case secToken != "":
		tt := TokenPAT
		if auth == "oauth" {
			tt = TokenOAuth
		}
		c.Token, c.TokenFrom = staticToken(secToken, tt), "~/.snowflake/connections.toml"
	case secPass != "" && auth == "programmatic_access_token":
		c.Token, c.TokenFrom = staticToken(secPass, TokenPAT), "~/.snowflake/connections.toml"
	case secKey != "" || secKeyPath != "":
		c.Token = keyPairToken(account, user, secKey, secKeyPath, secKeyPass)
		c.TokenFrom = "~/.snowflake/connections.toml (key-pair)"
	}
	return c
}

func staticToken(v string, typ TokenType) func(context.Context) (Token, error) {
	return func(context.Context) (Token, error) {
		return Token{Value: v, Type: typ}, nil
	}
}

func plain(v string) string {
	if strings.Contains(v, "{{") || strings.Contains(v, "{%") {
		return ""
	}
	return strings.TrimSpace(v)
}

func sameAccount(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(AccountHost(a), AccountHost(b))
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// tomlSection is a [name] section of ~/.snowflake/connections.toml.
type tomlSection struct {
	name   string
	values map[string]string
}

type tomlSections []tomlSection

func (ss tomlSections) find(name string) *tomlSection {
	for i := range ss {
		if ss[i].name == name {
			return &ss[i]
		}
	}
	return nil
}

// readTOMLSections reads the [section] key = value pairs of a flat TOML file
// (such as ~/.snowflake/connections.toml). When onlyPrefixed is true (for
// config.toml), only [connections.<name>] sections are kept. A bare
// [connections] table header is ignored.
func readTOMLSections(file string, onlyPrefixed bool) tomlSections {
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out tomlSections
	inSection := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := stripTOMLComment(strings.TrimSpace(sc.Text()))
		switch {
		case l == "":
		case l[0] == '[' && strings.HasSuffix(l, "]"):
			inSection = false
			raw := strings.TrimSpace(l[1 : len(l)-1])
			if strings.EqualFold(raw, "connections") {
				continue
			}
			var name string
			if rest, ok := strings.CutPrefix(raw, "connections."); ok {
				name = unquoteTOML(strings.TrimSpace(rest))
			} else if !onlyPrefixed {
				name = unquoteTOML(raw)
			}
			if name != "" {
				out = append(out, tomlSection{name: name, values: map[string]string{}})
				inSection = true
			}
		case inSection && len(out) > 0:
			if k, v, ok := strings.Cut(l, "="); ok {
				key := strings.ToLower(unquoteTOML(strings.TrimSpace(k)))
				val := unquoteTOML(strings.TrimSpace(v))
				if key != "" {
					out[len(out)-1].values[key] = val
				}
			}
		}
	}
	return out
}

func stripTOMLComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

func unquoteTOML(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		q := v[0]
		inner := v[1 : len(v)-1]
		if q == '"' {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t").Replace(inner)
		}
		return inner
	}
	return v
}
