package check

import (
	"strings"
)

// Marker is the hidden first line of a project's comment, by which a later
// run finds the comment to edit: one comment for each project.
func Marker(project string) string {
	if project == "" {
		project = "."
	}
	return "<!-- xf-check:" + project + " -->"
}

// IsOurs says body is the comment of the project marker names.
func IsOurs(body, marker string) bool {
	first, _, _ := strings.Cut(strings.TrimLeft(body, "\r\n "), "\n")
	return strings.TrimSpace(first) == marker
}

// commitMarker records the commit a report checked, for the folded last
// report of a check that failed later.
const commitMarker = "<!-- xf-check-commit:"

// Body is the comment for a report that is worth one.
func Body(marker, commit, markdown string) string {
	lines := []string{marker}
	if commit != "" {
		lines = append(lines, commitMarker+commit+" -->")
	}
	return strings.Join(append(lines, strings.TrimRight(markdown, "\n"), ""), "\n")
}

// Resolved is the comment an earlier push left once the branch has nothing
// to say.
func Resolved(marker, project string) string {
	return marker + "\n" + resolvedLine(project) + "\n"
}

func resolvedLine(project string) string {
	return "✅ Resolved: **" + Title(project) + "** has nothing to report on this push."
}

// IsResolved says body is already the resolved comment.
func IsResolved(body, marker, project string) bool {
	return strings.TrimSpace(body) == strings.TrimSpace(Resolved(marker, project))
}

// lastReport heads the report a failed check keeps folded.
const lastReport = "<details><summary>Last report"

// FailedBody is the comment an earlier push left when neither check could be
// made on this one: why, with the last report folded below it under the
// commit it checked, so nobody reads old findings as current. previous is
// the comment as it was.
func FailedBody(marker, project, commit, reason, previous string) string {
	lines := []string{marker, "### " + Title(project), "", "❔ **Couldn't check this push" + atCommit(commit) + ":** " + oneLine(reason)}
	if last, at := LastReport(previous); last != "" {
		summary := lastReport + "</summary>"
		if at != "" {
			summary = lastReport + ", of " + short(at) + "</summary>"
		}
		lines = append(lines, "", summary, "", last, "", "</details>")
	}
	return strings.Join(append(lines, ""), "\n")
}

// LastReport is the report a comment holds, and the commit it checked: a
// report's own, or the one a failed check kept folded. A resolved comment
// holds none.
func LastReport(body string) (report, commit string) {
	if i := strings.Index(body, lastReport); i >= 0 {
		rest := body[i:]
		if j := strings.Index(rest, "</summary>"); j >= 0 {
			head := rest[:j]
			if k := strings.Index(head, ", of "); k >= 0 {
				commit = head[k+len(", of "):]
			}
			rest = rest[j+len("</summary>"):]
		}
		rest = strings.TrimSpace(rest)
		rest = strings.TrimSpace(strings.TrimSuffix(rest, "</details>"))
		return rest, commit
	}
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "<!-- xf-check:"):
		case strings.HasPrefix(line, commitMarker):
			commit = strings.TrimSuffix(strings.TrimPrefix(line, commitMarker), " -->")
		default:
			keep = append(keep, line)
		}
	}
	report = strings.TrimSpace(strings.Join(keep, "\n"))
	if strings.HasPrefix(report, "✅ Resolved:") {
		return "", ""
	}
	return report, commit
}

func atCommit(commit string) string {
	if commit == "" {
		return ""
	}
	return " (" + short(commit) + ")"
}

func short(sha string) string { return sha[:min(len(sha), 7)] }

// Action is what --comment does with a project's comment.
type Action int

const (
	// Leave: nothing to post or change.
	Leave Action = iota
	// Post the body as a new comment.
	Post
	// Edit the existing comment to the body.
	Edit
)

// Plan is what to do with the project's comment, given the report and the
// comment an earlier push left ("" for none), and the body to post or edit
// it to.
func Plan(r *Report, o Options, commit, existing string) (Action, string) {
	marker := Marker(r.Project)
	var body string
	switch {
	case r.Neither():
		if existing == "" {
			return Leave, ""
		}
		reason := r.ImpactError
		if reason == "" {
			reason = r.CostError
		}
		body = FailedBody(marker, r.Project, commit, reason, existing)
	case r.Worth(o):
		body = Body(marker, commit, Markdown(r, o))
	case existing == "" || IsResolved(existing, marker, r.Project):
		return Leave, ""
	default:
		body = Resolved(marker, r.Project)
	}
	if existing == "" {
		return Post, body
	}
	if strings.TrimSpace(existing) == strings.TrimSpace(body) {
		return Leave, ""
	}
	return Edit, body
}
