// Package config reads xf's settings from JSON files: the user's
// ($XDG_CONFIG_HOME/xf/config.json, else ~/.config/xf/config.json) and the
// project's (.xf/config.json at the project root, meant to be committed).
// The project file wins over the user file; main applies flags on top.
package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ashish10alex/xf/internal/webtheme"
)

// Keys, which mirror the flags of the same names.
const (
	KeyDataformBin = "dataformBin"
	KeyDbtBin      = "dbtBin"
	// KeyDbtTarget is the profile target dbt compiles with (--target).
	KeyDbtTarget = "dbtTarget"
	// KeyDbtProdTarget is the profile target of the project's prod runs,
	// e.g. prod: column impact and query cost compile with it, so they
	// compare with and dry run against the tables prod builds.
	KeyDbtProdTarget = "dbtProdTarget"
	// KeyDbtCompileWithHooks compiles dbt v2 projects that have
	// on-run-start or on-run-end hooks, which dbt v2 runs when it compiles.
	KeyDbtCompileWithHooks = "dbtCompileWithHooks"
	KeyRunMode             = "runMode"
	KeyDataformLocation    = "dataformLocation"
	KeyDataformRepo        = "dataformRepo"
	KeyDefaultBranch       = "defaultBranch"
	// KeyDevCompilerOptions are the compiler options of the user's dev
	// runs, e.g. --schema-suffix=ashish, so column impact can fold their
	// lineage into the actions they run.
	KeyDevCompilerOptions = "devCompilerOptions"
	// KeyProdCompilerOptions are the compiler options of the project's
	// prod runs, e.g. --default-database=acme-prod, so column impact
	// compares with the tables they build rather than the default targets.
	KeyProdCompilerOptions = "prodCompilerOptions"
	// KeyTablePrefix, KeySchemaSuffix and KeyDatabaseSuffix are what a
	// Dataform project's compiles and runs add to every name it builds:
	// --table-prefix, --schema-suffix and --database-suffix, which
	// workflow_settings.yaml calls namePrefix, datasetSuffix and
	// projectSuffix. Dataform joins each with an underscore.
	KeyTablePrefix    = "tablePrefix"
	KeySchemaSuffix   = "schemaSuffix"
	KeyDatabaseSuffix = "databaseSuffix"
	// KeyEditor is the command o opens files with when no editor is
	// connected, e.g. code or nvim.
	KeyEditor = "editor"
	// KeyTheme is the colour theme of the TUI: auto, light or dark.
	// KeyWebTheme is xf web's: auto or one of its themes (webtheme.All),
	// and KeyWebThemeLight and KeyWebThemeDark the themes its auto shows
	// while the browser is light or dark. The TUI's is the user's to set
	// (the user's config file); xf web's can also be a project's, which
	// wins over the user's. Neither follows the other: the TUI goes by the
	// terminal, xf web by the browser.
	KeyTheme         = "theme"
	KeyWebTheme      = "webTheme"
	KeyWebThemeLight = "webThemeLight"
	KeyWebThemeDark  = "webThemeDark"
	// KeyScratchWarnGB and KeyScratchConfirmGB are the sizes, in GiB, of a
	// scratch query's dry run at which xf web turns it amber, and red and
	// asks before running it.
	KeyScratchWarnGB    = "scratchWarnGB"
	KeyScratchConfirmGB = "scratchConfirmGB"
	// KeySourceAutosaveMs turns on autosave in xf web's source pane: the
	// milliseconds it waits after an edit before saving; 0 or unset is
	// off.
	KeySourceAutosaveMs = "sourceAutosaveMs"
	// KeyDiffStyle is how xf web's Source Control shows a file's
	// difference to start with: "inline" (the default) or "side-by-side".
	KeyDiffStyle = "diffStyle"
	// KeyPythonLanguageServer is the language server xf web's source pane
	// checks Python files with: "pyrefly" (the default) or "off".
	KeyPythonLanguageServer = "pythonLanguageServer"
	// KeySqlfluffBin is the sqlfluff xf formats SQL with, and
	// KeySqlfluffConfig a config file for it to use in place of the
	// project's own (ADR 0024).
	KeySqlfluffBin    = "sqlfluffBin"
	KeySqlfluffConfig = "sqlfluffConfig"
	// KeyDeferToProd is whether defer to prod starts on (ADR 0031):
	// "false" (the default) or "true". Turning it on or off in xf web is
	// remembered per project and wins over this.
	KeyDeferToProd = "deferToProd"
	// KeyAutoHidePane2 hides xf web's pane 2 while the source pane shows a
	// file with no action, e.g. a Python script or a CI file: "true" (the
	// default) or "false".
	KeyAutoHidePane2 = "autoHidePane2"
	// KeyPane2Tab is the tab xf web's pane 2 starts on, "sql" (the
	// default) or "columns". It is the user's to set, as the themes are.
	KeyPane2Tab = "pane2Tab"
	// KeyWrapLines wraps long lines in xf web's editors (the source pane,
	// Scratch, the settings' JSON) rather than scrolling them sideways:
	// "false" (the default) or "true". It is the user's to set.
	KeyWrapLines = "wrapLines"
	// KeyWebAutoExitSeconds is how long xf web waits, once every browser
	// window has closed, before it stops; 0 is off. It is the user's to
	// set, as the themes are.
	KeyWebAutoExitSeconds = "webAutoExitSeconds"
	// KeyWebOpenProjectIn is where xf web opens a project picked from a
	// project's page: "same-tab" (the default) or "new-tab". It is the
	// user's to set.
	KeyWebOpenProjectIn = "webOpenProjectIn"
	// KeyWebBrowser is what xf web opens its page in: "default" (the
	// default) the system's browser, "app" an app window of the first
	// Chrome, Edge or Chromium found, else a Chromium browser's path or
	// name to open the app window in. An app window gets ctrl+n, which a
	// tab off a Mac never does. It is the user's to set.
	KeyWebBrowser = "webBrowser"
	// KeyWebBrowserProfile is the Chromium profile directory, e.g.
	// "Profile 1", that xf web's app window opens in, and its links with
	// it; empty (the default) leaves it to the browser, which picks the
	// profile last used. It is the user's to set.
	KeyWebBrowserProfile = "webBrowserProfile"
	// KeyForgePollSeconds is how often xf web asks GitLab or GitHub again
	// while the branch's pipeline runs, in seconds, at least
	// MinForgePollSeconds (ADR 0020). It is the user's to set.
	KeyForgePollSeconds = "forgePollSeconds"
	// KeyVimMode gives xf web's editors vim's keys: "false" (the default)
	// or "true". Off, they take the usual editor keys and the vim keys
	// below do nothing. It is the user's to set.
	KeyVimMode = "vimMode"
	// The vim keys of xf web's editor (the source pane and the Scratch
	// view), in normal mode: the next and previous git change (hunk),
	// undoing and staging the change under the cursor, the next and previous error, the documentation of what is under the cursor, and going to
	// its definition, the source pane's context menu, and the source
	// pane's buffers: the previous and next one and their list. Each is a
	// key sequence as vim writes one, e.g. ]g or <C-k>, where <leader>
	// stands for vimLeader's keys, and the user's to set; they have no
	// flags.
	KeyVimNextHunk     = "vimNextHunk"
	KeyVimPrevHunk     = "vimPrevHunk"
	KeyVimUndoHunk     = "vimUndoHunk"
	KeyVimStageHunk    = "vimStageHunk"
	KeyVimNextError    = "vimNextError"
	KeyVimPrevError    = "vimPrevError"
	KeyVimHover        = "vimHover"
	KeyVimDefinition   = "vimDefinition"
	KeyVimMenu         = "vimMenu"
	KeyVimLeader       = "vimLeader"
	KeyVimBufferPrev   = "vimBufferPrev"
	KeyVimBufferNext   = "vimBufferNext"
	KeyVimBuffers      = "vimBuffers"
	KeyVimBufferDelete = "vimBufferDelete"
	KeyVimFiles        = "vimFiles"
	// KeyVimFormat formats the file's SQL with sqlfluff (ADR 0024). It is
	// the one vim key with no default: :format and the palette do it
	// until a key is set.
	KeyVimFormat = "vimFormat"
)

