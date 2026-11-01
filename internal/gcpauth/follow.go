package gcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// xf follows the Application Default Credentials: a new login (gcloud auth
// application-default login, set-quota-project, revoke) takes effect at
// the next Google call, with no restart. Each call looks at the credential
// file (a stat, about a microsecond), and credentials are found again once
// it has changed.

// ErrNoCredentials is what a Credentials request fails with while no ADC
// can be found: the file is gone, or unreadable for now.
var ErrNoCredentials = errors.New("no Application Default Credentials")

// fileState is what Generation compares of the credential file.
type fileState struct {
	path   string
	exists bool
	mod    time.Time
	size   int64
}

var adcFile struct {
	mu   sync.Mutex
	seen bool
	last fileState
	gen  uint64
}

// statADC is what Generation compares: the file sourceFile names, as it
// is now.
func statADC() fileState {
	path, _ := sourceFile()
	st := fileState{path: path}
	if fi, err := os.Stat(path); err == nil {
		st.exists, st.mod, st.size = true, fi.ModTime(), fi.Size()
	}
	return st
}

// Generation looks at the ADC file and returns its generation: a number
// that goes up each time the file is written, made or removed (or
// $GOOGLE_APPLICATION_CREDENTIALS names another). Caches of answers that
// failed for want of credentials compare it with the one they last saw.
func Generation() uint64 {
	st := statADC()
	adcFile.mu.Lock()
	defer adcFile.mu.Unlock()
	if adcFile.seen && st != adcFile.last {
		adcFile.gen++
	}
	adcFile.seen, adcFile.last = true, st
	return adcFile.gen
}

// Credentials are the ADC for one scope, found again when Generation
// changes, and on every request while none could be found.
type Credentials struct {
	scope string

	mu    sync.Mutex
	gen   uint64
	ts    oauth2.TokenSource
	quota string
	err   error
}

// NewCredentials finds the ADC for scope. It fails when there are none
// now; once made, the credentials follow the file.
func NewCredentials(scope string) (*Credentials, error) {
	c := &Credentials{scope: scope}
	if _, _, err := c.current(); err != nil {
		return nil, err
	}
	return c, nil
}

// current is the token source and quota project of the ADC as they are
// now.
func (c *Credentials) current() (oauth2.TokenSource, string, error) {
	gen := Generation()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ts != nil && gen == c.gen {
		return c.ts, c.quota, nil
	}
	c.gen = gen
	// The token source refreshes with this context, so it must outlive
	// any one request.
	creds, err := google.FindDefaultCredentials(context.Background(), c.scope)
	if err != nil {
		c.ts, c.quota, c.err = nil, "", fmt.Errorf("%w: %v", ErrNoCredentials, err)
		return nil, "", c.err
	}
	var file struct {
		QuotaProject string `json:"quota_project_id"`
	}
	_ = json.Unmarshal(creds.JSON, &file) // metadata-server credentials have no JSON
	c.ts, c.quota, c.err = oauth2.ReuseTokenSource(nil, creds.TokenSource), file.QuotaProject, nil
	return c.ts, c.quota, nil
}

// QuotaProject is the ADC's quota_project_id, "" when they name none or
// there are none.
func (c *Credentials) QuotaProject() string {
	_, quota, _ := c.current()
	return quota
}

// Client is an HTTP client that signs each request with the ADC as they
// are when it is sent.
func (c *Credentials) Client() *http.Client {
	return &http.Client{Transport: transport{c}}
}

type transport struct{ c *Credentials }

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ts, _, err := t.c.current()
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return (&oauth2.Transport{Source: ts, Base: http.DefaultTransport}).RoundTrip(req)
}
