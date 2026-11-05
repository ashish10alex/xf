package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// vimDoc starts the description of each vim key, which the settings
// page's Vim tab leaves out: its header says it.
const vimDoc = "`keys` of xf web's editor (vim's normal mode): "

// flagUsage describes each config key, as its flag's usage when it has
// one.
var flagUsage = map[string]string{
	config.KeyDataformBin:          "`path` to the dataform binary (default: $" + dataform.BinEnv + ", then dataform on PATH)",
	config.KeyDbtBin:               "`path` to the dbt binary, dbt-core 1.x or dbt v2 (default: $" + dbt.BinEnv + ", then dbt on PATH)",
	config.KeyDbtTarget:            "profile `target` dbt compiles with, e.g. dev or prod; it changes the schemas and so the actions' IDs (default: $DBT_TARGET, then the profile's target)",
	config.KeyDbtProdTarget:        "profile `target` of the project's prod runs, e.g. prod, so column impact compares with the tables it builds and query cost dry runs against the tables it reads (default: none, dbtTarget's tables, and the reports say so when nothing sets that either)",
	config.KeyDbtCompileWithHooks:  "`true` to compile dbt v2 projects that have on-run-start or on-run-end hooks, which dbt v2 runs when it compiles (default: false, parse them)",
	config.KeyRunMode:              "`mode` r runs in to start with: cli (dataform run) or api (the Dataform API); a toggles it (default: cli)",
	config.KeyDataformLocation:     "`region` of the Dataform repository for API runs, e.g. europe-west2 (default: defaultLocation)",
	config.KeyDataformRepo:         "`name` of the Dataform repository for API runs (default: the origin remote's name)",
	config.KeyDefaultBranch:        "`branch` R (run changed), I (column impact) and C (query cost) compare against, as origin/<branch> or else the local one (default: " + config.DefaultBranch + ")",
	config.KeyTablePrefix:          "`prefix` for every table a Dataform project's compiles and runs build, joined with an underscore: dataform's --table-prefix (default: namePrefix of workflow_settings.yaml)",
	config.KeySchemaSuffix:         "`suffix` for every dataset a Dataform project's compiles and runs build in, joined with an underscore, e.g. your name: dataform's --schema-suffix (default: datasetSuffix of workflow_settings.yaml)",
	config.KeyDatabaseSuffix:       "`suffix` for every GCP project a Dataform project's compiles and runs build in, joined with an underscore: dataform's --database-suffix (default: projectSuffix of workflow_settings.yaml)",
	config.KeyDevCompilerOptions:   "compiler `options` of your dev runs, e.g. --schema-suffix=ashish, so column impact folds their lineage into the actions they run (default: none)",
	config.KeyProdCompilerOptions:  "compiler `options` of the project's prod runs, e.g. --default-database=acme-prod, so column impact compares with the tables they build and query cost dry runs against the tables they read; '' says the default targets are prod (default: none, the default targets, and the reports say so)",
	config.KeyEditor:               "`command` o opens files and projects with when no editor is connected, e.g. code, cursor, zed or nvim (default: $VISUAL, then $EDITOR, then " + config.DefaultEditor + ")",
	config.KeyTheme:                "`theme` of the TUI: auto (follow the terminal), light or dark; the palette's View: theme changes it (default: auto)",
	config.KeyWebTheme:             "`theme` of xf web: auto (follow the browser, with webThemeLight and webThemeDark) or a theme's id, e.g. dracula or catppuccin-mocha; the page's ◐ button picks it; a project's .xf/config.json can set its own (default: auto)",
	config.KeyWebThemeLight:        "theme xf web's auto shows while the browser is light, a theme's id, e.g. catppuccin-latte (default: " + webtheme.DefaultLight + ")",
	config.KeyWebThemeDark:         "theme xf web's auto shows while the browser is dark, a theme's id, e.g. tokyo-night (default: " + webtheme.DefaultDark + ")",
	config.KeyScratchWarnGB:        "`GiB` of a scratch query's dry run at which xf web turns it amber (default: 20)",
	config.KeyScratchConfirmGB:     "`GiB` of a scratch query's dry run at which xf web turns it red and asks before running it (default: 50)",
	config.KeyPythonLanguageServer: "`server` xf web's source pane checks Python files with: pyrefly or off (default: pyrefly)",
	config.KeySqlfluffBin:          "`path` to the sqlfluff binary that formats SQL (default: the project's .venv/ or venv/, then sqlfluff on PATH)",
	config.KeySqlfluffConfig:       "`path` to a sqlfluff config file to format with in place of the project's own (default: the project's .sqlfluff, tox.ini, setup.cfg or pyproject.toml, else the style built into xf)",
	config.KeyDiffStyle:            "`style` xf web's Source Control shows a file's difference in to start with: inline or side-by-side; t flips it while one shows (default: inline)",
	config.KeyDeferToProd:          "`true` to start with defer to prod on: an upstream table not built in dev is read from prod (prodCompilerOptions, or dbtProdTarget) in dry runs and runs; xf web's switch, kept per project, wins over it (default: false)",
	config.KeyAutoHidePane2:        "`false` to keep xf web's pane 2 showing while the source pane shows a file with no action, such as a .py script or .gitlab-ci.yml; " + "⌥⌘b (ctrl+alt+b) shows it for that file (default: true, hide it)",
	config.KeyWrapLines:            "`true` to wrap long lines in xf web's editors (the source pane, Scratch, a difference shown inline, the settings' JSON) rather than scroll them sideways; a side-by-side difference never wraps, so its two sides line up (default: false)",
	config.KeyPane2Tab:             "`tab` xf web's pane 2 starts on in a new browser tab: sql or columns; tab and c switch it after that, and the tab picked carries on to the next action (default: sql)",
	config.KeySourceAutosaveMs:     "`milliseconds` xf web's source pane waits after an edit (the last keystroke; with vimMode, leaving insert mode or a normal-mode change) before saving; 0 is off, and less than 500 acts as 500 (default: 0, off)",
	config.KeyWebAutoExitSeconds:   "`seconds` xf web waits, once every browser window has closed, before it stops; 0 is off (default: 45, or off with --allow-host)",
	config.KeyWebOpenProjectIn:     "`where` xf web opens a project picked from a project's page (> Project: open another): same-tab or new-tab; shift+enter does the other (default: same-tab)",
	config.KeyWebBrowser:           "`browser` xf web opens its page in: default (the system's browser), app (an app window of the first Chrome, Edge or Chromium found, which gets ctrl+n, as a tab off a Mac doesn't), or a Chromium browser's path or name to open the app window in; --app is app for one run (default: default)",
	config.KeyWebBrowserProfile:    "`profile` directory of the browser, e.g. \"Profile 1\", that xf web's app window opens in, and the links it opens with it, so they go to that profile's window rather than a new one; chrome://version names it (Profile Path's last part). A tab opens its links itself (default: none, the profile last used)",
	config.KeyForgePollSeconds:     "`seconds` xf web waits before asking GitLab or GitHub again while the branch's pipeline runs, 5 or more (default: 10)",
	config.KeyVimMode:              "`true` to give xf web's editors (the source pane, Scratch, Source Control's message, the settings' JSON, a difference) vim's keys, which the vim keys below add to; false gives them the usual editor keys, as VS Code's (default: false)",
	config.KeyVimNextHunk:          vimDoc + "go to the next git change (hunk) since the index, wrapping at the end (default: ]g)",
	config.KeyVimPrevHunk:          vimDoc + "go to the previous git change (hunk) since the index, wrapping at the start (default: [g)",
	config.KeyVimUndoHunk:          vimDoc + "undo the git change (hunk) under the cursor, its lines back as the index has them; u brings the change back (default: guh)",
	config.KeyVimStageHunk:         vimDoc + "stage the git change (hunk) under the cursor, the unsaved text's included; in a staged file's difference, unstage it (default: gsh)",
	config.KeyVimNextError:         vimDoc + "go to the next error, wrapping at the end (default: ]e)",
	config.KeyVimPrevError:         vimDoc + "go to the previous error, wrapping at the start (default: [e)",
	config.KeyVimHover:             vimDoc + "show the table card or documentation of what is under the cursor (default: K)",
	config.KeyVimDefinition:        vimDoc + "go to the definition of what is under the cursor; ctrl+o comes back (default: gd)",
	config.KeyVimMenu:              vimDoc + "open the source pane's context menu at the cursor, as a right-click does (default: gm)",
	config.KeyVimLeader:            "`keys` that <leader> stands for in xf web's other vim keys, as vim's mapleader (default: <Space>)",
	config.KeyVimBufferPrev:        vimDoc + "show the source pane's previous buffer (file opened), wrapping at the first, as :bp (default: <leader>[)",
	config.KeyVimBufferNext:        vimDoc + "show the source pane's next buffer (file opened), wrapping at the last, as :bn (default: <leader>])",
	config.KeyVimBuffers:           vimDoc + "list the source pane's buffers (files opened) to pick one, as :ls (default: <leader>b)",
	config.KeyVimBufferDelete:      vimDoc + "remove the buffer shown from the source pane's buffers, going to the alternate one first, as :bd (default: <leader>x)",
	config.KeyVimFiles:             vimDoc + "find a file to open, as the palette's cmd+p (ctrl+p off a Mac) does (default: <leader>f)",
	config.KeyVimFormat:            vimDoc + "format the file's SQL with sqlfluff and its config block, as :format does; the result stays unsaved and u undoes it (default: none, e.g. <leader>=)",
}

