package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/web"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// settingMeta is how xf web's settings page shows a key: the section it is
// in, its control, and whether xf web applies a change at once (live) or
// when the project reopens.
type settingMeta struct {
	section, kind string
	choices       []string
	live          bool
}

var themes = []string{config.ThemeAuto, config.ThemeLight, config.ThemeDark}

// webThemes are the ids of xf web's themes of kind, or with kind "" auto
// and every theme.
func webThemes(kind string) []string {
	var out []string
	if kind == "" {
		out = append(out, webtheme.Auto)
	}
	for _, t := range webtheme.All() {
		if kind == "" || t.Kind == kind {
			out = append(out, t.ID)
		}
	}
	return out
}

// settingSections lists xf web's settings sections in display order, under
// their sidebar groups. Warehouse marks warehouse-specific sections so the
// page hides irrelevant warehouses inside a project.
var settingSections = []web.SettingSection{
	{Name: "Dataform", Group: "Tool"},
	{Name: "dbt", Group: "Tool"},
	{Name: "BigQuery", Group: "Warehouse", Warehouse: project.BigQuery},
	{Name: "Databricks", Group: "Warehouse", Warehouse: project.Databricks},
	{Name: "Snowflake", Group: "Warehouse", Warehouse: project.Snowflake},
	{Name: "Checks", Group: "General"},
	{Name: "Git", Group: "General"},
	{Name: "Editor", Group: "General"},
	{Name: "Appearance", Group: "General"},
	{Name: "App", Group: "General"},
	{Name: "Vim", Group: "General"},
}

var settingMetas = map[string]settingMeta{
	config.KeyDataformBin:          {"Dataform", "text", nil, false},
	config.KeyRunMode:              {"Dataform", "choice", []string{"cli", "api"}, false},
	config.KeyDataformLocation:     {"Dataform", "text", nil, false},
	config.KeyDataformRepo:         {"Dataform", "text", nil, false},
	config.KeyTablePrefix:          {"Dataform", "text", nil, false},
	config.KeySchemaSuffix:         {"Dataform", "text", nil, false},
	config.KeyDatabaseSuffix:       {"Dataform", "text", nil, false},
	config.KeyDevCompilerOptions:   {"Dataform", "text", nil, false},
	config.KeyProdCompilerOptions:  {"Dataform", "text", nil, false},
	config.KeyDbtBin:               {"dbt", "text", nil, false},
	config.KeyDbtTarget:            {"dbt", "text", nil, false},
	config.KeyDbtProdTarget:        {"dbt", "text", nil, false},
	config.KeyDbtCompileWithHooks:  {"dbt", "bool", nil, false},
	config.KeyLineageProjects:      {"BigQuery", "text", nil, false},
	config.KeyQueryHistoryProjects: {"BigQuery", "text", nil, false},
	config.KeyQueryHistoryDays:     {"Checks", "number", nil, false},
	config.KeyQueryHistoryMaxGB:    {"BigQuery", "number", nil, false},
	config.KeyDeferToProd:          {"BigQuery", "bool", nil, false},
	config.KeyUSDPerTiB:            {"BigQuery", "number", nil, true},
	config.KeyScratchWarnGB:        {"BigQuery", "number", nil, true},
	config.KeyScratchConfirmGB:     {"BigQuery", "number", nil, true},
	config.KeyDatabricksWarehouse:  {"Databricks", "text", nil, false},
	config.KeyDatabricksProfile:    {"Databricks", "text", nil, false},
	config.KeySnowflakeWarehouse:   {"Snowflake", "text", nil, false},
	config.KeySnowflakeConnection:  {"Snowflake", "text", nil, false},
	config.KeyDefaultBranch:        {"Git", "text", nil, true},
	config.KeyDiffStyle:            {"Git", "choice", []string{config.DiffInline, config.DiffSideBySide}, true},
	config.KeyForgePollSeconds:     {"Git", "number", nil, true},
	config.KeySourceAutosaveMs:     {"Editor", "number", nil, true},
	config.KeyPythonLanguageServer: {"Editor", "choice", []string{config.PythonPyrefly, config.PythonOff}, true},
	config.KeySqlfluffBin:          {"Editor", "text", nil, true},
	config.KeySqlfluffConfig:       {"Editor", "text", nil, true},
	config.KeyAutoHidePane2:        {"Editor", "bool", nil, true},
	config.KeyPane2Tab:             {"Editor", "choice", []string{config.Pane2SQL, config.Pane2Columns}, true},
	config.KeyPanelPosition:        {"Editor", "choice", []string{config.PanelBottom, config.PanelRight}, true},
	config.KeyWrapLines:            {"Editor", "bool", nil, true},
	config.KeyTheme:                {"Appearance", "choice", themes, true},
	config.KeyWebTheme:             {"Appearance", "choice", webThemes(""), true},
	config.KeyWebThemeLight:        {"Appearance", "choice", webThemes(webtheme.Light), true},
	config.KeyWebThemeDark:         {"Appearance", "choice", webThemes(webtheme.Dark), true},
	config.KeyEditor:               {"App", "text", nil, true},
	config.KeyWebAutoExitSeconds:   {"App", "number", nil, true},
	config.KeyWebOpenProjectIn:     {"App", "choice", []string{config.OpenSameTab, config.OpenNewTab}, true},
	config.KeyWebBrowser:           {"App", "text", nil, true},
	config.KeyWebBrowserProfile:    {"App", "text", nil, true},
	config.KeyVimMode:              {"Vim", "bool", nil, true},
	config.KeyVimNextHunk:          {"Vim", "text", nil, true},
	config.KeyVimPrevHunk:          {"Vim", "text", nil, true},
	config.KeyVimUndoHunk:          {"Vim", "text", nil, true},
	config.KeyVimStageHunk:         {"Vim", "text", nil, true},
	config.KeyVimNextError:         {"Vim", "text", nil, true},
	config.KeyVimPrevError:         {"Vim", "text", nil, true},
	config.KeyVimHover:             {"Vim", "text", nil, true},
	config.KeyVimDefinition:        {"Vim", "text", nil, true},
	config.KeyVimMenu:              {"Vim", "text", nil, true},
	config.KeyVimLeader:            {"Vim", "text", nil, true},
	config.KeyVimBufferPrev:        {"Vim", "text", nil, true},
	config.KeyVimBufferNext:        {"Vim", "text", nil, true},
	config.KeyVimBuffers:           {"Vim", "text", nil, true},
	config.KeyVimBufferDelete:      {"Vim", "text", nil, true},
	config.KeyVimFiles:             {"Vim", "text", nil, true},
	config.KeyVimFileTree:          {"Vim", "text", nil, true},
	config.KeyVimFormat:            {"Vim", "text", nil, true},
}

