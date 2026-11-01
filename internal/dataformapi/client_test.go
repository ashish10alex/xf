package dataformapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

var testRepo = Repository{Project: "proj", Location: "europe-west2", Name: "shop"}

const repoPath = "/v1beta1/projects/proj/locations/europe-west2/repositories/shop"

// apiServer serves the handlers by "METHOD path" (path with query string).
func apiServer(t *testing.T, handlers map[string]func(body map[string]any) (int, string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		h, ok := handlers[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
			return
		}
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("%s: body is not JSON: %s", key, b)
			}
		}
		status, resp := h(body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.Client(), srv.URL, "")
}

func TestCompileInvokePollCancel(t *testing.T) {
	const inv = "projects/proj/locations/europe-west2/repositories/shop/workflowInvocations/123"
	c := apiServer(t, map[string]func(map[string]any) (int, string){
		"POST " + repoPath + "/compilationResults": func(body map[string]any) (int, string) {
			if body["gitCommitish"] != "feat-x" {
				t.Errorf("gitCommitish = %v", body["gitCommitish"])
			}
			if want := map[string]any{"schemaSuffix": "ashish"}; !reflect.DeepEqual(body["codeCompilationConfig"], want) {
				t.Errorf("codeCompilationConfig = %v, want %v", body["codeCompilationConfig"], want)
			}
			return 200, `{"name":"cr/1","resolvedGitCommitSha":"abc1234def","dataformCoreVersion":"3.0.26",
				"compilationErrors":[{"message":"bad ref","path":"definitions/a.sqlx","actionTarget":{"schema":"s","name":"a"}}]}`
		},
		"POST " + repoPath + "/workflowInvocations": func(body map[string]any) (int, string) {
			want := map[string]any{
				"compilationResult": "cr/1",
				"invocationConfig": map[string]any{
					"includedTargets":                []any{map[string]any{"database": "proj", "schema": "s", "name": "a"}},
					"includedTags":                   []any{"daily"},
					"transitiveDependenciesIncluded": true,
				},
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("invocation body = %v, want %v", body, want)
			}
			return 200, `{"name":"` + inv + `","state":"RUNNING"}`
		},
		"GET /v1beta1/" + inv: func(map[string]any) (int, string) {
			return 200, `{"name":"` + inv + `","state":"SUCCEEDED","invocationTiming":{"startTime":"2026-10-05T10:00:00Z","endTime":"2026-10-05T10:01:15Z"}}`
		},
		"GET /v1beta1/" + inv + ":query?pageSize=1000": func(map[string]any) (int, string) {
			return 200, `{"workflowInvocationActions":[{"target":{"schema":"s","name":"a"},"state":"RUNNING"}],"nextPageToken":"p2"}`
		},
		"GET /v1beta1/" + inv + ":query?pageSize=1000&pageToken=p2": func(map[string]any) (int, string) {
			return 200, `{"workflowInvocationActions":[{"target":{"schema":"s","name":"b"},"state":"FAILED","failureReason":"boom","bigqueryAction":{"sqlScript":"select 1","jobId":"proj:EU.job_b"},"invocationTiming":{"startTime":"2026-10-05T10:00:10Z","endTime":"2026-10-05T10:00:12Z"}}]}`
		},
		"POST /v1beta1/" + inv + ":cancel": func(body map[string]any) (int, string) {
			return 200, `{}`
		},
	})
	ctx := context.Background()

	cr, err := c.CreateCompilationResult(ctx, testRepo, "feat-x", CompilationConfig{SchemaSuffix: "ashish"})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Name != "cr/1" || cr.ResolvedGitCommitSHA != "abc1234def" || len(cr.Errors) != 1 || cr.Errors[0].ActionTarget.Name != "a" {
		t.Errorf("compilation result = %+v", cr)
	}

	got, err := c.CreateWorkflowInvocation(ctx, testRepo, "cr/1", InvocationConfig{
		IncludedTargets:                []Target{{Database: "proj", Schema: "s", Name: "a"}},
		IncludedTags:                   []string{"daily"},
		TransitiveDependenciesIncluded: true,
	})
	if err != nil || got.Name != inv {
		t.Fatalf("invocation = %+v, %v", got, err)
	}

	st, err := c.GetWorkflowInvocation(ctx, inv)
	if err != nil || !st.Done() || st.State != StateSucceeded || st.Timing.EndTime != "2026-10-05T10:01:15Z" {
		t.Errorf("get = %+v, %v", st, err)
	}

	actions, err := c.QueryWorkflowInvocationActions(ctx, inv)
	if err != nil || len(actions) != 2 || actions[1].FailureReason != "boom" ||
		actions[1].BigQuery.JobID != "proj:EU.job_b" || actions[1].Timing.StartTime != "2026-10-05T10:00:10Z" || actions[0].BigQuery.JobID != "" {
		t.Errorf("actions = %+v, %v", actions, err)
	}

	if err := c.CancelWorkflowInvocation(ctx, inv); err != nil {
		t.Error(err)
	}
}

