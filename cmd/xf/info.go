package main

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/format"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/remote"
)

// infoSettings are the settings each tool's projects have, less the
// binary and dbt's target, which the project info shows on their own.
var infoSettings = map[string][]string{
	backend.Dataform: {config.KeyRunMode, config.KeyDataformLocation, config.KeyDataformRepo, config.KeyDefaultBranch, config.KeyTablePrefix, config.KeySchemaSuffix, config.KeyDatabaseSuffix, config.KeyDevCompilerOptions, config.KeyProdCompilerOptions, config.KeyDeferToProd, config.KeyEditor, config.KeyTheme, config.KeyWebTheme, config.KeyWebThemeLight, config.KeyWebThemeDark, config.KeyScratchWarnGB, config.KeyScratchConfirmGB, config.KeySourceAutosaveMs, config.KeyDiffStyle, config.KeyPythonLanguageServer, config.KeySqlfluffBin, config.KeySqlfluffConfig, config.KeyAutoHidePane2, config.KeyPane2Tab, config.KeyWrapLines, config.KeyWebAutoExitSeconds, config.KeyWebOpenProjectIn, config.KeyWebBrowser, config.KeyForgePollSeconds, config.KeyVimMode},
	backend.DBT:      {config.KeyDbtProdTarget, config.KeyDeferToProd, config.KeyDbtCompileWithHooks, config.KeyDefaultBranch, config.KeyEditor, config.KeyTheme, config.KeyWebTheme, config.KeyWebThemeLight, config.KeyWebThemeDark, config.KeyScratchWarnGB, config.KeyScratchConfirmGB, config.KeySourceAutosaveMs, config.KeyDiffStyle, config.KeyPythonLanguageServer, config.KeyAutoHidePane2, config.KeyPane2Tab, config.KeyWrapLines, config.KeyWebAutoExitSeconds, config.KeyWebOpenProjectIn, config.KeyWebBrowser, config.KeyForgePollSeconds, config.KeyVimMode},
}

// projectInfo is what the TUI's , overlay shows of the project at root,
// which b compiles with settings, and of this xf.
func projectInfo(root, name string, b backend.Backend, settings config.Loaded) *frontend.ProjectInfo {
	engine := backend.EngineName(b)
	info := &frontend.ProjectInfo{
		BackendFrom:   "--backend",
		Exe:           "(unknown)",
		Build:         buildText(debug.ReadBuildInfo, thisBuild.Time),
		Warnings:      settings.Warnings,
		EditorCommand: cmp.Or(settings.Editor, first(defaultEditor(os.Getenv))),
		Socket:        remote.SocketPath(root, engine),
		Log:           remote.LogPath(root, engine),
		Git:           func(ctx context.Context) (frontend.InfoGit, error) { return readInfoGit(ctx, root) },
		ADC:           gcpauth.Lookup{}.ADC,
		Gcloud:        gcpauth.Lookup{}.Gcloud,
	}
	// A directory that is one tool's project needs no --backend, even when
	// the xf in a neovim split is always given it.
	if detected, err := backend.Detect(root, ""); err == nil && detected == name {
		info.BackendFrom = "detected"
	}
	if exe, err := os.Executable(); err == nil {
		info.Exe = exe
	}
	row := func(key string) settingRow { return settingRows(settings, root, []string{key})[0] }
	switch b := b.(type) {
	case dataform.Backend:
		info.ToolBin, info.ToolFrom = absPath(b.Bin), row(config.KeyDataformBin).from
		if b.Bin == "" { // xf web opened it without the CLI
			info.ToolBin = "(not on PATH when xf web opened it)"
		}
		info.ToolVersion = b.Version
		info.Format = sqlfluffInfo(root, settings, row(config.KeySqlfluffBin).from)
	case *dbt.Backend:
		info.ToolBin, info.ToolFrom = absPath(b.Bin), row(config.KeyDbtBin).from
		info.ToolVersion = b.Version
		target := row(config.KeyDbtTarget)
		info.Target, info.TargetFrom = target.value, target.from
	}
	for _, f := range settings.Files {
		info.Files = append(info.Files, frontend.InfoFile{Path: f.Abs, Found: f.Found})
	}
	for _, r := range settingRows(settings, root, infoSettings[name]) {
		info.Settings = append(info.Settings, frontend.InfoSetting{Key: r.key, Value: r.value, From: r.from})
	}
	return info
}

// landingInfo is what xf web's settings page shows outside any project:
// this xf, the tools PATH finds and the Google accounts.
func landingInfo() frontend.LandingInfo {
	info := frontend.LandingInfo{
		Exe:    "(unknown)",
		Build:  buildText(debug.ReadBuildInfo, thisBuild.Time),
		ADC:    gcpauth.Lookup{}.ADC,
		Gcloud: gcpauth.Lookup{}.Gcloud,
	}
	if exe, err := os.Executable(); err == nil {
		info.Exe = exe
	}
	tool := func(name, note string, version func(bin string) func(ctx context.Context) (string, error)) {
		t := frontend.LandingTool{Name: name, Note: note}
		if bin, err := exec.LookPath(name); err == nil {
			t.Bin, t.Version = bin, version(bin)
		}
		info.Tools = append(info.Tools, t)
	}
	tool("dataform", "a project can have its own, in node_modules/.bin", func(bin string) func(ctx context.Context) (string, error) {
		return dataform.Backend{Bin: bin}.Version
	})
	tool("dbt", "", func(bin string) func(ctx context.Context) (string, error) {
		return (&dbt.Backend{Bin: bin}).Version
	})
	tool("gcloud", "", func(bin string) func(ctx context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			out, err := cmdrun.Command(ctx, bin, "--version").CombinedOutput()
			if err != nil {
				return "", cmdrun.VersionError(out, err)
			}
			return cmdrun.FirstLine(cmdrun.StripANSI(string(out))), nil
		}
	})
	return info
}

