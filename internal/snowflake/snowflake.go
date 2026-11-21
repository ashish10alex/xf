// Package snowflake calls a Snowflake account's SQL REST API v2
// (/api/v2/statements), with no SDK (ADR 0044). A dbt project on the
// dbt-snowflake adapter builds there.
//
// A virtual warehouse bills while it runs, so xf never auto-checks Snowflake
// models on load or save. Only explicit checks, scratch queries, and
// ACCOUNT_USAGE queries (lineage and cost) execute on the virtual warehouse
// and may resume a suspended one. Metadata commands (SHOW, DESCRIBE) run in
// Snowflake's Cloud Services layer and wake nothing.
//
// The Client is xf's warehouse.Warehouse, warehouse.Waker and
// warehouse.ProjectLister: cmd/xf makes it, and nothing else imports this
// package.
package snowflake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// TokenType names the X-Snowflake-Authorization-Token-Type header for SQL API
// v2 authentication.
type TokenType string

const (
	TokenPAT        TokenType = "PROGRAMMATIC_ACCESS_TOKEN"
	TokenKeyPairJWT TokenType = "KEYPAIR_JWT"
	TokenOAuth      TokenType = "OAUTH"
)

// Token is an authenticated bearer token and its Snowflake token type.
type Token struct {
	Value string
	Type  TokenType
}

// Client makes authenticated calls to a Snowflake account's SQL API v2.
type Client struct {
	http *http.Client
	// account is the Snowflake account identifier (e.g. org-acct or xy12345.us-east-1).
	account string
	// host is the normalized base URL, e.g. https://org-acct.snowflakecomputing.com.
	host string
	// token gives the bearer token and token type of each call.
	token func(ctx context.Context) (Token, error)
	// database, schema and role are the defaults sent with statements.
	database string
	schema   string
	role     string

	mu sync.Mutex
	// warehouse is the virtual warehouse statements run on, "" until chosen.
	warehouse string
	// poll is how long a running statement is waited on between polls.
	poll time.Duration
	// jobs remembers statement types, times and metadata for async queries.
	jobs map[string]*job
	// chunks caches recently fetched result partitions.
	chunks []cachedChunk
}

// Config is where a Client connects, and as whom.
type Config struct {
	// Account is the Snowflake account identifier (e.g. acme-prod or
	// xy12345.us-east-1.aws). Used to derive Host when Host is empty and for
	// Snowsight links.
	Account string
	// Host is an explicit account URL override (e.g. for private link or tests);
	// a host without a scheme gets https://.
	Host string
	// Warehouse is the virtual warehouse name; "" until one is chosen, when
	// Check and queries fail with ErrNoWarehouse.
	Warehouse string
	// Database is the default database (DefaultProject's).
	Database string
	// Schema is the default schema.
	Schema string
	// Role is the Snowflake role sent with statements.
	Role string
	// Token gives the bearer token and its Snowflake token type.
	Token func(ctx context.Context) (Token, error)
	// HTTP makes the calls; nil is http.DefaultClient.
	HTTP *http.Client
}

// New makes a client of the Snowflake account c names. It calls nothing.
func New(c Config) *Client {
	h := c.HTTP
	if h == nil {
		h = warehouse.NewHTTPClient()
	}
	host := NormalizeHost(c.Host)
	if host == "" && c.Account != "" {
		host = AccountHost(c.Account)
	}
	return &Client{
		http:      h,
		account:   strings.TrimSpace(c.Account),
		host:      host,
		token:     c.Token,
		database:  strings.TrimSpace(c.Database),
		schema:    strings.TrimSpace(c.Schema),
		role:      strings.TrimSpace(c.Role),
		warehouse: strings.TrimSpace(c.Warehouse),
		poll:      time.Second,
	}
}

var (
	_ warehouse.Warehouse     = (*Client)(nil)
	_ warehouse.Waker         = (*Client)(nil)
	_ warehouse.ProjectLister = (*Client)(nil)
)

// NormalizeHost is host as a URL with no trailing slash: https:// when it has
// no scheme.
func NormalizeHost(host string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host != "" && !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return host
}