func TestErrors(t *testing.T) {
	c := apiServer(t, map[string]func(map[string]any) (int, string){
		"POST " + repoPath + "/compilationResults": func(map[string]any) (int, string) {
			return 404, `{"error":{"message":"Repository not found"}}`
		},
		"GET /v1beta1/x": func(map[string]any) (int, string) {
			return 401, `{"error":{"message":"Request had invalid authentication credentials."}}`
		},
	})
	_, err := c.CreateCompilationResult(context.Background(), testRepo, "main", CompilationConfig{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Message != "Repository not found" {
		t.Errorf("err = %v, want a 404 APIError", err)
	}
	if _, err := c.GetWorkflowInvocation(context.Background(), "x"); !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestConsoleURL(t *testing.T) {
	got := testRepo.ConsoleURL("projects/proj/locations/europe-west2/repositories/shop/workflowInvocations/1727-abc")
	want := "https://console.cloud.google.com/bigquery/dataform/locations/europe-west2/repositories/shop/workflows/1727-abc?project=proj"
	if got != want {
		t.Errorf("ConsoleURL = %s, want %s", got, want)
	}
}

func TestFindRepository(t *testing.T) {
	notFound := func(map[string]any) (int, string) { return 404, `{"error":{"message":"not found"}}` }
	c := apiServer(t, map[string]func(map[string]any) (int, string){
		"GET /v1beta1/projects/proj/locations?pageSize=1000": func(map[string]any) (int, string) {
			return 200, `{"locations":[{"locationId":"us-central1"},{"locationId":"europe-west2"}],"nextPageToken":"p2"}`
		},
		"GET /v1beta1/projects/proj/locations?pageSize=1000&pageToken=p2": func(map[string]any) (int, string) {
			return 200, `{"locations":[{"locationId":"asia-south1"}]}`
		},
		"GET /v1beta1/projects/proj/locations/us-central1/repositories/shop": notFound,
		"GET /v1beta1/projects/proj/locations/asia-south1/repositories/shop": func(map[string]any) (int, string) {
			return 403, `{"error":{"message":"denied"}}`
		},
		"GET /v1beta1/projects/proj/locations/europe-west2/repositories/shop": func(map[string]any) (int, string) {
			return 200, `{"name":"projects/proj/locations/europe-west2/repositories/shop"}`
		},
	})
	got, err := c.FindRepository(context.Background(), "proj", "shop")
	if err != nil || !reflect.DeepEqual(got, []string{"europe-west2"}) {
		t.Errorf("FindRepository = %v, %v", got, err)
	}
}

func TestLooksLikeLocationError(t *testing.T) {
	for _, tt := range []struct {
		err      error
		location string
		want     bool
	}{
		{&APIError{Status: 403, Message: "Location EU is not found or access is unauthorized."}, "EU", true},
		{&APIError{Status: 404, Message: "Repository not found"}, "US", true},
		{&APIError{Status: 404, Message: "Repository not found"}, "europe-west2", false},
		{&APIError{Status: 500, Message: "Location backend error"}, "EU", false},
		{errors.New("dial tcp: timeout"), "EU", false},
	} {
		if got := LooksLikeLocationError(tt.err, tt.location); got != tt.want {
			t.Errorf("LooksLikeLocationError(%v, %q) = %v", tt.err, tt.location, got)
		}
	}
}