// VimKeys are the vim keys, in the order they are documented, and
// VimKeyDefaults their keys when nothing sets them.
var (
	VimKeys        = []string{KeyVimNextHunk, KeyVimPrevHunk, KeyVimUndoHunk, KeyVimStageHunk, KeyVimNextError, KeyVimPrevError, KeyVimHover, KeyVimDefinition, KeyVimMenu, KeyVimLeader, KeyVimBufferPrev, KeyVimBufferNext, KeyVimBuffers, KeyVimBufferDelete, KeyVimFiles, KeyVimFormat}
	VimKeyDefaults = map[string]string{
		KeyVimNextHunk: "]g", KeyVimPrevHunk: "[g", KeyVimUndoHunk: "guh", KeyVimStageHunk: "gsh", KeyVimNextError: "]e", KeyVimPrevError: "[e",
		KeyVimHover: "K", KeyVimDefinition: "gd", KeyVimMenu: "gm",
		KeyVimLeader: "<Space>", KeyVimBufferPrev: "<leader>[", KeyVimBufferNext: "<leader>]", KeyVimBuffers: "<leader>b",
		KeyVimBufferDelete: "<leader>x", KeyVimFiles: "<leader>f",
	}
)

// IsVimKey reports whether key is one of VimKeys.
func IsVimKey(key string) bool { return slices.Contains(VimKeys, key) }

// vimSequence says whether v is a key sequence as vim writes one: keys
// with no space between them, a key a character or a name in angle
// brackets such as <C-k> or <Space>.
func vimSequence(v string) bool {
	return v != "" && !strings.ContainsFunc(v, unicode.IsSpace)
}

// DefaultWebAutoExitSeconds is webAutoExitSeconds when nothing sets it,
// unless xf web answers other hosts (--allow-host): then it is off.
const DefaultWebAutoExitSeconds = 45

// DefaultForgePollSeconds is forgePollSeconds when nothing sets it, and
// MinForgePollSeconds the least it may be.
const (
	DefaultForgePollSeconds = 10
	MinForgePollSeconds     = 5
)