// keyValue is the value column of the config key table in the help.
var keyValue = map[string]string{
	config.KeyDataformBin:          "path; relative to the folder holding .xf/",
	config.KeyDbtBin:               "path; relative to the folder holding .xf/",
	config.KeyDbtTarget:            "target in profiles.yml, e.g. dev",
	config.KeyDbtProdTarget:        "target in profiles.yml, e.g. prod",
	config.KeyDbtCompileWithHooks:  "true or false",
	config.KeyRunMode:              `"cli" or "api"`,
	config.KeyDataformLocation:     "region, e.g. europe-west2",
	config.KeyDataformRepo:         "Dataform repository name",
	config.KeyDefaultBranch:        "branch name, e.g. main",
	config.KeyTablePrefix:          "letters, digits, _ and -, e.g. tmp",
	config.KeySchemaSuffix:         "letters, digits, _ and -, e.g. ashish",
	config.KeyDatabaseSuffix:       "letters, digits, _ and -, e.g. dev",
	config.KeyDevCompilerOptions:   `e.g. "--schema-suffix=ashish"`,
	config.KeyProdCompilerOptions:  `e.g. "--default-database=acme-prod"`,
	config.KeyEditor:               `command, e.g. "code" or "nvim"`,
	config.KeyTheme:                `"auto", "light" or "dark"`,
	config.KeyWebTheme:             `"auto" or a theme, e.g. "dracula"`,
	config.KeyWebThemeLight:        `a theme, e.g. "github-light"`,
	config.KeyWebThemeDark:         `a theme, e.g. "nord"`,
	config.KeyScratchWarnGB:        "GiB, e.g. 20",
	config.KeyScratchConfirmGB:     "GiB, e.g. 50",
	config.KeySourceAutosaveMs:     "milliseconds, e.g. 1500",
	config.KeyDiffStyle:            `"inline" or "side-by-side"`,
	config.KeyPythonLanguageServer: `"pyrefly" or "off"`,
	config.KeySqlfluffBin:          "path; relative to the folder holding .xf/",
	config.KeySqlfluffConfig:       "path; relative to the folder holding .xf/",
	config.KeyAutoHidePane2:        "true or false",
	config.KeyDeferToProd:          "true or false",
	config.KeyPane2Tab:             `"sql" or "columns"`,
	config.KeyWrapLines:            "true or false",
	config.KeyWebAutoExitSeconds:   "seconds, e.g. 45; 0 is off",
	config.KeyWebOpenProjectIn:     `"same-tab" or "new-tab"`,
	config.KeyWebBrowser:           `"default", "app", or a browser, e.g. "msedge"`,
	config.KeyWebBrowserProfile:    `a profile directory, e.g. "Profile 1"`,
	config.KeyForgePollSeconds:     "seconds, e.g. 10; 5 or more",
	config.KeyVimMode:              "true or false",
	config.KeyVimNextHunk:          "vim keys, e.g. ]g",
	config.KeyVimPrevHunk:          "vim keys, e.g. [g",
	config.KeyVimUndoHunk:          "vim keys, e.g. guh",
	config.KeyVimStageHunk:         "vim keys, e.g. gsh",
	config.KeyVimNextError:         "vim keys, e.g. ]e",
	config.KeyVimPrevError:         "vim keys, e.g. [e",
	config.KeyVimHover:             "vim keys, e.g. K",
	config.KeyVimDefinition:        "vim keys, e.g. gd",
	config.KeyVimMenu:              "vim keys, e.g. gm",
	config.KeyVimLeader:            "vim keys, e.g. <Space> or \\",
	config.KeyVimBufferPrev:        "vim keys, e.g. <leader>[",
	config.KeyVimBufferNext:        "vim keys, e.g. <leader>]",
	config.KeyVimBuffers:           "vim keys, e.g. <leader>b",
	config.KeyVimBufferDelete:      "vim keys, e.g. <leader>x",
	config.KeyVimFiles:             "vim keys, e.g. <leader>f",
	config.KeyVimFormat:            "vim keys, e.g. <leader>=",
}

