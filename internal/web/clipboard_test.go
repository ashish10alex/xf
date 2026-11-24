package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalClipboardReadAndWrite(t *testing.T) {
	stored := "from system clipboard"
	s, err := New(Config{
		ClipboardRead:  func() (string, error) { return stored, nil },
		ClipboardWrite: func(text string) error { stored = text; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()

	// Boot advertises localClipboard for a local browser.
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/boot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("boot status = %d", rec.Code)
	}
	var boot bootView
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if !boot.LocalClipboard {
		t.Fatal("boot.LocalClipboard = false, want true")
	}

	// GET /api/clipboard reads the local clipboard.
	req = httptest.NewRequest("GET", "http://127.0.0.1:8080/api/clipboard", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/clipboard status = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "from system clipboard" {
		t.Fatalf("GET /api/clipboard = %q, want %q", got.Text, "from system clipboard")
	}

	// POST /api/clipboard writes to the local clipboard.
	req = httptest.NewRequest("POST", "http://127.0.0.1:8080/api/clipboard", strings.NewReader(`{"text":"yanked line\n"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/clipboard status = %d: %s", rec.Code, rec.Body.String())
	}
	if stored != "yanked line\n" {
		t.Fatalf("stored clipboard = %q, want %q", stored, "yanked line\n")
	}
}

func TestClipboardRefusedWhenRemote(t *testing.T) {
	called := false
	s, err := New(Config{
		Hosts:          []string{"mybox.tail.ts.net"},
		Token:          "secret-token",
		ClipboardRead:  func() (string, error) { called = true; return "secret", nil },
		ClipboardWrite: func(string) error { called = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()

	withCookie := func(r *http.Request) *http.Request {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: "secret-token"})
		return r
	}

	// Boot does not advertise localClipboard when --allow-host is set.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withCookie(httptest.NewRequest("GET", "http://127.0.0.1:8080/api/boot", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("boot status = %d", rec.Code)
	}
	var boot bootView
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.LocalClipboard {
		t.Fatal("boot.LocalClipboard = true on remote server, want false")
	}

	// Both GET and POST /api/clipboard are forbidden and never touch the host clipboard.
	for _, tc := range []struct {
		method, body string
	}{
		{"GET", ""},
		{"POST", `{"text":"nope"}`},
	} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, withCookie(httptest.NewRequest(tc.method, "http://127.0.0.1:8080/api/clipboard", strings.NewReader(tc.body))))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s /api/clipboard status = %d, want 403", tc.method, rec.Code)
		}
	}
	if called {
		t.Fatal("host clipboard callback was called on a remote server")
	}
}

func TestClipboardErrors(t *testing.T) {
	s, err := New(Config{
		ClipboardRead:  func() (string, error) { return "", errors.New("no xclip") },
		ClipboardWrite: func(string) error { return errors.New("no xclip") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8080/api/clipboard", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET error status = %d, want 500", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "http://127.0.0.1:8080/api/clipboard", strings.NewReader(`{"text":"x"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST error status = %d, want 500", rec.Code)
	}
}