// AccountHost derives the Snowflake SQL API base URL from an account
// identifier. Underscores in account names are replaced by hyphens for DNS,
// per Snowflake's hostname convention.
func AccountHost(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return ""
	}
	if strings.Contains(account, "://") || strings.Contains(strings.ToLower(account), ".snowflakecomputing.com") {
		return NormalizeHost(account)
	}
	dns := strings.ReplaceAll(strings.ToLower(account), "_", "-")
	return "https://" + dns + ".snowflakecomputing.com"
}

// SnowsightURL is a table's page in Snowsight for an orgname-accountname
// identifier; "" when the account isn't in org-account form or table is empty.
func SnowsightURL(account, database, schema, table string) string {
	account = strings.TrimSpace(account)
	if account == "" || database == "" || schema == "" || table == "" {
		return ""
	}
	// Strip host wrapper if an account URL was passed.
	if strings.Contains(account, "://") {
		if u, err := url.Parse(account); err == nil {
			account = u.Hostname()
		}
	}
	account = strings.TrimSuffix(strings.ToLower(account), ".snowflakecomputing.com")
	// Modern orgname-accountname identifiers contain a single hyphen and no dots.
	if strings.Contains(account, ".") {
		return ""
	}
	org, acct, ok := strings.Cut(account, "-")
	if !ok || org == "" || acct == "" {
		return ""
	}
	return "https://app.snowflake.com/" + url.PathEscape(org) + "/" + url.PathEscape(acct) +
		"/#/data/databases/" + url.PathEscape(strings.ToUpper(database)) +
		"/schemas/" + url.PathEscape(strings.ToUpper(schema)) +
		"/table/" + url.PathEscape(strings.ToUpper(table))
}

// Account is the configured Snowflake account identifier.
func (c *Client) Account() string { return c.account }

// Host is the Snowflake base URL.
func (c *Client) Host() string { return c.host }

// Warehouse is the virtual warehouse statements run on, "" when none is chosen.
func (c *Client) Warehouse() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warehouse
}

// SetWarehouse makes name the virtual warehouse statements run on.
func (c *Client) SetWarehouse(name string) {
	c.mu.Lock()
	c.warehouse = strings.TrimSpace(name)
	c.mu.Unlock()
}

// DefaultProject is the database the project builds in.
func (c *Client) DefaultProject() string { return c.database }

// ErrNoWarehouse is what Check and queries fail with before a virtual
// warehouse is chosen.
var ErrNoWarehouse = warehouse.ErrNoWarehouse

// ErrAuth is warehouse.ErrAuth in Snowflake's words: errors.Is finds either.
var ErrAuth error = authError{"no valid Snowflake credentials"}

// AuthHint tells the user how to fix ErrAuth.
const AuthHint = "set SNOWFLAKE_TOKEN (a programmatic access token), or configure key-pair auth (private_key_path / SNOWFLAKE_PRIVATE_KEY_PATH)"

type authError struct{ msg string }

func (e authError) Error() string { return e.msg }

// Is makes an authError both ErrAuth and warehouse.ErrAuth.
func (e authError) Is(target error) bool {
	_, ok := target.(authError)
	return ok || target == warehouse.ErrAuth
}

func authErr(why error) error { return fmt.Errorf("%w: %v", ErrAuth, why) }

// FullName is database.schema.table as Snowflake names a relation.
func FullName(database, schema, table string) string { return database + "." + schema + "." + table }

