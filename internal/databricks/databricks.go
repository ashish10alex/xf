// Package databricks calls a Databricks workspace's REST API: the SQL
// Statement Execution API, SQL warehouses and Unity Catalog, with no SDK
// (ADR 0042). A dbt project on the dbt-databricks adapter builds there.
//
// A SQL warehouse bills while it runs, so only Check and the queries a user
// asks for run statements, and they start a stopped warehouse; what Unity
// Catalog answers (a table's columns, the catalogs and tables) wakes none.
//
// The Client is xf's warehouse.Warehouse and its warehouse.Waker: cmd/xf
// makes it, and nothing else imports this package.
package databricks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// Client makes authenticated calls to a workspace.
type Client struct {
	http *http.Client
	host string
	// token gives the bearer token of each call.
	token func(ctx context.Context) (string, error)
	// catalog is the catalog queries name no catalog of, the dbt target's.
	catalog string

	mu sync.Mutex
	// warehouse is the SQL warehouse statements run on, "" until one is
	// chosen.
	warehouse string
	// poll is how long a statement's status is waited on between polls.
	poll time.Duration
	// jobs are what this client knows of the queries it started, which
	// the API doesn't say: their type and times.
	jobs map[string]*job
	// chunks are the chunks of results read last, so paging through one
	// fetches its file once.
	chunks []cachedChunk
}

// Config is where a Client connects, and as whom.
type Config struct {
	// Host is the workspace, such as https://dbc-1.cloud.databricks.com; a
	// host without a scheme is https.
	Host string
	// Warehouse is the SQL warehouse's ID; "" until one is chosen, when
	// Check fails with ErrNoWarehouse.
	Warehouse string
	// Catalog is the catalog the project builds in, DefaultProject's.
	Catalog string
	// Token gives the bearer token of a call: a personal access token or an
	// OAuth access token. An error from it is wrapped in ErrAuth.
	Token func(ctx context.Context) (string, error)
	// HTTP makes the calls; nil is http.DefaultClient.
	HTTP *http.Client
}

// New makes a client of the workspace c names. It calls nothing.
func New(c Config) *Client {
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	return &Client{http: h, host: NormalizeHost(c.Host), token: c.Token, catalog: c.Catalog, warehouse: c.Warehouse, poll: time.Second}
}

// The Client is a warehouse whose SQL warehouse is started by what it runs.
var (
	_ warehouse.Warehouse     = (*Client)(nil)
	_ warehouse.Waker         = (*Client)(nil)
	_ warehouse.ProjectLister = (*Client)(nil)
)

// NormalizeHost is host as a URL with no trailing slash: https:// when it
// has no scheme.
func NormalizeHost(host string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host != "" && !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return host
}

// Host is the workspace's URL.
func (c *Client) Host() string { return c.host }

// Warehouse is the ID of the SQL warehouse statements run on, "" when none
// is chosen.
func (c *Client) Warehouse() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warehouse
}

// SetWarehouse makes id the SQL warehouse statements run on.
func (c *Client) SetWarehouse(id string) {
	c.mu.Lock()
	c.warehouse = id
	c.mu.Unlock()
}

// DefaultProject is the catalog the project builds in.
func (c *Client) DefaultProject() string { return c.catalog }

// ErrNoWarehouse is what Check and the queries fail with before a SQL
// warehouse is chosen: neither the databricksWarehouse setting nor the dbt
// target's http_path names one.
var ErrNoWarehouse = warehouse.ErrNoWarehouse

// ErrAuth is warehouse.ErrAuth in Databricks' words: errors.Is finds
// either.
var ErrAuth error = authError{"no valid Databricks credentials"}

// AuthHint tells the user how to fix ErrAuth.
const AuthHint = "set DATABRICKS_TOKEN, or run `databricks auth login --host <workspace>`"

type authError struct{ msg string }

func (e authError) Error() string { return e.msg }

// Is makes an authError both ErrAuth and warehouse.ErrAuth.
func (e authError) Is(target error) bool {
	_, ok := target.(authError)
	return ok || target == warehouse.ErrAuth
}

// authErr is ErrAuth with why.
func authErr(why error) error { return fmt.Errorf("%w: %v", ErrAuth, why) }

// do calls the API: body, when not nil, goes as JSON. A 401 is ErrAuth,
// and any other error status a *warehouse.APIError with the API's message.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c.host == "" {
		return nil, errors.New("no Databricks workspace: set the dbt target's host, choose a Databricks profile, or set DATABRICKS_HOST")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.host+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token == nil {
		return nil, authErr(errors.New("no token"))
	}
	tok, err := c.token(ctx)
	if err != nil {
		if errors.Is(err, warehouse.ErrAuth) {
			return nil, err
		}
		return nil, authErr(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return data, nil
	}
	var e struct {
		Code    string `json:"error_code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, authErr(errors.New(msg))
	}
	return nil, &warehouse.APIError{Status: resp.StatusCode, Message: msg}
}
