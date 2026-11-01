package web

import (
	"errors"
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

// An app window's links open with the system's opener; only web links do,
// and without an opener the page is told to open the link itself.
func TestOpenURL(t *testing.T) {
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	var opened []string
	var fail error
	s.cfg.OpenURL = func(u string) error { opened = append(opened, u); return fail }
	open := func(u string) okView {
		var got okView
		postJSON(t, c, ts.URL+"/api/open-url", map[string]string{"url": u}, &got)
		return got
	}

	if got := open("https://gitlab.com/a/b/-/merge_requests/7"); !got.OK {
		t.Errorf("a web link: %+v", got)
	}
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "https:no-host", ""} {
		if got := open(u); got.OK {
			t.Errorf("%q opened", u)
		}
	}
	if want := []string{"https://gitlab.com/a/b/-/merge_requests/7"}; !slices.Equal(opened, want) {
		t.Errorf("opened %q, want %q", opened, want)
	}

	fail = errors.New("no open")
	if got := open("https://example.com"); got.OK || got.Message == "" {
		t.Errorf("a failed opener: %+v", got)
	}
	s.cfg.OpenURL = nil
	if got := open("https://example.com"); got.OK {
		t.Errorf("no opener: %+v", got)
	}
}