// sfReserved are Snowflake SQL reserved words that require double quotes when
// used as an identifier.
var sfReserved = map[string]bool{
	"ALL": true, "ALTER": true, "AND": true, "ANY": true, "AS": true,
	"BETWEEN": true, "BY": true, "CASE": true, "CAST": true, "CHECK": true,
	"COLUMN": true, "CONNECT": true, "CONSTRAINT": true, "CREATE": true,
	"CROSS": true, "CURRENT": true, "CURRENT_DATE": true, "CURRENT_TIME": true,
	"CURRENT_TIMESTAMP": true, "CURRENT_USER": true, "DEFAULT": true,
	"DELETE": true, "DISTINCT": true, "DROP": true, "ELSE": true,
	"EXISTS": true, "FALSE": true, "FOLLOWING": true, "FOR": true,
	"FROM": true, "FULL": true, "GRANT": true, "GROUP": true,
	"GSCLUSTER": true, "HAVING": true, "ILIKE": true, "IN": true,
	"INCREMENT": true, "INNER": true, "INSERT": true, "INTERSECT": true,
	"INTO": true, "IS": true, "ISSUE": true, "JOIN": true, "LATERAL": true,
	"LEFT": true, "LIKE": true, "LOCALTIME": true, "LOCALTIMESTAMP": true,
	"MINUS": true, "NATURAL": true, "NOT": true, "NULL": true, "OF": true,
	"ON": true, "OR": true, "ORDER": true, "ORGANIZATION": true,
	"QUALIFY": true, "REGEXP": true, "REVOKE": true, "RIGHT": true,
	"RLIKE": true, "ROW": true, "ROWS": true, "SAMPLE": true,
	"SCHEMA": true, "SELECT": true, "SET": true, "SOME": true,
	"START": true, "TABLE": true, "TABLESAMPLE": true, "THEN": true,
	"TO": true, "TRIGGER": true, "TRUE": true, "TRY_CAST": true,
	"UNION": true, "UNIQUE": true, "UPDATE": true, "USING": true,
	"VALUES": true, "VIEW": true, "WHEN": true, "WHENEVER": true,
	"WHERE": true, "WITH": true,
}

func isPlainIdent(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
		digit := c >= '0' && c <= '9'
		if i == 0 {
			if !letter {
				return false
			}
		} else if !letter && !digit && c != '$' {
			return false
		}
	}
	return !sfReserved[strings.ToUpper(name)]
}

// sqlIdent formats a Snowflake SQL identifier: plain unreserved names stay
// unquoted so Snowflake folds them to uppercase (matching dbt-snowflake's
// default unquoted DDL), while reserved words and names with special
// characters are double-quoted.
func sqlIdent(name string) string {
	if isPlainIdent(name) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral quotes a Snowflake SQL string literal with single quotes,
// escaping backslashes and single quotes.
func quoteLiteral(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `''`)
	return "'" + r.Replace(s) + "'"
}

// escapeLike escapes %, _ and \ for a Snowflake LIKE pattern.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// do calls the Snowflake SQL API: body, when not nil, goes as JSON. Returns
// the HTTP status code and response bytes for 2xx responses (200 or 202).
// A 401 is ErrAuth; 422 is a *StatementError; any other non-2xx is a
// *warehouse.APIError.
func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if c.host == "" {
		return 0, nil, errors.New("no Snowflake account: set the dbt target's account, choose a Snowflake connection, or set SNOWFLAKE_ACCOUNT")
	}
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = b
	}
	if c.token == nil {
		return 0, nil, authErr(errors.New("no credentials configured"))
	}
	tok, err := c.token(ctx)
	if err != nil {
		if errors.Is(err, warehouse.ErrAuth) {
			return 0, nil, err
		}
		return 0, nil, authErr(err)
	}
	idempotent := method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete || strings.HasSuffix(path, "/cancel")
	resp, err := warehouse.DoWithRetry(ctx, c.http, func() (*http.Request, error) {
		var r io.Reader
		if payload != nil {
			r = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.host+path, r)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "xf")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+tok.Value)
		if tok.Type != "" {
			req.Header.Set("X-Snowflake-Authorization-Token-Type", string(tok.Type))
		}
		return req, nil
	}, warehouse.RetryPolicy{RetryServerErrors: idempotent})
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	const maxResponseBytes = 64 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > maxResponseBytes {
		return resp.StatusCode, nil, errors.New("Snowflake response exceeded 64 MiB limit")
	}
	if resp.StatusCode/100 == 2 {
		return resp.StatusCode, data, nil
	}
	var e struct {
		Code     string `json:"code"`
		SQLState string `json:"sqlState"`
		Message  string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, nil, authErr(errors.New(msg))
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return resp.StatusCode, nil, &StatementError{Code: e.Code, SQLState: e.SQLState, Message: msg}
	}
	return resp.StatusCode, nil, &warehouse.APIError{Status: resp.StatusCode, Message: msg}
}