// Themes are the values of the theme setting; ThemeAuto follows the
// terminal's background (TUI) or the browser's colour scheme (xf web).
const (
	ThemeAuto  = "auto"
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// NextTheme is the theme after t when cycling auto, light, dark.
func NextTheme(t string) string {
	switch t {
	case ThemeAuto, "":
		return ThemeLight
	case ThemeLight:
		return ThemeDark
	}
	return ThemeAuto
}

// DefaultBranch is the branch R compares against when defaultBranch isn't
// set.
const DefaultBranch = "main"

// DefaultEditor is the command o opens files with when editor, $VISUAL
// and $EDITOR aren't set: VS Code.
const DefaultEditor = "code"

// DefaultScratchWarnGB and DefaultScratchConfirmGB are scratchWarnGB and
// scratchConfirmGB when nothing sets them.
const (
	DefaultScratchWarnGB    = 20
	DefaultScratchConfirmGB = 50
)

// NoDefault is the Default of a key that has none: nothing is used.
const NoDefault = "none"

// Titles are each key's name as a person reads it, e.g. "Autosave delay"
// for sourceAutosaveMs: xf web's settings page shows it, the key beside
// it.
var Titles = map[string]string{
	KeyDataformBin:          "Dataform binary",
	KeyDbtBin:               "dbt binary",
	KeyDbtTarget:            "dbt target",
	KeyDbtProdTarget:        "dbt prod target",
	KeyDbtCompileWithHooks:  "Compile dbt v2 projects with hooks",
	KeyRunMode:              "Run mode",
	KeyDataformLocation:     "Dataform region",
	KeyDataformRepo:         "Dataform repository",
	KeyDefaultBranch:        "Default branch",
	KeyDevCompilerOptions:   "Dev compiler options",
	KeyProdCompilerOptions:  "Prod compiler options",
	KeyTablePrefix:          "Table prefix",
	KeySchemaSuffix:         "Schema suffix",
	KeyDatabaseSuffix:       "Database suffix",
	KeyEditor:               "Editor command",
	KeyTheme:                "TUI theme",
	KeyWebTheme:             "Web theme",
	KeyWebThemeLight:        "Web theme when light",
	KeyWebThemeDark:         "Web theme when dark",
	KeyScratchWarnGB:        "Scratch warning size",
	KeyScratchConfirmGB:     "Scratch confirm size",
	KeySourceAutosaveMs:     "Autosave delay",
	KeyDiffStyle:            "Diff style",
	KeyPythonLanguageServer: "Python language server",
	KeySqlfluffBin:          "sqlfluff binary",
	KeySqlfluffConfig:       "sqlfluff config",
	KeyAutoHidePane2:        "Auto-hide pane 2",
	KeyDeferToProd:          "Defer to prod",
	KeyPane2Tab:             "Pane 2 starts on",
	KeyWrapLines:            "Wrap long lines",
	KeyWebAutoExitSeconds:   "Auto-exit delay",
	KeyWebOpenProjectIn:     "Open projects in",
	KeyWebBrowser:           "Browser",
	KeyWebBrowserProfile:    "Browser profile",
	KeyForgePollSeconds:     "Pipeline check interval",
	KeyVimMode:              "Vim mode",
	KeyVimNextHunk:          "Next git change",
	KeyVimPrevHunk:          "Previous git change",
	KeyVimUndoHunk:          "Undo git change",
	KeyVimStageHunk:         "Stage git change",
	KeyVimNextError:         "Next error",
	KeyVimPrevError:         "Previous error",
	KeyVimHover:             "Documentation",
	KeyVimDefinition:        "Go to definition",
	KeyVimMenu:              "Context menu",
	KeyVimLeader:            "Leader",
	KeyVimBufferPrev:        "Previous buffer",
	KeyVimBufferNext:        "Next buffer",
	KeyVimBuffers:           "List buffers",
	KeyVimBufferDelete:      "Remove buffer",
	KeyVimFiles:             "Find a file",
	KeyVimFormat:            "Format file",
}

// Defaults are what each key is when nothing sets it, as a person reads
// it: the value, and for a default the environment decides, how it is
// worked out, e.g. "code ($VISUAL/$EDITOR first)"; NoDefault for none.
// The constants above are the values xf uses.
var Defaults = func() map[string]string {
	d := map[string]string{
		KeyDataformBin:          "dataform on PATH ($DATAFORM_BIN first)",
		KeyDbtBin:               "dbt on PATH ($DBT_BIN first)",
		KeyDbtTarget:            "the profile's target ($DBT_TARGET first)",
		KeyDbtProdTarget:        NoDefault + " (dbtTarget's)",
		KeyDbtCompileWithHooks:  "false",
		KeyRunMode:              "cli",
		KeyDataformLocation:     "defaultLocation of the workflow settings",
		KeyDataformRepo:         "the origin remote's name",
		KeyDefaultBranch:        DefaultBranch,
		KeyDevCompilerOptions:   NoDefault,
		KeyProdCompilerOptions:  NoDefault + " (the default targets)",
		KeyTablePrefix:          "namePrefix of the workflow settings",
		KeySchemaSuffix:         "datasetSuffix of the workflow settings",
		KeyDatabaseSuffix:       "projectSuffix of the workflow settings",
		KeyEditor:               DefaultEditor + " ($VISUAL/$EDITOR first)",
		KeyTheme:                ThemeAuto,
		KeyWebTheme:             ThemeAuto,
		KeyWebThemeLight:        webtheme.DefaultLight,
		KeyWebThemeDark:         webtheme.DefaultDark,
		KeyScratchWarnGB:        strconv.Itoa(DefaultScratchWarnGB),
		KeyScratchConfirmGB:     strconv.Itoa(DefaultScratchConfirmGB),
		KeySourceAutosaveMs:     "0 (off)",
		KeyDiffStyle:            DiffInline,
		KeyPythonLanguageServer: PythonPyrefly,
		KeySqlfluffBin:          "the project's .venv, then sqlfluff on PATH",
		KeySqlfluffConfig:       "the project's own sqlfluff config, else xf's",
		KeyAutoHidePane2:        "true",
		KeyDeferToProd:          "false",
		KeyPane2Tab:             Pane2SQL,
		KeyWrapLines:            "false",
		KeyWebAutoExitSeconds:   strconv.Itoa(DefaultWebAutoExitSeconds) + " (off with --allow-host)",
		KeyWebOpenProjectIn:     OpenSameTab,
		KeyWebBrowser:           BrowserDefault,
		KeyWebBrowserProfile:    NoDefault + " (the profile last used)",
		KeyForgePollSeconds:     strconv.Itoa(DefaultForgePollSeconds),
		KeyVimMode:              "false",
	}
	for k, v := range VimKeyDefaults {
		d[k] = v
	}
	d[KeyVimFormat] = NoDefault + " (:format and the palette)"
	return d
}()

// Keys lists every key in the order they are documented.
var Keys = []string{KeyDataformBin, KeyDbtBin, KeyDbtTarget, KeyDbtProdTarget, KeyDbtCompileWithHooks, KeyRunMode, KeyDataformLocation, KeyDataformRepo, KeyDefaultBranch, KeyTablePrefix, KeySchemaSuffix, KeyDatabaseSuffix, KeyDevCompilerOptions, KeyProdCompilerOptions, KeyDeferToProd, KeyEditor, KeyTheme, KeyWebTheme, KeyWebThemeLight, KeyWebThemeDark, KeyScratchWarnGB, KeyScratchConfirmGB, KeySourceAutosaveMs, KeyDiffStyle, KeyPythonLanguageServer, KeySqlfluffBin, KeySqlfluffConfig, KeyAutoHidePane2, KeyPane2Tab, KeyWrapLines, KeyWebAutoExitSeconds, KeyWebOpenProjectIn, KeyWebBrowser, KeyWebBrowserProfile, KeyForgePollSeconds,
	KeyVimMode, KeyVimNextHunk, KeyVimPrevHunk, KeyVimUndoHunk, KeyVimStageHunk, KeyVimNextError, KeyVimPrevError, KeyVimHover, KeyVimDefinition, KeyVimMenu, KeyVimLeader, KeyVimBufferPrev, KeyVimBufferNext, KeyVimBuffers, KeyVimBufferDelete, KeyVimFiles, KeyVimFormat}

// userKeys are the keys only the user's config file sets.
var userKeys = []string{KeyTheme, KeyPane2Tab, KeyWrapLines, KeyWebAutoExitSeconds, KeyWebOpenProjectIn, KeyWebBrowser, KeyWebBrowserProfile, KeyForgePollSeconds,
	KeyVimMode, KeyVimNextHunk, KeyVimPrevHunk, KeyVimUndoHunk, KeyVimStageHunk, KeyVimNextError, KeyVimPrevError, KeyVimHover, KeyVimDefinition, KeyVimMenu, KeyVimLeader, KeyVimBufferPrev, KeyVimBufferNext, KeyVimBuffers, KeyVimBufferDelete, KeyVimFiles, KeyVimFormat}

// Flags maps each key to the flag that sets it; the vim keys, the
// themes of xf web's auto and diffStyle have none.
var Flags = map[string]string{
	KeyDataformBin:         "dataform-bin",
	KeyDbtBin:              "dbt-bin",
	KeyDbtTarget:           "dbt-target",
	KeyDbtProdTarget:       "dbt-prod-target",
	KeyDbtCompileWithHooks: "dbt-compile-with-hooks",
	KeyRunMode:             "run-mode",
	KeyDataformLocation:    "dataform-location",
	KeyDataformRepo:        "dataform-repo",
	KeyDefaultBranch:       "default-branch",
	KeyDevCompilerOptions:  "dev-compiler-options",
	KeyProdCompilerOptions: "prod-compiler-options",
	KeyTablePrefix:         "table-prefix",
	KeySchemaSuffix:        "schema-suffix",
	KeyDatabaseSuffix:      "database-suffix",
	KeyEditor:              "editor",
	KeyTheme:               "theme",
	KeyWebTheme:            "web-theme",
	KeyScratchWarnGB:       "scratch-warn-gb",
	KeyScratchConfirmGB:    "scratch-confirm-gb",
	KeySourceAutosaveMs:    "source-autosave-ms",
	KeyWebAutoExitSeconds:  "web-auto-exit-seconds",
	KeyWebBrowser:          "web-browser",
	KeyWebBrowserProfile:   "web-browser-profile",
}

// OpenSameTab and OpenNewTab are webOpenProjectIn's values.
const (
	OpenSameTab = "same-tab"
	OpenNewTab  = "new-tab"
)

// BrowserDefault and BrowserApp are webBrowser's values but for a
// browser's path or name.
const (
	BrowserDefault = "default"
	BrowserApp     = "app"
)

// PythonPyrefly and PythonOff are pythonLanguageServer's values.
const (
	PythonPyrefly = "pyrefly"
	PythonOff     = "off"
)

// DiffInline and DiffSideBySide are diffStyle's values.
const (
	DiffInline     = "inline"
	DiffSideBySide = "side-by-side"
)

// Pane2SQL and Pane2Columns are pane2Tab's values.
const (
	Pane2SQL     = "sql"
	Pane2Columns = "columns"
)

// ProjectFile is the project's config, relative to the project root.
var ProjectFile = filepath.Join(".xf", "config.json")

// Settings are xf's settings; "" means unset.
type Settings struct {
	DataformBin string // path to the dataform binary
	DbtBin      string // path to the dbt binary
	DbtTarget   string // profile target dbt compiles with
	// DbtProdTarget is the profile target of the project's prod runs
	DbtProdTarget string
	// DbtCompileWithHooks is "true" to compile dbt v2 projects with hooks
	DbtCompileWithHooks string
	RunMode             string // "cli" or "api": how r runs to start with
	DataformLocation    string // region of the Dataform repository, for API runs
	DataformRepo        string // name of the Dataform repository, for API runs
	DefaultBranch       string // the branch R (run changed) compares against
	// DevCompilerOptions are the compiler options of the user's dev runs
	DevCompilerOptions string
	// ProdCompilerOptions are the compiler options of the project's prod
	// runs
	ProdCompilerOptions string
	// TablePrefix, SchemaSuffix and DatabaseSuffix are what Dataform adds
	// to the names it builds
	TablePrefix, SchemaSuffix, DatabaseSuffix string
	// Editor is the command o opens files with when no editor is connected
	Editor string
	// Theme is auto, light or dark, WebTheme auto or a theme's id, and
	// WebThemeLight and WebThemeDark a theme's id
	Theme, WebTheme, WebThemeLight, WebThemeDark string
	// ScratchWarnGB and ScratchConfirmGB are GiB, as a number
	ScratchWarnGB, ScratchConfirmGB string
	// SourceAutosaveMs is milliseconds, as a whole number
	SourceAutosaveMs string
	// DiffStyle is "inline" or "side-by-side"
	DiffStyle string
	// PythonLanguageServer is "pyrefly" or "off"
	PythonLanguageServer string
	// SqlfluffBin is the path to the sqlfluff binary, SqlfluffConfig the
	// path to a config file for it
	SqlfluffBin, SqlfluffConfig string
	// DeferToProd is "true" to start with defer to prod on.
	DeferToProd string
	// AutoHidePane2 is "false" to keep pane 2 beside a file with no action
	AutoHidePane2 string
	// Pane2Tab is "sql" or "columns"
	Pane2Tab string
	// WrapLines is "true" to wrap long lines in xf web's editors
	WrapLines string
	// VimMode is "true" to give xf web's editors vim's keys
	VimMode string
	// WebAutoExitSeconds is seconds, as a whole number
	WebAutoExitSeconds string
	// WebOpenProjectIn is "same-tab" or "new-tab"
	WebOpenProjectIn string
	// WebBrowser is "default", "app" or a Chromium browser's path or name
	WebBrowser string
	// WebBrowserProfile is a Chromium profile directory, or empty
	WebBrowserProfile string
	// ForgePollSeconds is seconds, as a whole number
	ForgePollSeconds string
	// VimNextHunk and the rest are key sequences as vim writes them
	VimNextHunk, VimPrevHunk, VimUndoHunk, VimStageHunk, VimNextError, VimPrevError, VimHover, VimDefinition, VimMenu string
	// VimLeader is the keys <leader> stands for in the others
	VimLeader, VimBufferPrev, VimBufferNext, VimBuffers, VimBufferDelete, VimFiles string
	// VimFormat is the key that formats the SQL, "" for none
	VimFormat string
}

func (s *Settings) field(key string) *string {
	switch key {
	case KeyDataformBin:
		return &s.DataformBin
	case KeyDbtBin:
		return &s.DbtBin
	case KeyDbtTarget:
		return &s.DbtTarget
	case KeyDbtProdTarget:
		return &s.DbtProdTarget
	case KeyDbtCompileWithHooks:
		return &s.DbtCompileWithHooks
	case KeyRunMode:
		return &s.RunMode
	case KeyDataformLocation:
		return &s.DataformLocation
	case KeyDataformRepo:
		return &s.DataformRepo
	case KeyDefaultBranch:
		return &s.DefaultBranch
	case KeyDevCompilerOptions:
		return &s.DevCompilerOptions
	case KeyProdCompilerOptions:
		return &s.ProdCompilerOptions
	case KeyTablePrefix:
		return &s.TablePrefix
	case KeySchemaSuffix:
		return &s.SchemaSuffix
	case KeyDatabaseSuffix:
		return &s.DatabaseSuffix
	case KeyEditor:
		return &s.Editor
	case KeyTheme:
		return &s.Theme
	case KeyWebTheme:
		return &s.WebTheme
	case KeyWebThemeLight:
		return &s.WebThemeLight
	case KeyWebThemeDark:
		return &s.WebThemeDark
	case KeyScratchWarnGB:
		return &s.ScratchWarnGB
	case KeyScratchConfirmGB:
		return &s.ScratchConfirmGB
	case KeySourceAutosaveMs:
		return &s.SourceAutosaveMs
	case KeyDiffStyle:
		return &s.DiffStyle
	case KeyPythonLanguageServer:
		return &s.PythonLanguageServer
	case KeySqlfluffBin:
		return &s.SqlfluffBin
	case KeySqlfluffConfig:
		return &s.SqlfluffConfig
	case KeyAutoHidePane2:
		return &s.AutoHidePane2
	case KeyDeferToProd:
		return &s.DeferToProd
	case KeyPane2Tab:
		return &s.Pane2Tab
	case KeyWrapLines:
		return &s.WrapLines
	case KeyVimMode:
		return &s.VimMode
	case KeyWebAutoExitSeconds:
		return &s.WebAutoExitSeconds
	case KeyForgePollSeconds:
		return &s.ForgePollSeconds
	case KeyWebOpenProjectIn:
		return &s.WebOpenProjectIn
	case KeyWebBrowser:
		return &s.WebBrowser
	case KeyWebBrowserProfile:
		return &s.WebBrowserProfile
	case KeyVimNextHunk:
		return &s.VimNextHunk
	case KeyVimPrevHunk:
		return &s.VimPrevHunk
	case KeyVimUndoHunk:
		return &s.VimUndoHunk
	case KeyVimStageHunk:
		return &s.VimStageHunk
	case KeyVimNextError:
		return &s.VimNextError
	case KeyVimPrevError:
		return &s.VimPrevError
	case KeyVimHover:
		return &s.VimHover
	case KeyVimDefinition:
		return &s.VimDefinition
	case KeyVimMenu:
		return &s.VimMenu
	case KeyVimLeader:
		return &s.VimLeader
	case KeyVimBufferPrev:
		return &s.VimBufferPrev
	case KeyVimBufferNext:
		return &s.VimBufferNext
	case KeyVimBuffers:
		return &s.VimBuffers
	case KeyVimBufferDelete:
		return &s.VimBufferDelete
	case KeyVimFiles:
		return &s.VimFiles
	case KeyVimFormat:
		return &s.VimFormat
	}
	return nil
}

// VimKey is the sequence of vim key key: the one set, else its default.
func (s Settings) VimKey(key string) string {
	if v := s.Get(key); v != "" {
		return v
	}
	return VimKeyDefaults[key]
}

// leaderRE is <leader> in a vim key sequence, as vim reads it: in any case.
var leaderRE = regexp.MustCompile(`(?i)<leader>`)

// VimSequence is the keys of vim command key, <leader> written out as
// the leader's keys.
func (s Settings) VimSequence(key string) string {
	v := s.VimKey(key)
	if key == KeyVimLeader {
		return v
	}
	return leaderRE.ReplaceAllLiteralString(v, s.VimKey(KeyVimLeader))
}

// VimClash says which vim commands would share keys if key's keys were
// keys: key and another command, or for the leader any two commands.
func (s Settings) VimClash(key, keys string) error {
	if f := s.field(key); f != nil {
		*f = keys
	}
	for _, k := range VimKeys {
		if k == KeyVimLeader || (key != KeyVimLeader && k != key) {
			continue
		}
		for _, other := range VimKeys {
			if other == k || other == KeyVimLeader || s.VimSequence(other) != s.VimSequence(k) {
				continue
			}
			if key == KeyVimLeader {
				return fmt.Errorf("%s and %s would both be %s", k, other, s.VimSequence(k))
			}
			return fmt.Errorf("%s already has %s", other, s.VimSequence(k))
		}
	}
	return nil
}

// Get returns the value of key.
func (s Settings) Get(key string) string {
	if f := s.field(key); f != nil {
		return *f
	}
	return ""
}

// File is a config file xf looked for.
type File struct {
	Path  string // as shown to the user (~ for the home directory)
	Abs   string // the absolute path
	Found bool
}

// Loaded is the result of Load.
type Loaded struct {
	Settings
	// Sources says where each set key came from: a file as shown to the
	// user, or "--flag" once Set.
	Sources map[string]string
	// Files are the files looked for, lowest precedence first.
	Files []File
	// Warnings are unknown keys, one per line, e.g.
	// `.xf/config.json:3: unknown key "dataformLocaton"`.
	Warnings []string
}

// Set overrides key with a flag's value.
func (l *Loaded) Set(key, value, source string) {
	if f := l.field(key); f != nil {
		*f = value
		l.Sources[key] = source
	}
}

// Options say which files Load reads.
type Options struct {
	// Root is the project root; "" reads no project file (outside a
	// project).
	Root string
	// Project, when non-nil, replaces the project file (--config): a path,
	// relative to the working directory, or "" to read none.
	Project *string
	// UserDir is the directory of the user's config.json; "" means the
	// default (see UserDir).
	UserDir string
	// Home is the home directory, for ~ in paths and in how files are
	// shown; "" means os.UserHomeDir.
	Home string
}

// goos is the OS whose conventions UserDir follows; tests change it.
var goos = runtime.GOOS

// UserDir is where the user's config.json lives: $XDG_CONFIG_HOME/xf, else
// %AppData%\xf on Windows and ~/.config/xf elsewhere (also on macOS, as most
// command-line tools do).
func UserDir(getenv func(string) string, home string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "xf")
	}
	if a := getenv("AppData"); goos == "windows" && a != "" {
		return filepath.Join(a, "xf")
	}
	return filepath.Join(home, ".config", "xf")
}

