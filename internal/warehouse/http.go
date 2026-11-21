package warehouse

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultResponseHeaderTimeout bounds how long warehouse and lineage API
// requests may wait for response headers so a stalled socket never hangs xf
// check or xf web indefinitely, without cutting off slow large result downloads.
const DefaultResponseHeaderTimeout = 90 * time.Second

// NewHTTPClient returns an HTTP client whose transport bounds response-header
// and idle connection waits without limiting full body download duration.
func NewHTTPClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: DefaultResponseHeaderTimeout}
	}
	clone := tr.Clone()
	clone.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	if clone.IdleConnTimeout == 0 {
		clone.IdleConnTimeout = DefaultResponseHeaderTimeout
	}
	return &http.Client{Transport: clone}
}

// RetryPolicy configures bounded retries on transient HTTP responses.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the initial call.
	// Defaults to 4 when <= 0.
	MaxAttempts int
	// BaseDelay is the initial backoff delay before the first retry.
	// Defaults to 150ms when <= 0.
	BaseDelay time.Duration
	// MaxDelay caps exponential backoff and Retry-After waits.
	// Defaults to 10s when <= 0.
	MaxDelay time.Duration
	// RetryServerErrors enables retries on 500, 502, 503 and 504 in addition
	// to 429. Set true only for idempotent or read-only requests so POSTs
	// that start side-effecting work cannot execute twice on a 503.
	RetryServerErrors bool
}

// DoWithRetry sends requests created by buildReq through hc, retrying on
// HTTP 429 (and 500/502/503/504 when policy.RetryServerErrors is true)
// with bounded exponential backoff + jitter, honoring Retry-After and ctx
// cancellation. When err == nil, the caller must close resp.Body.
func DoWithRetry(ctx context.Context, hc *http.Client, buildReq func() (*http.Request, error), policy RetryPolicy) (*http.Response, error) {
	if hc == nil {
		hc = NewHTTPClient()
	}
	maxAttempts := policy.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	baseDelay := policy.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 150 * time.Millisecond
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 10 * time.Second
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if attempt >= maxAttempts-1 || !retryableStatus(resp.StatusCode, policy.RetryServerErrors) {
			return resp, nil
		}
		wait := retryDelay(resp.Header.Get("Retry-After"), attempt, baseDelay, maxDelay)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func retryableStatus(status int, serverErrors bool) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return serverErrors
	}
	return false
}

func retryDelay(retryAfter string, attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	if d, ok := parseRetryAfter(retryAfter); ok {
		return min(max(d, 0), maxDelay)
	}
	shift := min(attempt, 10)
	d := min(baseDelay<<shift, maxDelay)
	// +/-20% jitter to avoid thundering herds across concurrent workers.
	if spread := int64(d / 5); spread > 0 {
		d += time.Duration(rand.Int64N(2*spread+1) - spread)
	}
	return min(max(d, 0), maxDelay)
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}