// settingFlags are the flags of a command that mirror config keys, and
// --config.
type settingFlags struct {
	fs     *flag.FlagSet
	values map[string]*string // by config key
	config *string
	// noProject skips the project file (xf init replaces it).
	noProject bool
	// remote: xf web answers other hosts (--allow-host), so auto-exit is
	// off unless a flag or the user's file sets it.
	remote bool
}

// addSettingFlags registers --config and the flags of keys on fs; a key
// with no flag (the vim keys) is skipped.
func addSettingFlags(fs *flag.FlagSet, keys ...string) *settingFlags {
	sf := &settingFlags{fs: fs, values: map[string]*string{}}
	for _, k := range keys {
		if config.Flags[k] == "" {
			continue
		}
		sf.values[k] = fs.String(config.Flags[k], "", flagUsage[k])
	}
	sf.config = fs.String("config", "", "read the JSON file at `path` instead of .xf/config.json ('' reads none)")
	return sf
}

// load reads the config files for root ("" outside a project) and applies
// the flags that were given on top.
func (sf *settingFlags) load(root string) (config.Loaded, error) {
	opts := config.Options{Root: root}
	sf.fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			opts.Project = sf.config
		}
	})
	if sf.noProject {
		none := ""
		opts.Project = &none
	}
	l, err := config.Load(opts)
	if err != nil {
		return l, err
	}
	sf.apply(&l)
	if l.RunMode != "" && l.RunMode != "cli" && l.RunMode != "api" {
		return l, fmt.Errorf("--run-mode must be cli or api, not %q", l.RunMode)
	}
	if t := l.Theme; t != "" && t != config.ThemeAuto && t != config.ThemeLight && t != config.ThemeDark {
		return l, fmt.Errorf("--%s must be auto, light or dark, not %q", config.Flags[config.KeyTheme], t)
	}
	if t := l.WebTheme; t != "" {
		if err := config.Check(config.KeyWebTheme, t); err != nil {
			return l, fmt.Errorf("--%s: %w", config.Flags[config.KeyWebTheme], err)
		}
	}
	if v := l.WebAutoExitSeconds; v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 {
			return l, fmt.Errorf("--%s must be a whole number of seconds, 0 or more, not %q", config.Flags[config.KeyWebAutoExitSeconds], v)
		}
	}
	if v := l.DbtCompileWithHooks; v != "" && v != "true" && v != "false" {
		return l, fmt.Errorf("--dbt-compile-with-hooks must be true or false, not %q", v)
	}
	return l, nil
}