// Load reads the user's and the project's config. A file that doesn't
// exist is skipped; one that can't be parsed, or has a wrong value, is an
// error naming the file and line.
func Load(opts Options) (Loaded, error) {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	userDir := opts.UserDir
	if userDir == "" {
		userDir = UserDir(os.Getenv, home)
	}
	l := Loaded{Sources: map[string]string{}}

	type candidate struct {
		path, base string // base is what relative paths in it are relative to
		required   bool   // named by --config
	}
	files := []candidate{{path: filepath.Join(userDir, "config.json"), base: userDir}}
	switch {
	case opts.Project != nil && *opts.Project != "":
		p, err := filepath.Abs(expandHome(*opts.Project, home))
		if err != nil {
			return l, err
		}
		files = append(files, candidate{path: p, base: filepath.Dir(p), required: true})
	case opts.Project == nil && opts.Root != "":
		// Paths in .xf/config.json are relative to the project root.
		files = append(files, candidate{path: filepath.Join(opts.Root, ProjectFile), base: opts.Root})
	}
	for i, c := range files {
		shown := display(c.path, opts.Root, home)
		data, err := os.ReadFile(c.path)
		if errors.Is(err, os.ErrNotExist) {
			if c.required {
				return l, fmt.Errorf("--config %s: %w", *opts.Project, err)
			}
			l.Files = append(l.Files, File{Path: shown, Abs: c.path})
			continue
		}
		if err != nil {
			return l, err
		}
		l.Files = append(l.Files, File{Path: shown, Abs: c.path, Found: true})
		type userValue struct {
			v, from string
			set     bool
		}
		user := map[string]userValue{}
		for _, k := range userKeys {
			user[k] = userValue{l.Get(k), l.Sources[k], l.IsSet(k)}
		}
		if err := l.parse(data, shown, c.base, home); err != nil {
			return l, err
		}
		// The TUI's theme, auto-exit and vim keys are the user's: a project's file
		// doesn't set them.
		for _, k := range userKeys {
			if i == 0 || l.Sources[k] != shown {
				continue
			}
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s: %s is ignored: it is a setting of the user's config file", shown, k))
			u := user[k]
			*l.field(k) = u.v
			if u.set {
				l.Sources[k] = u.from
			} else {
				delete(l.Sources, k)
			}
		}
	}
	return l, nil
}