// webAuthAlerts are the sign-in problems xf web's picker warns of.
func webAuthAlerts(ctx context.Context) []gcpauth.Alert {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var adc gcpauth.ADC
	var cli gcpauth.CLI
	var wg sync.WaitGroup
	wg.Go(func() { adc = gcpauth.Lookup{}.ADC(ctx) })
	wg.Go(func() { cli = gcpauth.Lookup{}.Gcloud(ctx) })
	wg.Wait()
	return gcpauth.Alerts(adc, cli)
}

// absPath is path made absolute, or path itself when that fails.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// versionRequested says whether args ask for xf's version.
func versionRequested(args []string) bool {
	return len(args) > 0 && slices.Contains([]string{"version", "-v", "-version", "--version"}, args[0])
}

// versionText is what xf --version prints: until xf has releases, the
// commit it was built from and when that commit was made.
func versionText() string {
	return "xf " + buildText(debug.ReadBuildInfo, thisBuild.Time)
}

// pseudoVersion matches the version Go gives a build from a commit that
// has no tag, e.g. v0.0.0-20261005224111-fe5033068497.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)

// buildText says which build of xf this is, from what Go recorded in it:
// the module's version, or for a build from an untagged commit "devel" and
// the commit, its time (UTC) and whether the checkout had uncommitted changes. A
// build with neither says when the executable was written (modified).
func buildText(read func() (*debug.BuildInfo, bool), modified time.Time) string {
	var parts []string
	if bi, ok := read(); ok {
		setting := func(key string) string {
			if i := slices.IndexFunc(bi.Settings, func(s debug.BuildSetting) bool { return s.Key == key }); i >= 0 {
				return bi.Settings[i].Value
			}
			return ""
		}
		if v := bi.Main.Version; v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
			parts = append(parts, v)
		} else if rev := setting("vcs.revision"); rev != "" {
			parts = append(parts, "devel", rev[:min(len(rev), 7)])
			if t, err := time.Parse(time.RFC3339, setting("vcs.time")); err == nil {
				parts = append(parts, t.UTC().Format("2006-01-02 15:04")+" UTC")
			}
			if setting("vcs.modified") == "true" {
				parts = append(parts, "with uncommitted changes")
			}
		}
		if len(parts) > 0 && bi.GoVersion != "" {
			parts = append(parts, "("+bi.GoVersion+")")
		}
	}
	if len(parts) == 0 {
		if modified.IsZero() {
			return "(unknown build)"
		}
		return "built " + modified.Local().Format("2006-01-02 15:04")
	}
	return strings.Join(parts, " ")
}

// readInfoGit reads the branch and origin of the checkout that holds dir.
func readInfoGit(ctx context.Context, dir string) (frontend.InfoGit, error) {
	git := func(args ...string) (string, error) {
		out, err := cmdrun.Git(ctx, dir, args...).Output()
		return strings.TrimRight(string(out), "\r\n"), err
	}
	var g frontend.InfoGit
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return g, errors.New("not a git repository")
	}
	g.Branch, _ = git("symbolic-ref", "--quiet", "--short", "HEAD")
	g.Origin, _ = git("remote", "get-url", "origin")
	return g, nil
}

// sqlfluffInfo is the sqlfluff xf formats with, as the settings page's
// overview shows it: where it was found (from says how) and, asked when the
// page opens, its version.
func sqlfluffInfo(root string, settings config.Loaded, from string) *frontend.InfoBin {
	bin, err := format.FindBin(settings.SqlfluffBin, root, nil)
	if err != nil {
		return &frontend.InfoBin{Bin: "(not found)", From: "uv tool install sqlfluff"}
	}
	// A file in a folder with its own config uses that one: this is the
	// project root's.
	config, configFrom := settings.SqlfluffConfig, "the sqlfluffConfig setting"
	if config == "" {
		if config = format.FindConfig(root, root); config != "" {
			configFrom = "the project's own"
		} else {
			config, configFrom = "(xf's built-in style)", "the project has none: the Dataform Tools extension's style, upper-case keywords, identifiers as written"
		}
	}
	return &frontend.InfoBin{Bin: bin, From: from, Config: config, ConfigFrom: configFrom, Version: func(ctx context.Context) (string, error) {
		out, err := cmdrun.Command(ctx, bin, "--version").CombinedOutput()
		if err != nil {
			return "", cmdrun.VersionError(out, err)
		}
		return strings.TrimPrefix(strings.TrimSpace(string(out)), "sqlfluff, version "), nil
	}}
}
