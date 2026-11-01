package web

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
)

// With ?stream=1 the project's info comes as lines: the first before a
// slow gcloud answers, with "…" for it, and the last with its answer.
func TestInfoStream(t *testing.T) {
	gcloud := make(chan struct{})
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), Info: &frontend.ProjectInfo{
		ToolBin: "/bin/dataform",
		Gcloud: func(context.Context) gcpauth.CLI {
			<-gcloud
			return gcpauth.CLI{Account: "me@work.example"}
		},
	}})
	res, err := c.Get(ts.URL + "/api/info?stream=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", ct)
	}
	gcloudRow := func(rows []frontend.InfoRow) string {
		for _, r := range rows {
			if r.Label == "gcloud" {
				return r.Value
			}
		}
		return "(none)"
	}
	lines := bufio.NewScanner(res.Body)
	next := func() []frontend.InfoRow {
		t.Helper()
		got := make(chan bool, 1)
		go func() { got <- lines.Scan() }()
		select {
		case ok := <-got:
			if !ok {
				t.Fatalf("the stream ended: %v", lines.Err())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no line came")
		}
		var rows []frontend.InfoRow
		if err := json.Unmarshal(lines.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if got := gcloudRow(next()); got != "…" {
		t.Errorf("first line's gcloud = %q, want …", got)
	}
	close(gcloud)
	if got := gcloudRow(next()); got != "me@work.example" {
		t.Errorf("last line's gcloud = %q", got)
	}
	if lines.Scan() {
		t.Errorf("a line after the last: %s", lines.Bytes())
	}
}

// Chrome's "Install page as app" makes an app of its own, with xf's icon,
// only when the page links a manifest whose icons it can fetch.
func TestManifestAndIconsServed(t *testing.T) {
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	for path, typ := range map[string]string{
		"/manifest.webmanifest": "application/manifest+json",
		"/icon.svg":             "image/svg+xml",
		"/icon-192.png":         "image/png",
		"/icon-512.png":         "image/png",
	} {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(got, typ) {
			t.Errorf("%s: %d, %q; want 200, %s", path, resp.StatusCode, got, typ)
		}
	}
}