// parse reads one file's keys over what is already loaded.
func (l *Loaded) parse(data []byte, shown, base, home string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	fail := func(offset int64, format string, args ...any) error {
		return fmt.Errorf("%s:%d: %s", shown, lineAt(data, offset), fmt.Sprintf(format, args...))
	}
	syntax := func(err error) error {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return fail(se.Offset, "%v", se)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fail(int64(len(data)), "unexpected end of file")
		}
		return fail(dec.InputOffset(), "%v", err)
	}
	tok, err := dec.Token()
	if err != nil {
		return syntax(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fail(0, "want a JSON object like {\"%s\": \"api\"}", KeyRunMode)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return syntax(err)
		}
		key := tok.(string) // object keys are always strings
		at := dec.InputOffset()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return syntax(err)
		}
		f := l.field(key)
		if f == nil {
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s:%d: unknown key %q (known: %s)", shown, lineAt(data, at), key, strings.Join(Keys, ", ")))
			continue
		}
		v, err := valueOf(key, raw, base, home)
		if err != nil {
			return fail(at, "%v", err)
		}
		*f = v
		l.Sources[key] = shown
	}
	if _, err := dec.Token(); err != nil {
		return syntax(err)
	}
	if dec.More() {
		return fail(dec.InputOffset(), "unexpected content after the object")
	}
	return nil
}

