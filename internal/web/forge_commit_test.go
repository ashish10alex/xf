package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/forge"
)

// The blame card's commit: its page on the host and the merge request
// that brought it in, asked of the host once and then kept.
func TestForgeCommit(t *testing.T) {
	_, ts, c, host, _, _ := forgeTest(t)
	sha := strings.Repeat("ab", 20)
	host.mu.Lock()
	host.commitMRs = map[string]*forge.MR{sha: {Number: 9, Title: "Feat", State: "merged", URL: "https://gitlab.example.com/group/proj/-/merge_requests/9"}}
	host.mu.Unlock()

	var v commitView
	getJSON(t, c, ts.URL+"/api/forge/commit?sha="+sha, &v)
	if v.State != "ok" || v.URL != "https://gitlab.example.com/group/proj/-/commit/"+sha || v.MR == nil || v.MR.Number != 9 {
		t.Fatalf("commit = %+v", v)
	}
	v = commitView{}
	getJSON(t, c, ts.URL+"/api/forge/commit?sha="+sha, &v)
	host.mu.Lock()
	calls := host.commitCalls
	host.mu.Unlock()
	if v.MR == nil || calls != 1 {
		t.Errorf("asked the host %d times for one commit, want once (then kept): %+v", calls, v)
	}

	other := strings.Repeat("cd", 20)
	v = commitView{}
	getJSON(t, c, ts.URL+"/api/forge/commit?sha="+other, &v)
	if v.State != "ok" || v.MR != nil || v.URL == "" {
		t.Errorf("a commit with no merge request = %+v", v)
	}
	if code := getStatus(t, c, ts.URL+"/api/forge/commit?sha=HEAD"); code != http.StatusBadRequest {
		t.Errorf("a sha that isn't one = %d, want 400", code)
	}
}