// apply sets the settings whose flags were given.
func (sf *settingFlags) apply(l *config.Loaded) {
	sf.fs.Visit(func(f *flag.Flag) {
		for k, v := range sf.values {
			if config.Flags[k] == f.Name {
				l.Set(k, *v, "--"+f.Name)
			}
		}
	})
}

// given lists the flags that were given, as arguments to pass on (to the
// xf in the nvim split), leaving out skip.
func (sf *settingFlags) given(skip ...string) []string {
	var args []string
	sf.fs.Visit(func(f *flag.Flag) {
		for _, s := range skip {
			if f.Name == s {
				return
			}
		}
		args = append(args, "--"+f.Name, f.Value.String())
	})
	return args
}

// resolveBin finds the dataform binary the settings name, or the default.
func resolveBin(l config.Loaded) (string, error) {
	return dataform.ResolveBinaryFrom(l.DataformBin, l.Sources[config.KeyDataformBin], os.Getenv, exec.LookPath)
}

// resolveDbtBin finds the dbt binary the settings name, or the default for
// the project at root.
func resolveDbtBin(l config.Loaded, root string) (string, error) {
	return dbt.ResolveBinaryFrom(l.DbtBin, l.Sources[config.KeyDbtBin], root, os.Getenv, exec.LookPath)
}

