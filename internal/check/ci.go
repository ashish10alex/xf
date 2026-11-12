package check

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/forge"
)

// PR is the merge request a CI job runs for, from the CI's environment.
type PR struct {
	Repo   forge.Repo
	Number int
	// Commit is the commit the job checks: the pull request's head.
	Commit string
	Token  string
}

// GitHubActions says the job runs in GitHub Actions, where notes are
// workflow commands.
func GitHubActions(getenv func(string) string) bool { return getenv("GITHUB_ACTIONS") == "true" }

// ErrNoPR is a CI job that doesn't run for a merge request: a push to a
// branch, say.
var ErrNoPR = errors.New("this job doesn't run for a pull request or merge request")

// ErrNoToken is a merge request with no token to comment on it with. FindPR
// still returns the PR, so the checks run and the job fails after them
// (ADR 0041).
var ErrNoToken = errors.New("no token to comment with")

// FindPR is the merge request of the CI job the environment describes:
// GitHub Actions (the event in GITHUB_EVENT_PATH) or GitLab CI
// (CI_MERGE_REQUEST_IID). The token is XF_FORGE_TOKEN, else GITHUB_TOKEN on
// GitHub; GitLab's job token can't post notes.
func FindPR(getenv func(string) string, readFile func(string) ([]byte, error)) (*PR, error) {
	switch {
	case GitHubActions(getenv):
		data, err := readFile(getenv("GITHUB_EVENT_PATH"))
		if err != nil {
			return nil, fmt.Errorf("reading the event: %w", err)
		}
		var ev struct {
			PullRequest *struct {
				Number int `json:"number"`
				Head   struct {
					SHA string `json:"sha"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, fmt.Errorf("reading the event: %w", err)
		}
		if ev.PullRequest == nil || ev.PullRequest.Number == 0 {
			return nil, ErrNoPR
		}
		repo := forge.RepoAt(forge.GitHub, cmp.Or(getenv("GITHUB_SERVER_URL"), "https://github.com"), getenv("GITHUB_REPOSITORY"))
		if api := getenv("GITHUB_API_URL"); api != "" {
			repo.API = api
		}
		pr := &PR{Repo: repo, Number: ev.PullRequest.Number, Commit: ev.PullRequest.Head.SHA, Token: cmp.Or(getenv("XF_FORGE_TOKEN"), getenv("GITHUB_TOKEN"))}
		if pr.Token == "" {
			return pr, fmt.Errorf("%w: set XF_FORGE_TOKEN or GITHUB_TOKEN", ErrNoToken)
		}
		return pr, nil
	case getenv("GITLAB_CI") == "true":
		iid := getenv("CI_MERGE_REQUEST_IID")
		if iid == "" {
			return nil, ErrNoPR
		}
		n, err := strconv.Atoi(iid)
		if err != nil {
			return nil, fmt.Errorf("CI_MERGE_REQUEST_IID %q isn't a number", iid)
		}
		repo := forge.RepoAt(forge.GitLab, getenv("CI_SERVER_URL"), getenv("CI_PROJECT_PATH"))
		if api := getenv("CI_API_V4_URL"); api != "" {
			repo.API = api
		}
		pr := &PR{Repo: repo, Number: n, Commit: getenv("CI_COMMIT_SHA"), Token: getenv("XF_FORGE_TOKEN")}
		if pr.Token == "" {
			return pr, fmt.Errorf("%w: set XF_FORGE_TOKEN to a project access token with the api scope (GitLab's job token can't post notes)%s", ErrNoToken, hiddenVariable(getenv))
		}
		return pr, nil
	}
	return nil, errors.New("--comment works in GitHub Actions and GitLab CI, and this isn't either")
}

// hiddenVariable says why a GitLab CI/CD variable that is set may still be
// empty in this job: protected, on a branch that isn't, or scoped to an
// environment other than the job's.
func hiddenVariable(getenv func(string) string) string {
	var why []string
	if getenv("CI_COMMIT_REF_PROTECTED") != "true" {
		ref := cmp.Or(getenv("CI_MERGE_REQUEST_SOURCE_BRANCH_NAME"), getenv("CI_COMMIT_REF_NAME"))
		branch := "this branch isn't"
		if ref != "" {
			branch = "`" + ref + "` isn't"
		}
		why = append(why, "it is Protected, which reaches only protected branches, and "+branch)
	}
	if env := getenv("CI_ENVIRONMENT_NAME"); env != "" {
		why = append(why, "its environment scope isn't `*` or `"+env+"`, this job's environment")
	} else {
		why = append(why, "it is scoped to an environment, and this job has none")
	}
	return ". If XF_FORGE_TOKEN is set, this job can't see it when " + strings.Join(why, ", or when ")
}
