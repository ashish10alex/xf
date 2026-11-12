// Package gitlab holds xf check's GitLab CI template, built into xf so that
// xf check init gitlab can copy it into a repository (ADR 0041).
package gitlab

import _ "embed"

// Template is ci/gitlab/xf-check.yml.
//
//go:embed xf-check.yml
var Template string
