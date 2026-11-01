package ui

import "github.com/ashish10alex/xf/internal/frontend"

// The TUI's names for what it shares with xf web (see package frontend).

type (
	Deps        = frontend.Deps
	Engine      = frontend.Engine
	Opening     = frontend.Opening
	RunHistory  = frontend.RunHistory
	Branches    = frontend.Branches
	ProjectInfo = frontend.ProjectInfo
	InfoFile    = frontend.InfoFile
	InfoSetting = frontend.InfoSetting
	InfoGit     = frontend.InfoGit
	GitPart     = frontend.GitPart
)

const (
	costFootnote  = frontend.CostFootnote
	infoTimeout   = frontend.InfoTimeout
	renamedReason = frontend.RenamedReason
)

var (
	actionState    = frontend.ActionState
	aloneMessage   = frontend.AloneMessage
	assertionKind  = frontend.AssertionKind
	countRun       = frontend.CountRun
	countChanged   = frontend.CountChanged
	plural         = frontend.Plural
	firstLine      = frontend.FirstLine
	formatBytes    = frontend.FormatBytes
	formatCost     = frontend.FormatCost
	formatDuration = frontend.FormatDuration
	noSQLMessage   = frontend.NoSQLMessage
	relTime        = frontend.RelTime
	shortPath      = frontend.ShortPath
	splitID        = frontend.SplitID
	runLabel       = frontend.RunLabel
	tagsLabel      = frontend.TagsLabel
	formatDryRun   = frontend.FormatDryRun
	changesSummary = frontend.ChangesSummary
	removed        = frontend.Removed
	renamedFrom    = frontend.RenamedFrom
	apiGitParts    = frontend.APIGitLine
	jobSortNumbers = frontend.JobSortNumbers
)
