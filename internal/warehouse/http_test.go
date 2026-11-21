package warehouse

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoWithRetryRecovers429And502AndRebuildsBody(t *testing.T) {
	var attempts atomic.Int32
	var seenBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenBodies = append(seenBodies, string(b))
		switch attempts.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader([]byte(`{"k":"v"}`)))
	}, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, RetryServerErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || attempts.Load() != 3 {
		t.Fatalf("status=%d attempts=%d, want 200 and 3", resp.StatusCode, attempts.Load())
	}
	for i, b := range seenBodies {
		if b != `{"k":"v"}` {
			t.Errorf("attempt %d body = %q", i+1, b)
		}
	}
}

func TestDoWithRetrySkipsNonIdempotent500And4xx(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		retryServerErrors bool
		wantAttempts      int32
	}{
		{"403 never retried", http.StatusForbidden, true, 1},
		{"500 not retried when non-idempotent", http.StatusInternalServerError, false, 1},
		{"503 not retried when non-idempotent", http.StatusServiceUnavailable, false, 1},
		{"503 retried when idempotent", http.StatusServiceUnavailable, true, 3},
		{"429 retried even when non-idempotent", http.StatusTooManyRequests, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			resp, err := DoWithRetry(context.Background(), srv.Client(), func() (*http.Request, error) {
				return http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
			}, RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, RetryServerErrors: tc.retryServerErrors})
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if attempts.Load() != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", attempts.Load(), tc.wantAttempts)
			}
		})
	}
}

func TestDoWithRetryRespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := DoWithRetry(ctx, srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	}, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Second, MaxDelay: time.Minute})
	if err == nil {
		t.Fatal("expected context deadline exceeded")
	}
}
