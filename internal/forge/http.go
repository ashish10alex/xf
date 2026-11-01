package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// api calls one host's API with a token.
type api struct {
	repo  Repo
	token string
	hc    *http.Client
}

// requestTimeout bounds one call; a host that takes longer counts as
// unreachable.
const requestTimeout = 15 * time.Second

func (a api) client() *http.Client {
	if a.hc != nil {
		return a.hc
	}
	return &http.Client{Timeout: requestTimeout}
}

// get reads the JSON at u (an API URL) into out.
func (a api) get(ctx context.Context, u string, out any) error {
	return a.do(ctx, http.MethodGet, u, nil, out)
}

func (a api) do(ctx context.Context, method, u string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "xf")
	if a.repo.Kind == GitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if a.token != "" {
			req.Header.Set("Authorization", "Bearer "+a.token)
		}
	} else if a.token != "" {
		req.Header.Set("PRIVATE-TOKEN", a.token)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		return &UnreachableError{Host: a.repo.Host, Err: shortNetErr(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &UnreachableError{Host: a.repo.Host, Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrToken
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("%s is limiting how often xf asks: try again later", a.repo.Host)
		}
		return fmt.Errorf("%s refused: %s", a.repo.Host, message(data, resp.Status))
	case resp.StatusCode >= 500:
		return &UnreachableError{Host: a.repo.Host, Err: errors.New(resp.Status)}
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s: %s", a.repo.Host, message(data, resp.Status))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		// A login page or a proxy's, not the API.
		return fmt.Errorf("%s didn't answer as %s's API does", a.repo.Host, a.repo.Kind)
	}
	return nil
}

// errNotFound is a 404, which callers say in their own words.
var errNotFound = errors.New("not found")

// message is an error answer's message, else status.
func message(data []byte, status string) string {
	var m struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &m) == nil {
		if s, ok := m.Message.(string); ok && s != "" {
			return s
		}
		if m.Error != "" {
			return m.Error
		}
	}
	return status
}

// shortNetErr is a network error without the URL Go puts in front.
func shortNetErr(err error) error {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && strings.Contains(s[:i], "://") {
		return errors.New(s[i+2:])
	}
	return err
}

// seconds is how long from start to end, 0 when either is unknown.
func seconds(start, end *time.Time) float64 {
	if start == nil || end == nil || start.IsZero() || end.IsZero() {
		return 0
	}
	return end.Sub(*start).Seconds()
}