// valueOf is the value raw gives key, as a file's JSON writes it: an error
// says why it isn't one. Paths of binaries are made absolute, relative to
// base.
func valueOf(key string, raw json.RawMessage, base, home string) (string, error) {
	var v string
	if key == KeyDbtCompileWithHooks || key == KeyAutoHidePane2 || key == KeyWrapLines || key == KeyVimMode || key == KeyDeferToProd {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", fmt.Errorf("%s must be true or false, not %s", key, raw)
		}
		v = fmt.Sprint(b)
	} else if key == KeyScratchWarnGB || key == KeyScratchConfirmGB {
		// A number, or a string of one.
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("%s must be a number of GiB, not %s", key, raw)
			}
		} else {
			v = n.String()
		}
		if f, err := strconv.ParseFloat(v, 64); err != nil || f <= 0 {
			return "", fmt.Errorf("%s must be a number of GiB above 0, not %s", key, raw)
		}
	} else if key == KeySourceAutosaveMs {
		// A whole number, or a string of one.
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("%s must be a number of milliseconds, not %s", key, raw)
			}
		} else {
			v = n.String()
		}
		if ms, err := strconv.Atoi(v); err != nil || ms < 0 {
			return "", fmt.Errorf("%s must be a whole number of milliseconds, 0 or more, not %s", key, raw)
		}
	} else if key == KeyWebAutoExitSeconds {
		// A whole number, or a string of one.
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("%s must be a number of seconds, not %s", key, raw)
			}
		} else {
			v = n.String()
		}
		if sec, err := strconv.Atoi(v); err != nil || sec < 0 {
			return "", fmt.Errorf("%s must be a whole number of seconds, 0 or more, not %s", key, raw)
		}
	} else if key == KeyForgePollSeconds {
		// A whole number, or a string of one.
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("%s must be a number of seconds, not %s", key, raw)
			}
		} else {
			v = n.String()
		}
		if sec, err := strconv.Atoi(v); err != nil || sec < MinForgePollSeconds {
			return "", fmt.Errorf("%s must be a whole number of seconds, %d or more, not %s", key, MinForgePollSeconds, raw)
		}
	} else if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("%s must be a string, not %s", key, raw)
	}
	switch {
	case key == KeyRunMode && v != "cli" && v != "api":
		return "", fmt.Errorf("%s must be \"cli\" or \"api\", not %q", key, v)
	case key == KeyDiffStyle && v != DiffInline && v != DiffSideBySide:
		return "", fmt.Errorf("%s must be \"%s\" or \"%s\", not %q", key, DiffInline, DiffSideBySide, v)
	case key == KeyPane2Tab && v != Pane2SQL && v != Pane2Columns:
		return "", fmt.Errorf("%s must be \"%s\" or \"%s\", not %q", key, Pane2SQL, Pane2Columns, v)
	case key == KeyPythonLanguageServer && v != PythonPyrefly && v != PythonOff:
		return "", fmt.Errorf("%s must be \"%s\" or \"%s\", not %q", key, PythonPyrefly, PythonOff, v)
	case key == KeyWebOpenProjectIn && v != OpenSameTab && v != OpenNewTab:
		return "", fmt.Errorf("%s must be \"%s\" or \"%s\", not %q", key, OpenSameTab, OpenNewTab, v)
	case key == KeyTheme && v != ThemeAuto && v != ThemeLight && v != ThemeDark:
		return "", fmt.Errorf("%s must be \"auto\", \"light\" or \"dark\", not %q", key, v)
	case key == KeyWebTheme && v != ThemeAuto && !webThemeOK(v):
		return "", fmt.Errorf("%s must be \"auto\" or a theme (%s), not %q", key, webtheme.IDs(), v)
	case (key == KeyWebThemeLight || key == KeyWebThemeDark) && !webThemeOK(v):
		return "", fmt.Errorf("%s must be a theme (%s), not %q", key, webtheme.IDs(), v)
	case IsVimKey(key) && !vimSequence(v):
		return "", fmt.Errorf("%s must be keys as vim writes them, e.g. %s or <C-k>, with no spaces, not %q", key, cmp.Or(VimKeyDefaults[key], "<leader>="), v)
	case (key == KeyTablePrefix || key == KeySchemaSuffix || key == KeyDatabaseSuffix) && !nameAffix.MatchString(v):
		return "", fmt.Errorf("%s must be letters, digits, _ and -, not %q", key, v)
	case key == KeyWebBrowser && v == "":
		return "", fmt.Errorf("%s must be \"%s\", \"%s\" or a Chromium browser's path or name, not \"\"", key, BrowserDefault, BrowserApp)
	case key == KeyWebBrowser && v != BrowserDefault && v != BrowserApp:
		v = expandHome(v, home)
	case (key == KeyDataformBin || key == KeyDbtBin || key == KeySqlfluffBin) && v != "":
		v = expandHome(v, home)
		// A path, not a name to look up on PATH: relative to the file.
		if (strings.ContainsRune(v, '/') || strings.ContainsRune(v, filepath.Separator)) && !filepath.IsAbs(v) {
			v = filepath.Join(base, v)
		}
	case key == KeySqlfluffConfig && v != "":
		// Always a path, even a bare .sqlfluff: relative to the file.
		if v = expandHome(v, home); !filepath.IsAbs(v) {
			v = filepath.Join(base, v)
		}
	}
	return v, nil
}

