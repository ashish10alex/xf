package lineage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2"

	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// ErrAuth means there are no usable credentials, as for BigQuery.
var ErrAuth = errors.New("not signed in to Google Cloud: run `gcloud auth application-default login`")

const (
	defaultBaseURL = "https://datalineage.googleapis.com"
	scope          = "https://www.googleapis.com/auth/cloud-platform"
	// pageSize is the most links a search page can hold; the API returns
	// 10 a page unless asked for more.
	pageSize = 100
	// maxSearchPages caps Dataplex searchLinks pagination (10,000 links) so a
	// huge fan-out or cyclic nextPageToken cannot loop forever.
	maxSearchPages = 100
)

// Client searches Data Lineage links with Application Default Credentials.
type Client struct {
	http         *http.Client
	baseURL      string
	quotaProject string
	// creds, when set, are the ADC the client follows (gcpauth.Credentials),
	// whose quota project goes in place of quotaProject.
	creds *gcpauth.Credentials
}

// NewClient finds Application Default Credentials. It fails with ErrAuth
// when there are none. Once made, it follows them: a new login is used
// from the next call.
func NewClient(context.Context) (*Client, error) {
	creds, err := gcpauth.NewCredentials(scope)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrAuth, err)
	}
	c := NewClientWith(creds.Client(), defaultBaseURL, "")
	c.creds = creds
	return c, nil
}

// NewClientWith makes a client that sends its calls with httpClient to
// baseURL, for tests.
func NewClientWith(httpClient *http.Client, baseURL, quotaProject string) *Client {
	if httpClient == nil {
		httpClient = warehouse.NewHTTPClient()
	}
	return &Client{http: httpClient, baseURL: baseURL, quotaProject: quotaProject}
}

// SearchRequest asks for the links from Source or to Target, in Parent
// ("projects/<project>/locations/<region>").
type SearchRequest struct {
	Parent string           `json:"-"`
	Source *EntityReference `json:"source,omitempty"`
	Target *EntityReference `json:"target,omitempty"`
}

// SearchLinks returns every page of a link search, capped at maxSearchPages
// so huge fan-out or cyclic pagination tokens never loop forever.
func (c *Client) SearchLinks(ctx context.Context, req SearchRequest) ([]Link, error) {
	u := c.baseURL + "/v1/" + req.Parent + ":searchLinks"
	var links []Link
	token := ""
	seenTokens := map[string]bool{}
	entity := ""
	switch {
	case req.Source != nil:
		entity = req.Source.FullyQualifiedName
	case req.Target != nil:
		entity = req.Target.FullyQualifiedName
	}
	for pageNum := 0; ; pageNum++ {
		if pageNum >= maxSearchPages {
			if entity != "" {
				return nil, fmt.Errorf("data lineage search exceeded %d pages for %s in %s", maxSearchPages, entity, projectOf(req.Parent))
			}
			return nil, fmt.Errorf("data lineage search exceeded %d pages in %s", maxSearchPages, projectOf(req.Parent))
		}
		body := struct {
			SearchRequest
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken,omitempty"`
		}{req, pageSize, token}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		resp, err := c.post(ctx, u, data, req.Parent)
		if err != nil {
			return nil, err
		}
		var page struct {
			Links         []Link `json:"links"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(resp, &page); err != nil {
			return nil, fmt.Errorf("decode lineage links: %w", err)
		}
		links = append(links, page.Links...)
		if page.NextPageToken == "" {
			return links, nil
		}
		if seenTokens[page.NextPageToken] {
			return nil, fmt.Errorf("data lineage search returned a repeated page token in %s", projectOf(req.Parent))
		}
		seenTokens[page.NextPageToken] = true
		token = page.NextPageToken
	}
}

func (c *Client) post(ctx context.Context, u string, body []byte, parent string) ([]byte, error) {
	quota := c.quotaProject
	if c.creds != nil {
		quota = c.creds.QuotaProject()
	}
	resp, err := warehouse.DoWithRetry(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if quota != "" {
			req.Header.Set("X-Goog-User-Project", quota)
		}
		return req, nil
	}, warehouse.RetryPolicy{RetryServerErrors: true})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) || errors.Is(err, gcpauth.ErrNoCredentials) {
			return nil, ErrAuth
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, data, parent)
	}
	return data, nil
}

// apiError says what went wrong as the extension does: what to enable or
// grant for a 403, how to log in for a 401, else the API's message.
func apiError(status int, body []byte, parent string) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return fmt.Errorf("no access to Data Lineage in %s: it needs the Data Lineage API enabled and roles/datalineage.viewer", projectOf(parent))
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return errors.New(e.Error.Message)
	}
	return fmt.Errorf("data lineage: %s", http.StatusText(status))
}

// projectOf is the project of "projects/<project>/locations/<region>".
func projectOf(parent string) string {
	rest, ok := strings.CutPrefix(parent, "projects/")
	if !ok {
		return parent
	}
	project, _, _ := strings.Cut(rest, "/")
	return project
}