// dataformOnly and dbtOnly are the keys of one tool, which the page leaves
// out of the other's projects.
var (
	dataformOnly = []string{config.KeyDataformBin, config.KeyRunMode, config.KeyDataformLocation, config.KeyDataformRepo,
		config.KeyTablePrefix, config.KeySchemaSuffix, config.KeyDatabaseSuffix, config.KeyDevCompilerOptions, config.KeyProdCompilerOptions}
	dbtOnly = []string{config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtProdTarget, config.KeyDbtCompileWithHooks,
		config.KeyDatabricksWarehouse, config.KeyDatabricksProfile, config.KeySnowflakeWarehouse, config.KeySnowflakeConnection}
)

// settingsKeys are the keys the page lists for a project of tool, in the
// documented order; outside a project (tool "") every key.
func settingsKeys(tool string) []string {
	var out []string
	for _, k := range config.Keys {
		if tool == "dbt" && slices.Contains(dataformOnly, k) || tool == "dataform" && slices.Contains(dbtOnly, k) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// usageDefault is the "(default: …)" a flag's usage ends with, which the
// page shows as the value in use instead.
var usageDefault = regexp.MustCompile(`\s*\(default:.*\)$`)

// settingDoc is a key's description for the page: its flag's usage,

// webDocs are the page's own words for the keys it shows as forms (ADR
// 0037), whose flag usage talks of flags the form fills in.
var webDocs = map[string]string{
	config.KeyProdCompilerOptions: "where the project's prod runs build: column impact compares with these tables, query cost dry runs against them and defer to prod reads them. More flags are any other flags of dataform compile; all empty says the default targets are prod",
	config.KeyDbtProdTarget:       "the profile target of the project's prod runs: column impact compares with its tables and query cost dry runs against them",
}

// without the backticks that name the flag's argument or its default.
func settingDoc(key string) string {
	if d, ok := webDocs[key]; ok {
		return d
	}
	return usageDefault.ReplaceAllString(strings.ReplaceAll(strings.TrimPrefix(flagUsage[key], vimDoc), "`", ""), "")
}

// webSettings is what xf web's settings page needs of the config files:
// what each file says, the value in use with where it comes from (as xf
// config shows it), and writing a key with the check a file's value gets.
func webSettings(sf *settingFlags) *web.Settings {
	return &web.Settings{
		View: func(root, tool string) web.SettingsView { return settingsView(sf, root, tool) },
		Set: func(root, level, key, value string, unset bool) (string, error) {
			if !unset {
				if err := config.Check(key, value); err != nil {
					return "", err
				}
				if err := vimKeyTaken(sf, root, key, value); err != nil {
					return "", err
				}
			}
			switch {
			case level == "user" && unset:
				return config.UnsetUserKey(key)
			case level == "user":
				return config.SetUserKey(key, value)
			case root == "":
				return "", errors.New("no project is open")
			case config.UserOnly(key):
				return "", errors.New(key + " is set in your user config only")
			case unset:
				return config.UnsetProjectKey(root, key)
			}
			return config.SetProjectKey(root, key, value)
		},
		File: func(root, level string) (string, error) {
			switch {
			case level == "user":
				return config.UserFile()
			case root == "":
				return "", errors.New("no project is open")
			}
			return filepath.Join(root, config.ProjectFile), nil
		},
	}
}

// vimKeyTaken says which other vim key already has keys, as two can't
// share them.
func vimKeyTaken(sf *settingFlags, root, key, keys string) error {
	if !config.IsVimKey(key) {
		return nil
	}
	l, err := sf.load(root)
	if err != nil {
		return nil // the load's own error shows on the page
	}
	return l.VimClash(key, keys)
}

// vimKeys are the vim keys' sequences in use, by key.
func vimKeys(l config.Settings) map[string]string {
	out := map[string]string{}
	for _, k := range config.VimKeys {
		out[k] = l.VimKey(k)
	}
	return out
}

func settingsView(sf *settingFlags, root, tool string) web.SettingsView {
	v := web.SettingsView{Levels: []web.SettingsLevel{}, Warnings: []string{}, Sections: settingSections, Keys: []web.SettingKey{}}
	home, _ := os.UserHomeDir()
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	userPath, _ := config.UserFile()
	user := web.SettingsLevel{Level: "user", Path: frontend.ShortPath(home, userPath), Found: exists(userPath)}
	userVals, err := config.ReadKeys(userPath)
	none := ""
	if _, lerr := config.Load(config.Options{Project: &none}); lerr != nil {
		user.Error = lerr.Error()
	} else if err != nil {
		user.Error = err.Error()
	}
	v.Levels = append(v.Levels, user)

	var projectVals map[string]string
	if root != "" {
		path := filepath.Join(root, config.ProjectFile)
		project := web.SettingsLevel{Level: "project", Path: config.ProjectFile, Found: exists(path)}
		projectVals, err = config.ReadKeys(path)
		replaced := false
		sf.fs.Visit(func(f *flag.Flag) { replaced = replaced || f.Name == "config" })
		switch {
		case replaced:
			project.Error = "xf web was started with --config, which it reads instead of " + config.ProjectFile
		case err != nil:
			project.Error = err.Error()
		case user.Error == "":
			if _, lerr := config.Load(config.Options{Root: root}); lerr != nil {
				project.Error = lerr.Error()
			}
		}
		v.Levels = append(v.Levels, project)
	}

	used, err := sf.load(root)
	if err != nil {
		used = config.Loaded{Sources: map[string]string{}}
	}
	v.Warnings = append(v.Warnings, used.Warnings...)
	keys := settingsKeys(tool)
	rows := settingRows(used, root, keys)
	for i, k := range keys {
		if k == config.KeyWebAutoExitSeconds && sf.remote && !used.IsSet(k) {
			rows[i].value, rows[i].from = "0", "default with --allow-host: off"
		}
	}
	for i, k := range keys {
		m := settingMetas[k]
		sk := web.SettingKey{Key: k, Title: config.Titles[k], Section: m.section, Doc: settingDoc(k), Default: config.Defaults[k], Hint: keyValue[k], Kind: m.kind, Choices: m.choices,
			UserOnly: config.UserOnly(k), Live: m.live, Value: rows[i].value, From: rows[i].from}
		if val, ok := userVals[k]; ok {
			sk.User = &val
		}
		if val, ok := projectVals[k]; ok && !sk.UserOnly {
			sk.Project = &val
		}
		if strings.HasPrefix(used.Sources[k], "--") {
			val := used.Get(k)
			sk.Flag = &val
		}
		v.Keys = append(v.Keys, sk)
	}
	return v
}
