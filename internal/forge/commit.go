package forge

import (
	"context"
	"errors"
)

// CommitURL is the host's page for commit sha.
func CommitURL(repo Repo, sha string) string {
	if repo.Kind == GitLab {
		return repo.Web + "/-/commit/" + sha
	}
	return repo.Web + "/commit/" + sha
}

// pickMR is the merge request to show of those a commit is in: a merged
// one first (the one that brought it in), else the first the host lists.
func pickMR(list []MR) *MR {
	for i := range list {
		if list[i].State == "merged" {
			return &list[i]
		}
	}
	if len(list) > 0 {
		return &list[0]
	}
	return nil
}

// CommitMR is the merge request that brought commit sha in, else an open
// one it is in; nil for none, or a commit the host doesn't have (not
// pushed).
func (g *gitlab) CommitMR(ctx context.Context, repo Repo, sha string) (*MR, error) {
	var list []glMR
	if err := g.api(repo).get(ctx, project(repo)+"/repository/commits/"+sha+"/merge_requests", &list); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	mrs := make([]MR, 0, len(list))
	for _, m := range list {
		if m.SourceProjectID == 0 || m.SourceProjectID == m.ProjectID {
			mrs = append(mrs, m.mr())
		}
	}
	return pickMR(mrs), nil
}

// CommitMR is GitLab's, of GitHub's pull requests.
func (g *github) CommitMR(ctx context.Context, repo Repo, sha string) (*MR, error) {
	var list []ghPR
	if err := g.api(repo).get(ctx, repoURL(repo)+"/commits/"+sha+"/pulls", &list); err != nil {
		if errors.Is(err, errNotFound) || errors.As(err, new(unprocessableError)) {
			return nil, nil
		}
		return nil, err
	}
	mrs := make([]MR, 0, len(list))
	for _, p := range list {
		mrs = append(mrs, p.mr())
	}
	return pickMR(mrs), nil
}
