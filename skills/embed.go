// Package skills holds xf's Agent Skills (xf-checks, xf-run), built into
// xf so that xf skills install writes the ones that match its flags.
package skills

import "embed"

// FS holds each skill's folder, with its SKILL.md.
//
//go:embed */SKILL.md
var FS embed.FS
