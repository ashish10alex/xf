package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	src := strings.Join([]string{
		"# Getting started",
		"## Getting started",
		"",
		"| a | b |",
		"|---|---|",
		"| 1 | ~~2~~ |",
		"",
		"- [x] done",
		"",
		"<script>alert(1)</script>",
		"",
		"[bad](javascript:alert(1)) [gone](missing.md) [up](../README.md) [sib](other.md#part) [root](/definitions/x.sqlx) [web](https://example.com) [here](#getting-started)",
		"",
		"![pic](img/a.png) ![remote](https://example.com/b.png)",
		"",
		"```sql",
		"SELECT 1 < 2",
		"```",
	}, "\n")
	files := map[string]bool{"README.md": true, "docs/other.md": true, "definitions/x.sqlx": true}
	out, err := renderMarkdown("docs/guide.md", src, "proj", func(f string) bool { return files[f] })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<h1 id="md-getting-started">`,
		`<h2 id="md-getting-started-1">`,
		`<table>`,
		`<del>2</del>`,
		`type="checkbox"`,
		`<a href="../README.md" data-file="README.md">`,
		`<a href="other.md#part" data-file="docs/other.md">`,
		`<a href="/definitions/x.sqlx" data-file="definitions/x.sqlx">`,
		`<a href="https://example.com">`,
		`<a href="#getting-started">`,
		`src="/api/markdown/image?file=docs%2Fimg%2Fa.png&amp;project=proj"`,
		`src="https://example.com/b.png"`,
		`<pre class="md-code"><code><span class="t-k">SELECT</span>`,
		`&lt;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in\n%s", want, out)
		}
	}
	for _, not := range []string{"<script>", "javascript:", `data-file="docs/missing.md"`} {
		if strings.Contains(out, not) {
			t.Errorf("%s left in\n%s", not, out)
		}
	}
}

func TestProjectLink(t *testing.T) {
	for _, c := range []struct {
		dir, dest, want string
		ok              bool
	}{
		{"docs", "a.md", "docs/a.md", true},
		{"docs", "./a%20b.md", "docs/a b.md", true},
		{".", "docs/a.md?x=1#y", "docs/a.md", true},
		{"docs", "/README.md", "README.md", true},
		{"docs", "../../etc/passwd", "", false},
		{"docs", "..", "", false},
		{"docs", "#top", "", false},
		{"docs", "mailto:a@b.c", "", false},
		{"docs", "//host/x.md", "", false},
	} {
		got, ok := projectLink(c.dir, c.dest)
		if got != c.want || ok != c.ok {
			t.Errorf("projectLink(%q, %q) = %q, %v; want %q, %v", c.dir, c.dest, got, ok, c.want, c.ok)
		}
	}
}

func TestMarkdownPreview(t *testing.T) {
	_, ts, c := newSourceTest(t, map[string]string{"README.md": "# x\n"})
	var got struct{ HTML string }
	if code := postStatus(t, c, ts.URL+"/api/markdown", map[string]string{"file": "README.md", "text": "# unsaved"}, &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(got.HTML, ">unsaved</h1>") {
		t.Errorf("html %q", got.HTML)
	}
	if code := postStatus(t, c, ts.URL+"/api/markdown", map[string]string{"file": "../x.md", "text": "# x"}, nil); code != http.StatusForbidden {
		t.Errorf("a file out of the project: status %d, want 403", code)
	}
}

func TestMarkdownImage(t *testing.T) {
	_, ts, c := newSourceTest(t, map[string]string{
		"docs/a.png": "\x89PNG\r\n\x1a\nnot really",
		"docs/b.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"secret.txt": "nope",
	})
	img := func(file string) *http.Response {
		t.Helper()
		res, err := c.Get(ts.URL + "/api/markdown/image?" + url.Values{"file": {file}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	res := img("docs/a.png")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(string(body), "\x89PNG") {
		t.Errorf("png: %d %s %q", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	res = img("docs/b.svg")
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("svg's CSP %q has no sandbox", csp)
	}
	if res := img("secret.txt"); res.StatusCode != http.StatusForbidden {
		t.Errorf("a text file: status %d, want 403", res.StatusCode)
	}
	if res := img("../a.png"); res.StatusCode != http.StatusForbidden {
		t.Errorf("out of the project: status %d, want 403", res.StatusCode)
	}
}
