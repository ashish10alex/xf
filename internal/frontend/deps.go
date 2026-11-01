// Package frontend is what xf's front ends share: the side effects they
// are given (Deps), and how they find, word and run things, so that xf web
// and the TUI (package ui) show a project the same way. Nothing here
// draws; a front end lays these rows and words out as it likes.
package frontend

import (
	"context"
	"os/exec"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/commanduse"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
	"github.com/ashish10alex/xf/internal/session"
)

// Engine keeps the project compiled (see package engine). main passes the
// real one; tests pass fakes.
type Engine interface {
	// Subscribe returns the engine's state now and then every change to it.
	Subscribe() (<-chan engine.State, func())
	// Recompile asks for a compile without waiting for it.
	Recompile(reason string)
	// Focus asks for an action's SQL to be compiled ahead of the rest of
	// the project, without waiting for it (see engine.Engine.Focus).
	Focus(id project.ActionID)
	// SetTheme makes theme (auto, light or dark) the colour theme every
	// client shows (see engine.Engine.SetTheme).
	SetTheme(theme string)
}

// Deps are the side effects a front end needs. main wires them to the
// engine and internal/dataform; tests pass fakes.
type Deps struct {
	// Theme is the saved colour theme: auto (follow the terminal's
	// background), light or dark; "" is auto.
	Theme string
	// SaveTheme saves a theme the user chose to their config file; nil
	// keeps it for this run only.
	SaveTheme func(theme string) error
	// CommandUses counts the palette's commands as they are done, which
	// the palette lists most used first; nil keeps them in their order.
	CommandUses *commanduse.Store
	// Backend names the project's tool, as backend.Backend.Name does:
	// "dataform" (the default) or "dbt". The status bar shows it.
	Backend string
	// Root is the project root, shown in the status bar.
	Root string
	// Target is the dbt target the project compiles with, shown in the
	// status bar; "" when dbt chooses it.
	Target string
	// Engine compiles the project and says when it has.
	Engine Engine
	// Now returns the current time (for the compile animation's timer).
	Now func() time.Time
	// StartupQuiet is how long after starting keypresses are ignored: a
	// terminal's replies to xf's startup queries (background colour, modes,
	// keyboard protocol) can arrive split, and their tail then reads as keys,
	// such as "1u" from the keyboard reply, where u opens the browser. 0
	// ignores none.
	StartupQuiet time.Duration
	// StartRun starts a run with the project's tool (`dataform run`, `dbt
	// build`); nil hides the Run pane. Cancelling ctx cancels the run.
	StartRun func(ctx context.Context, opts project.RunOptions) (*project.Run, error)
	// RunCommand is the command line StartRun runs with opts, shown in the
	// footer and at the top of the run log; nil shows the dataform one.
	RunCommand func(opts project.RunOptions) string
	// StartAPIRun runs an action in GCP through the Dataform API (see
	// package dataformapi); nil hides the a toggle. The UI fills in the
	// project, location and invocation; main the rest. Cancelling ctx asks
	// Dataform to cancel the invocation, and Run.Kill stops watching it.
	StartAPIRun func(ctx context.Context, opts dataformapi.Options) (*project.Run, error)
	// WatchWorkflow follows a workflow invocation xf did not start (see
	// dataformapi.Watch): ref names it, and base is the project's own
	// repository, which fills in what ref leaves out. Cancelling ctx, like
	// Run.Kill, stops watching; the invocation is never cancelled. Nil
	// hides the command.
	WatchWorkflow func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error)
	// RunAPI starts the a toggle on (--run-mode api).
	RunAPI bool
	// APIGit reads the checkout as an API run sees it, for the line under
	// the toggles while a is on; nil shows the line without it.
	APIGit func(ctx context.Context) (dataformapi.GitState, error)
	// Git reads the checkout for the branch in the status line; nil, or
	// an error (not a repository), leaves it out.
	Git func(ctx context.Context) (dataformapi.GitState, error)
	// Branches lists and switches the checkout's branches, for B; nil
	// hides B.
	Branches Branches
	// ChangedActions works out the Changed Actions (see
	// engine.Engine.ChangedActions); nil hides R.
	ChangedActions func(ctx context.Context, req changes.Request) (*changes.Result, error)
	// ColumnImpact works out the column impact of the Changed Actions (see
	// engine.Engine.ColumnImpact), telling progress how far it got; nil
	// hides I.
	ColumnImpact func(ctx context.Context, req engine.ImpactRequest, progress func(impact.Progress)) (*impact.Report, error)
	// QueryCost estimates what the Changed Actions would scan (see
	// engine.Engine.QueryCost), telling progress how far it got; nil hides
	// C.
	QueryCost func(ctx context.Context, req engine.CostRequest, progress func(cost.Progress)) (*cost.Report, error)
	// Deferral asks the engine what defer to prod reads from prod (see
	// engine.Engine.Deferral, ADR 0031); nil hides defer to prod.
	Deferral func(ctx context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error)
	// SetDefer turns defer to prod on or off for every client of the
	// engine.
	SetDefer func(on bool)
	// DeferUnreadable tells the engine of prod tables a query was denied,
	// so the next Deferral reads them from dev.
	DeferUnreadable func(ctx context.Context, ts []project.Target) (bool, error)
	// DeferWriter makes and removes Proxy Views before and after deferred
	// runs; nil can't, and a deferred run is refused.
	DeferWriter func(ctx context.Context) (deferral.Writer, error)
	// DevCompilerOptions are the compiler options of the user's dev runs
	// (devCompilerOptions), which I folds into the actions they run.
	DevCompilerOptions string
	// ProdCompilerOptions are the compiler options of the project's prod
	// runs (prodCompilerOptions), whose tables I compares with and C dry
	// runs against.
	ProdCompilerOptions string
	// DbtProdTarget is the profile target of a dbt project's prod runs
	// (dbtProdTarget), which I compares with and C dry runs against.
	DbtProdTarget string
	// Unset is the setting that picks the tables I compares with and C dry
	// runs against, "prodCompilerOptions" or "dbtProdTarget", when nothing
	// sets it, not even to "": their reports then say they used the
	// defaults, which may be dev tables.
	Unset string
	// SaveProdOptions sets prodCompilerOptions in the project's config
	// file, once the project compiles with them; "" says the default
	// targets are prod. Nil never asks for them.
	SaveProdOptions func(ctx context.Context, options string) error
	// DefaultBranch is the branch R, I and C compare against
	// (defaultBranch): the base, which b changes.
	DefaultBranch string
	// SaveBranch makes branch the project's defaultBranch in its config
	// file (config.SetProjectBranch). Nil when that file isn't the one
	// read (--config): b then changes the base for the session only.
	SaveBranch func(branch string) error
	// DryRun asks BigQuery to plan sql in a job in project and location,
	// without running it; nil turns dry runs off. main passes the engine's
	// cached DryRun.
	DryRun func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)
	// ForgetFailedDryRuns has whatever caches DryRun's answers try the
	// failed ones again, after a run that may have built the tables they
	// read; nil when DryRun keeps nothing.
	ForgetFailedDryRuns func()
	// Table fetches the BigQuery metadata of project.dataset.table, for
	// when an action's table was last updated; a table that doesn't exist
	// is a *bigquery.APIError with Status 404. Nil hides the line.
	Table func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error)
	// Editor reports the file active in the editor (see nvim/plugin/xf.lua);
	// nil disables following the editor.
	Editor <-chan project.EditorEvent
	// Notice is shown in the footer at startup (e.g. why watching is off).
	Notice string
	// Beside says xf runs beside the editor (in a neovim split), which it
	// follows: rather than open the palette after the first compile, it
	// shows the editor's file, or failing that the first action.
	Beside bool
	// Open opens a project file (an absolute path) at a 0-based line in
	// the editor: the one used last, through its xf lsp, or the neovim xf
	// runs beside (see nvim/lua/xf.lua), or failing those the editor
	// setting's; nil hides the o key.
	Open func(file string, line int) (Opening, error)
	// FullScreen shows xf full screen in the editor it runs beside, or
	// puts it back in its split; nil hides the Z key.
	FullScreen func() error
	// Runs is the project's kept runs, for the log pane's jobs and history
	// tabs; nil leaves the pane without them.
	Runs RunHistory
	// OpenURL opens a URL in the browser (an API run's workflow
	// invocation); nil hides the u key.
	OpenURL func(url string) error
	// Resume is what the last xf in this project showed when it quit: its
	// action is selected again after the first compile, unless the editor
	// xf runs beside has a file to show.
	Resume session.Session
	// Info is what the , overlay shows of the project and of this xf; nil
	// hides the , key.
	Info *ProjectInfo
	// Copy puts text on the system clipboard. Copies are also sent with OSC
	// 52, so nil still works in terminals that support it.
	Copy func(string) error
}

