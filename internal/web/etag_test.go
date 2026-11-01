package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

// fetch GETs u with an If-None-Match of tag, when there is one.
func fetch(t *testing.T, c *http.Client, u, tag string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(body)
}

// TestAssetsRevalidate: a reload asks again for each file of the page, and
// one it already has is answered 304, with no body.
func TestAssetsRevalidate(t *testing.T) {
	t.Setenv(devAssetsEnv, "") // the files built into xf
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	for _, p := range []string{"/", "/state.js", "/vendor/codemirror.mjs", "/style.css", "/themes.css"} {
		res, body := fetch(t, c, ts.URL+p, "")
		tag := res.Header.Get("ETag")
		if res.StatusCode != http.StatusOK || tag == "" || body == "" {
			t.Fatalf("%s: status %d, ETag %q, %d bytes", p, res.StatusCode, tag, len(body))
		}
		if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", p, cc)
		}

		res, body = fetch(t, c, ts.URL+p, tag)
		if res.StatusCode != http.StatusNotModified || body != "" {
			t.Errorf("%s again with its ETag: status %d, %d bytes, want 304 and none", p, res.StatusCode, len(body))
		}

		// A tag of another version: the file changed since, so it comes whole.
		res, body = fetch(t, c, ts.URL+p, `"older"`)
		if res.StatusCode != http.StatusOK || body == "" {
			t.Errorf("%s with another version's ETag: status %d, %d bytes, want 200 and the file", p, res.StatusCode, len(body))
		}
	}
}

// TestDevAssetsHaveNoETag: files XF_WEB_ASSETS serves change while xf runs,
// so they go by their modification time, not a tag of the built-in ones.
func TestDevAssetsHaveNoETag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.js"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(devAssetsEnv, dir)
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	res, body := fetch(t, c, ts.URL+"/state.js", "")
	if body != "// edited\n" {
		t.Fatalf("served %q, want the directory's file", body)
	}
	if tag := res.Header.Get("ETag"); tag != "" {
		t.Errorf("ETag %q on a file from XF_WEB_ASSETS", tag)
	}
	if res.Header.Get("Last-Modified") == "" {
		t.Error("no Last-Modified on a file from XF_WEB_ASSETS")
	}
}