// nameAffix is what tablePrefix, schemaSuffix and databaseSuffix may be:
// what a BigQuery table, dataset or project name may have in it, so a
// value never needs quoting on a command line.
var nameAffix = regexp.MustCompile(`^[A-Za-z0-9_-]*$`)

// webThemeOK reports whether v names one of xf web's themes; light and
// dark, which webTheme was before there were themes, name xf's own.
func webThemeOK(v string) bool {
	_, ok := webtheme.Find(v)
	return ok
}

// lineAt is the 1-based line of byte offset in data.
func lineAt(data []byte, offset int64) int {
	offset = min(max(offset, 0), int64(len(data)))
	return bytes.Count(data[:offset], []byte("\n")) + 1
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator))) {
		return filepath.Join(home, p[1:])
	}
	return p
}

// display shows path relative to root when inside it, else with ~ for home.
func display(path, root, home string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// Write writes s's set keys, in the documented order, as the project file
// of root. It refuses to replace an existing file unless force is set.
func Write(root string, s Settings, force bool) (string, error) {
	path := filepath.Join(root, ProjectFile)
	if !force {
		if _, err := os.Stat(path); err == nil {
			return path, os.ErrExist
		}
	}
	data := Marshal(s)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// Marshal renders s's set keys as an indented JSON object, in the
// documented order.
func Marshal(s Settings) []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	var set []string
	for _, k := range Keys {
		if s.Get(k) != "" {
			set = append(set, k)
		}
	}
	for i, k := range set {
		v, _ := json.Marshal(s.Get(k))
		fmt.Fprintf(&b, "  %q: %s", k, v)
		if i < len(set)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// SetProjectKey sets key to value in the project file of root, keeping the
// other keys as they are written, and creates the file when there is none.
// The file is rewritten with the known keys in the documented order, then
// any others. Unlike Marshal it writes an empty value: for
// prodCompilerOptions, "" says the default targets are prod.
func SetProjectKey(root, key, value string) (string, error) {
	path := filepath.Join(root, ProjectFile)
	return path, setKey(path, key, value, false)
}

// UnsetProjectKey removes key from the project file of root, so the
// user's file or the default applies; a missing file or key is left as it
// is.
func UnsetProjectKey(root, key string) (string, error) {
	path := filepath.Join(root, ProjectFile)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	return path, setKey(path, key, "", true)
}

// SetProjectBranch makes branch the defaultBranch of the project at root:
// it takes the key out of the project file when the user's file, or else
// DefaultBranch, already says branch, so a later change there reaches the
// project; otherwise it sets it.
func SetProjectBranch(root, branch string) (string, error) {
	if strings.TrimSpace(branch) == "" {
		return "", errors.New("no branch named")
	}
	if err := Check(KeyDefaultBranch, branch); err != nil {
		return "", err
	}
	inherited := DefaultBranch
	if path, err := userFile(); err == nil {
		if keys, err := ReadKeys(path); err == nil && keys[KeyDefaultBranch] != "" {
			inherited = keys[KeyDefaultBranch]
		}
	}
	if branch == inherited {
		return UnsetProjectKey(root, KeyDefaultBranch)
	}
	return SetProjectKey(root, KeyDefaultBranch, branch)
}

// UnsetUserKey removes key from the user's config file.
func UnsetUserKey(key string) (string, error) {
	path, err := userFile()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	return path, setKey(path, key, "", true)
}

// ReadKeys returns the known keys the config file at path sets, as
// written: a string's text, else the JSON (true, 1500). A missing file sets
// none; a file that isn't a JSON object is an error.
func ReadKeys(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range raw {
		if !slices.Contains(Keys, k) {
			continue
		}
		var str string
		if json.Unmarshal(v, &str) == nil {
			out[k] = str
		} else {
			out[k] = string(v)
		}
	}
	return out, nil
}

// UserFile is the user's config file (see UserDir).
func UserFile() (string, error) { return userFile() }

func userFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(UserDir(os.Getenv, home), "config.json"), nil
}

// UserOnly reports whether only the user's file sets key, as with the
// theme, webAutoExitSeconds and the vim keys: a project's file can't.
func UserOnly(key string) bool { return slices.Contains(userKeys, key) }

// Check reports why value can't be key's, typed as a person types it: true
// or false, a number, or text. It is the check a file's value gets.
func Check(key, value string) error {
	if !slices.Contains(Keys, key) {
		return fmt.Errorf("unknown key %q", key)
	}
	_, err := valueOf(key, rawOf(key, value), "", "")
	return err
}

// Problem is something wrong in a config file's text: its 1-based line,
// the key it is about ("" for the text's syntax), and what is wrong.
type Problem struct {
	Line    int    `json:"line"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}

// CheckFile checks data, the whole text of a config file, as SetUserKey
// and SetProjectKey check one value; user says it is the user's file,
// which alone sets the user-only keys. Errors are what stop it being
// written: text that isn't one JSON object, a known key's value its check
// refuses, a user-only key in a project's file, and a vim key with keys
// another vim key has. Warnings don't: the unknown keys Load warns of.
func CheckFile(data []byte, user bool) (errs, warns []Problem) {
	dec := json.NewDecoder(bytes.NewReader(data))
	syntax := func(err error) []Problem {
		offset := dec.InputOffset()
		msg := err.Error()
		var se *json.SyntaxError
		switch {
		case errors.As(err, &se):
			offset = se.Offset
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			offset, msg = int64(len(data)), "unexpected end of file"
		}
		return []Problem{{Line: lineAt(data, offset), Message: msg}}
	}
	tok, err := dec.Token()
	if err != nil {
		return syntax(err), nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return []Problem{{Line: 1, Message: fmt.Sprintf("want a JSON object like {\"%s\": \"api\"}", KeyRunMode)}}, nil
	}
	lines := map[string]int{}
	var set Settings
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return syntax(err), nil
		}
		key := tok.(string) // object keys are always strings
		line := lineAt(data, dec.InputOffset())
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return syntax(err), nil
		}
		f := set.field(key)
		switch {
		case f == nil:
			warns = append(warns, Problem{Line: line, Key: key, Message: fmt.Sprintf("unknown key %q (known: %s)", key, strings.Join(Keys, ", "))})
		case !user && UserOnly(key):
			errs = append(errs, Problem{Line: line, Key: key, Message: key + " is set in your user config only"})
		default:
			v, err := valueOf(key, raw, "", "")
			if err != nil {
				errs = append(errs, Problem{Line: line, Key: key, Message: err.Error()})
				continue
			}
			*f, lines[key] = v, line
		}
	}
	if _, err := dec.Token(); err != nil {
		return syntax(err), nil
	}
	if dec.More() {
		return []Problem{{Line: lineAt(data, dec.InputOffset()), Message: "unexpected content after the object"}}, nil
	}
	for _, k := range VimKeys {
		if _, ok := lines[k]; !ok {
			continue
		}
		if err := set.VimClash(k, set.VimKey(k)); err != nil {
			errs = append(errs, Problem{Line: lines[k], Key: k, Message: err.Error()})
		}
	}
	return errs, warns
}

// rawOf is value written as key's JSON: a boolean or a number for the keys
// that are one, else a string.
func rawOf(key, value string) json.RawMessage {
	switch key {
	case KeyDbtCompileWithHooks, KeyAutoHidePane2, KeyWrapLines, KeyVimMode, KeyDeferToProd:
		if value == "true" || value == "false" {
			return json.RawMessage(value)
		}
	case KeyScratchWarnGB, KeyScratchConfirmGB, KeySourceAutosaveMs, KeyWebAutoExitSeconds, KeyForgePollSeconds:
		var n json.Number
		if json.Unmarshal([]byte(value), &n) == nil {
			return json.RawMessage(n.String())
		}
	}
	b, _ := json.Marshal(value)
	return b
}

// SetUserKey sets key to value in the user's config file (see UserDir),
// as SetProjectKey does in a project's.
func SetUserKey(key, value string) (string, error) {
	path, err := userFile()
	if err != nil {
		return "", err
	}
	return path, setKey(path, key, value, false)
}

// setKey sets key to value in the config file at path, or with remove
// takes it out.
func setKey(path, key, value string, remove bool) error {
	if !slices.Contains(Keys, key) {
		return fmt.Errorf("unknown key %q", key)
	}
	var order []string
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		dec := json.NewDecoder(bytes.NewReader(data))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return fmt.Errorf("%s is not a JSON object", path)
		}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			k := tok.(string) // object keys are always strings
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if _, seen := raw[k]; !seen {
				order = append(order, k)
			}
			raw[k] = v
		}
	}
	if remove {
		delete(raw, key)
	} else {
		raw[key] = rawOf(key, value)
	}

	keys := make([]string, 0, len(raw))
	for _, k := range Keys {
		if _, ok := raw[k]; ok {
			keys = append(keys, k)
		}
	}
	for _, k := range order {
		if !slices.Contains(Keys, k) {
			keys = append(keys, k)
		}
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range keys {
		name, _ := json.Marshal(k)
		fmt.Fprintf(&b, "  %s: %s", name, raw[k])
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// IsSet reports whether key was set by a file or a flag, even to "".
func (l Loaded) IsSet(key string) bool {
	_, ok := l.Sources[key]
	return ok
}