// parseArgs parses a command's flags, printing its usage for -h/--help;
// help reports that it did.
func parseArgs(fs *flag.FlagSet, args []string) (help bool, err error) {
	fs.SetOutput(io.Discard) // errors are returned; usage printed below
	err = fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(os.Stdout)
		fs.Usage()
		return true, nil
	}
	if err != nil {
		fs.SetOutput(os.Stderr)
		return false, fmt.Errorf("%w (see %s --help)", err, fs.Name())
	}
	return false, nil
}

// commandUsage sets fs's usage to a summary line and its flags.
func commandUsage(fs *flag.FlagSet, usage, summary string) {
	fs.Usage = func() {
		out := fs.Output()
		_, _ = fmt.Fprintf(out, "usage: %s\n\n%s\n\nflags:\n", usage, summary)
		printFlags(out, fs)
	}
}

// printFlags lists fs's flags as --name, as they are usually typed.
func printFlags(w io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		if f.Usage == "" {
			return
		}
		name, usage := flag.UnquoteUsage(f)
		arg := "--" + f.Name
		if name != "" {
			arg += " " + name
		}
		_, _ = fmt.Fprintf(w, "  %s\n      %s\n", arg, usage)
	})
}

// mainHelp is xf's full help: what it does, its commands, flags, config
// file, API runs and main keys. flags is the top-level flag set.
func mainHelp(w io.Writer, flags *flag.FlagSet) {
	p := func(s string) { _, _ = io.WriteString(w, s) }
	p(`xf: a lazygit-style terminal UI for Dataform and dbt projects.

Run it in a project's root, the directory with workflow_settings.yaml or
dataform.json (Dataform) or dbt_project.yml (dbt), or pass --dir.

usage:
  xf [flags]                 open the TUI for the project in the working directory,
                              or outside any project, pick a project xf has opened or found
  xf --nvim [flags] [file…]  open neovim with the files and xf in a split beside it
                              (the :Xf command of the plugin in nvim/)
  xf web [flags]             open the TUI's panes and keys in your browser
                              (--port, --no-open); ctrl+c quits
  xf scratch [flags]         open a new empty SQL scratch query that runs in BigQuery,
                              in your browser (--project, --location, --port, --no-open)
  xf search [flags] [dir…]   find the Dataform and dbt projects in the folders (default:
                              the working directory) and below, for the picker to list
  xf init [--force]          write .xf/config.json with the project's Dataform
                              repository and the region it lives in
  xf config [flags]          show the settings xf would use and where each came from;
                             --auth adds the Google accounts, --json prints JSON
  xf impact [flags]          print the column impact of the branch's changes as
                              Markdown for a pull request (--fail-on to gate CI)
  xf cost [flags]            print what the branch's changed actions would scan in
                              BigQuery and what that costs (--fail-over to gate CI)
  xf run [flags]             dataform run (dbt build) with its own flags; --defer reads
                              upstream tables not built in dev from prod, --dry-run plans
  xf lsp                    language server for editors (LSP on stdin and stdout)
  xf serve [--dir dir]       the engine xf and xf lsp share; started for you and
                              stopped a few seconds after the last of them leaves
  xf stop                    stop your xf engines and list the xf processes still
                              running (close them before upgrading on Windows)
  xf update                  update xf to the latest release, the way it was installed
                              (brew, go install, or the install script; also xf --update)
  xf help                    this help; xf <command> --help for a command's flags
  xf --version               the version of xf: the commit it was built from and when

flags:
`)
	printFlags(w, flags)
	p(`
config:
  Every flag except --nvim and --config can be set in a JSON file instead,
  and the vim* keys of xf web's editor are set only there. Later sources win:

    defaults < ~/.config/xf/config.json < .xf/config.json < flags

  .xf/config.json sits at the project root, beside ` + dataform.SettingsFile + `, and is
  meant to be committed so the team shares it; ` + "`xf init`" + ` writes it. The user
  file (in $XDG_CONFIG_HOME/xf when that is set) holds personal defaults.
  --config path reads another file instead of .xf/config.json; --config ''
  reads none. A relative dataformBin is relative to the folder holding .xf/
  (or the file). Unknown keys are warned about; a malformed file stops xf
  and names its line. ` + "`xf config`" + ` shows what is in effect.

    key                  flag                    value
`)
	for _, k := range config.Keys {
		fl := "--" + config.Flags[k]
		if config.Flags[k] == "" {
			fl = "(none)"
		}
		_, _ = fmt.Fprintf(w, "    %-20s %-23s %s\n", k, fl, keyValue[k])
	}
	p(`
  for example:

    {
      "runMode": "api",
      "dataformLocation": "europe-west2",
      "dataformRepo": "my_repo"
    }

API runs:
  With runMode api (or a in the Run pane), r runs the selected action in GCP
  through the Dataform API instead of ` + "`dataform run`" + `. Dataform compiles the
  branch pushed to origin, not the files on disk; the log warns when they
  differ, then follows the workflow invocation. Dataform repositories live in
  a region such as europe-west2, not a multi-region like EU, so set
  dataformLocation (` + "`xf init`" + ` looks it up). Credentials are Application
  Default Credentials: gcloud auth application-default login. , in the TUI
  and ` + "`xf config --auth`" + ` show which account that is.

run changed:
  R lists the actions whose compiled output differs from the merge-base with
  defaultBranch (origin/<branch>, else the local branch; nothing is fetched),
  and runs them all. The first R of the day compiles that commit, cached under
  the user cache directory; after that / and the Actions pane mark them. With
  the API toggle on, it compares the branch pushed to origin instead.

column impact:
  I (or xf impact) checks what the Changed Actions do to their tables' columns:
  each changed table, view and incremental table is dry run and compared with
  its table in BigQuery: the action's default target, or with
  prodCompilerOptions set (e.g. --default-database=acme-prod) the table a
  compile with those options names. Without them the first I or C asks for
  them, and the reports say the default targets, which may be dev tables,
  were compared. An action new on the branch is listed as new, and a change
  the base branch already made is marked as its, and counts as low. Dataplex Data
  Lineage is asked who reads the columns it drops or retypes, and the tables
  it deletes. The report is the Markdown a pull
  request gets; y copies it. It needs the Data Lineage API and
  roles/datalineage.viewer, and lineage runs about 2 h behind prod runs. In
  a dbt project (xf web and xf impact), dbtProdTarget (e.g. prod) compiles
  the branch with that target, dry runs that and compares it with the tables
  it builds; without it, with the tables xf's target builds.

query cost:
  C (or xf cost) estimates what the Changed Actions would scan in BigQuery: each
  one with a query is dry run, as it is on the branch and as it was at the
  merge-base, in Dataform and dbt projects. An incremental table counts its
  incremental run. With prodCompilerOptions set (dbtProdTarget in dbt), the
  dry runs are of the project compiled with them, so they read the tables
  prod runs read. A read
  of a partitioned table that never names its partition column is marked. y
  copies the report as Markdown; xf cost --fail-over 100GB fails a CI job.

keys (? in xf lists them all):
  /      find an action       r      run                  R      run changed actions
  I      column impact of the changes                     C      query cost of the changes
  d / D  deps / dependents    f      full refresh         a      run via the API
  s      run the selected action only (r runs its whole file)
  c      compile              e      errors (y copies)    0-4    focus a pane
  tab    next pane            z      zoom the pane        L      show/hide the log
  O      show/hide lineage    o      open in editor        q      quit
  u      open an API run in the browser (log pane)
  ctrl+c cancel the run
`)
}

// helpRequested says whether args asks for xf's help.
func helpRequested(args []string) bool {
	return len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help")
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}