// Opening is what is left for xf to do once it has asked for a file or a
// project to be opened in an editor: nothing, when the editor has it.
type Opening struct {
	// Run is a terminal editor, which xf runs in its own terminal until
	// it exits.
	Run *exec.Cmd
	// Leave says xf should quit so its caller can replace it with the
	// editor: neovim with xf in a split beside it.
	Leave bool
}

// RunHistory is the project's kept runs.
type RunHistory interface {
	// List is the kept runs, newest first.
	List() ([]runhistory.Entry, error)
	// Refresh looks up what has become of a run that was never seen to
	// end, and keeps it as it is known now.
	Refresh(ctx context.Context, e runhistory.Entry) (runhistory.Entry, error)
	// Clear forgets the kept runs, but for one that is still going.
	Clear() error
}

// Branches lists and switches the checkout's branches (gitbranch.Repo).
type Branches interface {
	List(ctx context.Context) (gitbranch.Listing, error)
	Switch(ctx context.Context, name string) error
	Fetch(ctx context.Context) error
	Watch(ctx context.Context, debounce time.Duration) (<-chan struct{}, error)
}

// ProjectInfo is what the , overlay shows of the project and of this xf,
// as main knew it when xf started: the settings are the ones this xf
// runs with, whatever the config files say now.
type ProjectInfo struct {
	// BackendFrom says how the project's tool was chosen: "detected" or
	// "--backend".
	BackendFrom string
	// Target is the dbt target and TargetFrom where it came from, e.g.
	// "$DBT_TARGET"; "" for a project that has none.
	Target, TargetFrom string
	// Exe is xf's own executable and Build what Go knows of its build.
	Exe, Build string
	// ToolBin is the tool's binary and ToolFrom where xf found it, e.g.
	// "PATH".
	ToolBin, ToolFrom string
	// Format is the sqlfluff that formats SQL (ADR 0024), shown beside
	// the tool's binary; nil leaves it out.
	Format *InfoBin
	// Files are the config files looked for, lowest precedence first.
	Files []InfoFile
	// Settings are the settings that apply to the project's tool.
	Settings []InfoSetting
	// Warnings are the config files' warnings.
	Warnings []string
	// EditorCommand is the command o opens files with when no editor is
	// connected.
	EditorCommand string
	// Socket is where the project's xf serve listens, and Log its log.
	Socket, Log string
	// ToolVersion asks the tool's binary for its version; nil leaves the
	// version out. It is asked each time the overlay opens.
	ToolVersion func(ctx context.Context) (string, error)
	// Git reads the checkout's state; nil leaves the section out. It is
	// read each time the overlay opens.
	Git func(ctx context.Context) (InfoGit, error)
	// ADC signs in with the Application Default Credentials xf's Google
	// calls use, and Gcloud asks the gcloud CLI for its account; nil
	// leaves either out. They are asked each time the overlay opens.
	ADC    func(ctx context.Context) gcpauth.ADC
	Gcloud func(ctx context.Context) gcpauth.CLI
}

// InfoBin is a program xf runs besides the project's tool: where it was
// found, from what, and (asked each time the overlay opens; nil for none)
// its version.
type InfoBin struct {
	Bin, From string
	Version   func(ctx context.Context) (string, error)
	// Config is the config file it formats with and ConfigFrom what chose
	// it; "" leaves the row out.
	Config, ConfigFrom string
}

// InfoFile is a config file xf looked for.
type InfoFile struct {
	Path  string
	Found bool
}

// InfoSetting is a setting, its value and where the value came from: a
// config file, a flag, the environment or a default.
type InfoSetting struct {
	Key, Value, From string
}

// InfoGit is the state of the project's checkout.
type InfoGit struct {
	Branch string // "" when HEAD is detached
	Origin string // URL of the origin remote, "" when there is none
}

// GitPart is a piece of the git line: its text, its tone ("", "dim",
// "warn" or "error") and, for a count, what it counts (files or
// commits), which xf web shows on hover.
type GitPart struct {
	Text   string   `json:"text"`
	Tone   string   `json:"tone,omitempty"`
	Detail []string `json:"detail,omitempty"`
}
