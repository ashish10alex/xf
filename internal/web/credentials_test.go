package web

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/frontend"
)

// A new Google login has the failed dry runs asked again, with no
// compile, and every page told why: found as a page comes back into focus
// (POST /api/credentials), or as one asks for an action's details.
func TestNewLoginRetriesFailedDryRuns(t *testing.T) {
	for _, via := range []string{"focus", "details"} {
		t.Run(via, func(t *testing.T) {
			var gen atomic.Uint64
			var loggedIn atomic.Bool
			dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
				if !loggedIn.Load() {
					return nil, bigquery.ErrAuth
				}
				return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}, nil
			}
			s, ts, c := newTestConfig(t, Config{
				Project:     &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), DryRun: dry}},
				Credentials: gen.Load,
			})
			dryLine := func() string {
				var v actionView
				getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders", &v)
				for _, a := range v.Pane {
					if a.ID == "p.mart.orders" {
						for _, d := range a.Details {
							if d.Dry {
								return d.Value
							}
						}
					}
				}
				return ""
			}
			waitFor(t, func() bool { return dryLine() == "no credentials" })

			events, ok := s.hub.subscribe()
			if !ok {
				t.Fatal("no event stream")
			}
			defer s.hub.unsubscribe(events)
			loggedIn.Store(true)
			if dryLine() != "no credentials" {
				t.Fatal("a failed dry run was asked again with the same login")
			}

			told := func() {
				t.Helper()
				deadline := time.After(5 * time.Second)
				for {
					select {
					case ev := <-events:
						if ev.name == "credentials" {
							if !strings.Contains(string(ev.data), credsChangedText) {
								t.Errorf("credentials event = %s", ev.data)
							}
							return
						}
					case <-deadline:
						t.Fatal("the pages were not told of the new login")
					}
				}
			}

			gen.Add(1)
			if via == "focus" {
				resp, err := c.Post(ts.URL+"/api/credentials", "application/json", strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					t.Fatalf("POST /api/credentials: %s", resp.Status)
				}
				told() // before any page asks for details
			}
			waitFor(t, func() bool { return strings.Contains(dryLine(), "MiB") })
			if via == "details" {
				told()
			}
		})
	}
}
