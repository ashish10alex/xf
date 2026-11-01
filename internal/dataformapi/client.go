// Package dataformapi runs actions in GCP through the Dataform API, as the
// extension's "api" execution mode does: it compiles a pushed git branch of a
// Dataform repository and invokes a workflow from the result. Like package
// bigquery it calls the REST API with Application Default Credentials and no
// client library.
package dataformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/oauth2"

	"github.com/ashish10alex/xf/internal/gcpauth"
)

// ErrAuth means there are no usable credentials: none are set up, the login
// has expired, or Dataform rejected them. Logging in again usually fixes it.
var ErrAuth = errors.New("no valid Google credentials")

// AuthHint tells the user how to fix ErrAuth.
const AuthHint = "run `gcloud auth application-default login`"

const (
	defaultBaseURL = "https://dataform.googleapis.com"
	apiVersion     = "v1beta1"
	scope          = "https://www.googleapis.com/auth/cloud-platform"
)

// Repository is a Dataform repository in GCP.
type Repository struct {
	Project, Location, Name string
}

func (r Repository) path() string {
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s",
		url.PathEscape(r.Project), url.PathEscape(r.Location), url.PathEscape(r.Name))
}

// ConsoleURL is the Cloud console page of a workflow invocation, given the
// invocation's resource name.
func (r Repository) ConsoleURL(invocation string) string {
	id := invocation[strings.LastIndex(invocation, "/")+1:]
	return fmt.Sprintf("https://console.cloud.google.com/bigquery/dataform/locations/%s/repositories/%s/workflows/%s?project=%s",
		url.PathEscape(r.Location), url.PathEscape(r.Name), url.PathEscape(id), url.QueryEscape(r.Project))
}

// Target is an action's target as the API spells it.
type Target struct {
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Name     string `json:"name,omitempty"`
}

// CompilationResult is a compile of the repository in GCP.
type CompilationResult struct {
	Name                 string             `json:"name"`
	ResolvedGitCommitSHA string             `json:"resolvedGitCommitSha"`
	DataformCoreVersion  string             `json:"dataformCoreVersion"`
	Errors               []CompilationError `json:"compilationErrors"`
}

// CompilationError is one error of a CompilationResult.
type CompilationError struct {
	Message      string  `json:"message"`
	Path         string  `json:"path"`
	ActionTarget *Target `json:"actionTarget"`
}

// InvocationConfig selects what a workflow invocation runs.
type InvocationConfig struct {
	IncludedTargets []Target `json:"includedTargets,omitempty"`
	// IncludedTags selects every action with any of the tags.
	IncludedTags                         []string `json:"includedTags,omitempty"`
	TransitiveDependenciesIncluded       bool     `json:"transitiveDependenciesIncluded,omitempty"`
	TransitiveDependentsIncluded         bool     `json:"transitiveDependentsIncluded,omitempty"`
	FullyRefreshIncrementalTablesEnabled bool     `json:"fullyRefreshIncrementalTablesEnabled,omitempty"`
}

// Invocation states.
const (
	StateRunning   = "RUNNING"
	StateSucceeded = "SUCCEEDED"
	StateCancelled = "CANCELLED"
	StateFailed    = "FAILED"
	StateCanceling = "CANCELING"
)

// Timing is when an invocation or one of its actions started and ended, as
// RFC 3339 timestamps; "" until it has.
type Timing struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

// Invocation is a workflow invocation.
type Invocation struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Timing Timing `json:"invocationTiming"`
}

// Done says whether the invocation has ended.
func (i *Invocation) Done() bool {
	switch i.State {
	case StateSucceeded, StateCancelled, StateFailed:
		return true
	}
	return false
}

// Action is the state of one action of a workflow invocation: PENDING,
// RUNNING, SKIPPED, DISABLED, SUCCEEDED, CANCELLED or FAILED.
type Action struct {
	Target        Target `json:"target"`
	State         string `json:"state"`
	FailureReason string `json:"failureReason"`
	Timing        Timing `json:"invocationTiming"`
	// BigQuery is set for an action that ran a BigQuery job.
	BigQuery struct {
		// JobID is the job's ID, bare or as project:location.id.
		JobID string `json:"jobId"`
	} `json:"bigqueryAction"`
}

// Client makes authenticated calls to the Dataform API.
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
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	c := newClient(creds.Client(), defaultBaseURL, "")
	c.creds = creds
	return c, nil
}

func newClient(httpClient *http.Client, baseURL, quotaProject string) *Client {
	return &Client{http: httpClient, baseURL: baseURL, quotaProject: quotaProject}
}

// CompilationConfig is what a compile adds to the names the project
// builds, the API's codeCompilationConfig: the CLI's --table-prefix,
// --schema-suffix and --database-suffix.
type CompilationConfig struct {
	TablePrefix    string `json:"tablePrefix,omitempty"`
	SchemaSuffix   string `json:"schemaSuffix,omitempty"`
	DatabaseSuffix string `json:"databaseSuffix,omitempty"`
}

