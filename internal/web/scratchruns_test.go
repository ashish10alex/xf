package web

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

func TestScratchRunsKeepTheLastFive(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	root, ts, c := newScratchTest(t, frontend.Deps{}, bq)

	var a, b scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"form": "empty", "project": "p"}, &a)
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"form": "empty", "project": "p"}, &b)

	var runs []scratchRun
	getJSON(t, c, ts.URL+"/api/scratch/runs", &runs)
	if len(runs) != 0 {
		t.Fatalf("runs before any = %+v", runs)
	}
	add := func(scratch, job string) {
		t.Helper()
		postJSON(t, c, ts.URL+"/api/scratch/runs", map[string]any{
			"scratch": scratch, "job": map[string]string{"project": "p", "location": "EU", "id": job},
			"sql": "SELECT " + job, "rows": 2, "started": "2026-10-25T10:00:00Z", "ended": "2026-10-25T10:00:01Z",
		}, &runs)
	}
	for i := range 6 {
		add(a.ID, fmt.Sprintf("job%d", i))
	}
	add(b.ID, "jobB")
	add(a.ID, "job4") // run again: moves to the front, kept once
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.Job.ID)
	}
	if got := strings.Join(ids, " "); got != "job4 jobB job5 job3 job2" {
		t.Errorf("runs = %s, want the newest five, newest first", got)
	}

	// Kept beside the scratches, in their git-ignored folder.
	data, err := os.ReadFile(filepath.Join(root, ".xf", "tmp", "runs.json"))
	if err != nil || !strings.Contains(string(data), `"jobB"`) {
		t.Errorf("runs.json = %q, %v", data, err)
	}

	// Deleting a scratch forgets its runs.
	postJSON(t, c, ts.URL+"/api/scratch/delete", map[string]any{"id": a.ID}, nil)
	getJSON(t, c, ts.URL+"/api/scratch/runs", &runs)
	if len(runs) != 1 || runs[0].Job.ID != "jobB" {
		t.Errorf("after deleting a scratch = %+v", runs)
	}

	// A run needs a scratch and a job.
	for _, body := range []string{`{"scratch":"../x","job":{"project":"p","id":"j"}}`, `{"scratch":"` + b.ID + `","job":{"project":"p"}}`} {
		res, err := c.Post(ts.URL+"/api/scratch/runs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode == 200 {
			t.Errorf("POST %s was kept", body)
		}
	}
}

func TestScratchRowsOfAGoneResult(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	res, err := c.Get(ts.URL + "/api/scratch/rows?project=p&location=EU&id=expired")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	var v struct{ Error string }
	_ = json.Unmarshal(body, &v)
	if !strings.Contains(v.Error, "about 24 hours") {
		t.Errorf("rows of a gone result = %s %s", res.Status, body)
	}
}