// CreateCompilationResult compiles gitCommitish (a branch, tag or commit)
// of the repository with cfg. Compile errors come back in the result, not
// as an error.
func (c *Client) CreateCompilationResult(ctx context.Context, repo Repository, gitCommitish string, cfg CompilationConfig) (*CompilationResult, error) {
	req := struct {
		GitCommitish string             `json:"gitCommitish"`
		Config       *CompilationConfig `json:"codeCompilationConfig,omitempty"`
	}{GitCommitish: gitCommitish}
	if cfg != (CompilationConfig{}) {
		req.Config = &cfg
	}
	var out CompilationResult
	if err := c.call(ctx, http.MethodPost, repo.path()+"/compilationResults", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateWorkflowInvocation runs a compilation result (its resource name).
func (c *Client) CreateWorkflowInvocation(ctx context.Context, repo Repository, compilationResult string, cfg InvocationConfig) (*Invocation, error) {
	req := struct {
		CompilationResult string           `json:"compilationResult"`
		InvocationConfig  InvocationConfig `json:"invocationConfig"`
	}{compilationResult, cfg}
	var out Invocation
	if err := c.call(ctx, http.MethodPost, repo.path()+"/workflowInvocations", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetWorkflowInvocation fetches an invocation by its resource name.
func (c *Client) GetWorkflowInvocation(ctx context.Context, name string) (*Invocation, error) {
	var out Invocation
	if err := c.call(ctx, http.MethodGet, name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryWorkflowInvocationActions lists the actions of an invocation and
// their states, following every page.
func (c *Client) QueryWorkflowInvocationActions(ctx context.Context, name string) ([]Action, error) {
	var all []Action
	token := ""
	for {
		u := name + ":query?pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Actions []Action `json:"workflowInvocationActions"`
			Next    string   `json:"nextPageToken"`
		}
		if err := c.call(ctx, http.MethodGet, u, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Actions...)
		if page.Next == "" {
			return all, nil
		}
		token = page.Next
	}
}

// Locations lists the IDs of the locations Dataform offers in project
// (europe-west2 and so on).
func (c *Client) Locations(ctx context.Context, project string) ([]string, error) {
	var ids []string
	token := ""
	for {
		u := fmt.Sprintf("projects/%s/locations?pageSize=1000", url.PathEscape(project))
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Locations []struct {
				LocationID string `json:"locationId"`
			} `json:"locations"`
			Next string `json:"nextPageToken"`
		}
		if err := c.call(ctx, http.MethodGet, u, nil, &page); err != nil {
			return nil, err
		}
		for _, l := range page.Locations {
			ids = append(ids, l.LocationID)
		}
		if page.Next == "" {
			return ids, nil
		}
		token = page.Next
	}
}

// RepositoryExists says whether the repository exists, and can be seen
// with these credentials.
func (c *Client) RepositoryExists(ctx context.Context, repo Repository) (bool, error) {
	err := c.call(ctx, http.MethodGet, repo.path(), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden) {
		return false, nil
	}
	return err == nil, err
}

// FindRepository looks for the repository named name in every location of
// project and returns the locations that have it (normally one).
func (c *Client) FindRepository(ctx context.Context, project, name string) ([]string, error) {
	locations, err := c.Locations(ctx, project)
	if err != nil {
		return nil, err
	}
	type result struct {
		location string
		found    bool
		err      error
	}
	results := make(chan result, len(locations))
	sem := make(chan struct{}, 8)
	for _, loc := range locations {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			found, err := c.RepositoryExists(ctx, Repository{Project: project, Location: loc, Name: name})
			results <- result{loc, found, err}
		}()
	}
	var found []string
	var firstErr error
	for range locations {
		r := <-results
		switch {
		case r.found:
			found = append(found, r.location)
		case r.err != nil && firstErr == nil:
			firstErr = r.err
		}
	}
	slices.Sort(found)
	if len(found) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return found, nil
}

// LooksLikeLocationError says whether err is the API rejecting a location,
// as it does for a multi-region such as EU: Dataform repositories live in a
// region.
func LooksLikeLocationError(err error, location string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || (apiErr.Status != http.StatusForbidden && apiErr.Status != http.StatusNotFound && apiErr.Status != http.StatusBadRequest) {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Message), "location") || !strings.Contains(location, "-")
}

// CancelWorkflowInvocation asks Dataform to cancel an invocation; it is
// CANCELING until its BigQuery jobs have stopped.
func (c *Client) CancelWorkflowInvocation(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodPost, name+":cancel", struct{}{}, nil)
}

// call sends a request to path (relative to the API version, with any query
// string) with req as its JSON body when non-nil, and decodes a 200
// response into out when non-nil.
func (c *Client) call(ctx context.Context, method, path string, req, out any) error {
	var body io.Reader
	if req != nil {
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+apiVersion+"/"+path, body)
	if err != nil {
		return err
	}
	if req != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	quota := c.quotaProject
	if c.creds != nil {
		quota = c.creds.QuotaProject()
	}
	if quota != "" {
		r.Header.Set("X-Goog-User-Project", quota)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, gcpauth.ErrNoCredentials) {
			return fmt.Errorf("%w: %v", ErrAuth, err)
		}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return fmt.Errorf("%w: %s", ErrAuth, retrieveReason(re))
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, respBody)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// APIError is an error response from Dataform other than 401.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }

func apiError(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", ErrAuth, msg)
	}
	return &APIError{Status: status, Message: msg}
}

func retrieveReason(re *oauth2.RetrieveError) string {
	switch {
	case re.ErrorDescription != "":
		return re.ErrorDescription
	case re.ErrorCode != "":
		return re.ErrorCode
	}
	return "token refresh failed"
}
