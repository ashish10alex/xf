package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/format"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// runInit writes .xf/config.json with the project's Dataform repository
// (named after origin) and the region it lives in (looked up in GCP).
func runInit(args []string) error {
	fs := flag.NewFlagSet("xf init", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace an existing .xf/config.json")
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDataformLocation, config.KeyDataformRepo)
	commandUsage(fs, "xf init [flags]", `Writes .xf/config.json at the project root with the keys the team shares:
dataformRepo (the origin remote's name) and dataformLocation (the region the
Dataform repository lives in, looked up in the project's GCP project, which
xf compiles to find). Commit the file. --dataform-repo and
--dataform-location set either instead of looking it up.`)
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf init --help)", fs.Args())
	}
	root, name, err := pf.find()
	if err != nil {
		return err
	}
	if err := requireDataform("xf init", root, name); err != nil {
		return err
	}
	path := filepath.Join(root, config.ProjectFile)
	if existing, err := os.ReadFile(path); err == nil && !*force {
		fmt.Printf("%s already exists:\n\n%s\n", path, indent(string(existing), "  "))
		return errors.New(config.ProjectFile + " already exists; --force replaces it")
	}
	// Only the user file and flags: the project file is being replaced.
	sf.noProject = true
	settings, err := sf.load(root)
	if err != nil {
		return err
	}
	ctx := context.Background()

	repo := settings.DataformRepo
	if repo == "" {
		g, err := dataformapi.ReadGit(ctx, root)
		if err != nil {
			return err
		}
		if repo = dataformapi.RepoNameFromURL(g.Origin); repo == "" {
			return errors.New("no origin remote to name the Dataform repository after: pass --dataform-repo")
		}
	}
	location := settings.DataformLocation
	if location == "" {
		if location, err = lookUpLocation(ctx, settings, root, repo, os.Stdin, os.Stderr); err != nil {
			return err
		}
	}
	s := config.Settings{DataformRepo: repo, DataformLocation: location}
	if _, err := config.Write(root, s, *force); err != nil {
		return err
	}
	fmt.Printf("wrote %s:\n\n%s\n\nCommit it so everyone's xf uses it.\n", path, indent(string(config.Marshal(s)), "  "))
	return nil
}

// lookUpLocation finds the region of the Dataform repository repo in the
// project's GCP project, asking on in when it can't tell.
func lookUpLocation(ctx context.Context, settings config.Loaded, root, repo string, in io.Reader, out io.Writer) (string, error) {
	bin, err := resolveBin(settings)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintln(out, "compiling to find the GCP project…")
	g, err := dataform.Compile(ctx, bin, root)
	if err != nil {
		return "", err
	}
	project := g.DefaultDatabase
	if project == "" {
		return "", errors.New("no defaultProject in " + dataform.SettingsFile)
	}
	client, err := dataformapi.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, dataformapi.AuthHint)
	}
	_, _ = fmt.Fprintf(out, "looking for Dataform repository %q in %s…\n", repo, project)
	found, err := client.FindRepository(ctx, project, repo)
	if err != nil {
		_, _ = fmt.Fprintf(out, "could not look it up: %v\n", err)
	}
	return chooseLocation(found, repo, project, in, out)
}

// chooseLocation picks the region from those the repository was found in,
// asking on in when there isn't exactly one.
func chooseLocation(found []string, repo, project string, in io.Reader, out io.Writer) (string, error) {
	if len(found) == 1 {
		_, _ = fmt.Fprintf(out, "found it in %s\n", found[0])
		return found[0], nil
	}
	if len(found) == 0 {
		_, _ = fmt.Fprintf(out, "no repository %q found in %s. Region to use (e.g. europe-west2): ", repo, project)
	} else {
		_, _ = fmt.Fprintf(out, "%q is in %s. Which one? ", repo, strings.Join(found, ", "))
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return "", errors.New("no region given: pass --dataform-location")
	}
	return line, nil
}

// runConfig prints the settings in effect and where each came from.
func runConfig(args []string) error {
	fs := flag.NewFlagSet("xf config", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.Keys...)
	auth := fs.Bool("auth", false, "also show the Google account xf signs in with and the gcloud CLI's (asks Google, runs gcloud)")
	asJSON := fs.Bool("json", false, "print it as JSON")
	commandUsage(fs, "xf config [flags]", `Shows the settings xf would use in this directory (with these flags) and
where each came from, and which config files it read. See xf help for the
files and keys.

With --auth it also shows which Google account xf's own calls sign in
with (Application Default Credentials) and the gcloud CLI's account, which
can be another one. A login that is missing or expired is reported there;
xf config still exits 0.`)
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf config --help)", fs.Args())
	}
	root, name, err := pf.find()
	if err != nil {
		root = "" // the user file still applies
	}
	settings, err := sf.load(root)
	if err != nil {
		return err
	}
	var cloud *googleCloud
	if *auth {
		cloud = lookUpGoogleCloud(context.Background(), gcpauth.Lookup{})
	}
	if *asJSON {
		return printConfigJSON(os.Stdout, settings, root, cloud)
	}
	printConfig(os.Stdout, settings, root)
	if cloud != nil {
		printGoogleCloud(os.Stdout, *cloud, root != "" && name == backend.DBT)
	}
	return nil
}

// authTimeout bounds xf config --auth's lookups.
const authTimeout = 10 * time.Second

// googleCloud is what xf config --auth adds: the Google account xf's own
// calls sign in with, and the gcloud CLI's.
type googleCloud struct {
	ADC    gcpauth.ADC `json:"adc"`
	Gcloud gcpauth.CLI `json:"gcloud"`
}

// lookUpGoogleCloud asks for both accounts at once.
func lookUpGoogleCloud(ctx context.Context, l gcpauth.Lookup) *googleCloud {
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	var c googleCloud
	var wg sync.WaitGroup
	wg.Go(func() { c.ADC = l.ADC(ctx) })
	wg.Go(func() { c.Gcloud = l.Gcloud(ctx) })
	wg.Wait()
	return &c
}

func printGoogleCloud(w io.Writer, c googleCloud, dbt bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "\ngoogle cloud")
	for _, r := range append(c.ADC.Rows(dbt), c.Gcloud.Rows(c.ADC)...) {
		// As in the TUI's project info, a note follows its value closely.
		line := "  " + r[0] + "\t" + r[1]
		if r[2] != "" {
			line += "\t" + r[2]
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
}

// configJSON is xf config --json: what xf config prints, under names that
// scripts and editors can rely on.
type configJSON struct {
	Project     string           `json:"project"` // "" outside a project
	ConfigFiles []configFileJSON `json:"configFiles"`
	Settings    []settingJSON    `json:"settings"`
	Warnings    []string         `json:"warnings"`
	GoogleCloud *googleCloud     `json:"googleCloud,omitempty"` // with --auth
}

type configFileJSON struct {
	Path  string `json:"path"`
	Found bool   `json:"found"`
}

type settingJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	From  string `json:"from"`
}

func printConfigJSON(w io.Writer, l config.Loaded, root string, cloud *googleCloud) error {
	out := configJSON{Project: root, ConfigFiles: []configFileJSON{}, Settings: []settingJSON{}, Warnings: []string{}, GoogleCloud: cloud}
	for _, f := range l.Files {
		out.ConfigFiles = append(out.ConfigFiles, configFileJSON{Path: f.Abs, Found: f.Found})
	}
	for _, r := range settingRows(l, root, config.Keys) {
		out.Settings = append(out.Settings, settingJSON{Key: r.key, Value: r.value, From: r.from})
	}
	for _, warning := range l.Warnings {
		out.Warnings = append(out.Warnings, strings.TrimRight(warning, "\n"))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func printConfig(w io.Writer, l config.Loaded, root string) {
	if root != "" {
		_, _ = fmt.Fprintf(w, "project  %s\n\n", root)
	} else {
		_, _ = fmt.Fprintf(w, "project  (none: not a Dataform or dbt project's root)\n\n")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "config files, lowest precedence first")
	for _, f := range l.Files {
		state := "not found"
		if f.Found {
			state = "read"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", f.Path, state)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "setting\tvalue\tfrom")
	for _, r := range settingRows(l, root, config.Keys) {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", r.key, r.value, r.from)
	}
	_ = tw.Flush()
	for _, warning := range l.Warnings {
		_, _ = fmt.Fprintf(w, "\nwarning: %s", warning)
	}
	if len(l.Warnings) > 0 {
		_, _ = fmt.Fprintln(w)
	}
}

// settingRow is a setting as xf config and the TUI's project info show it:
// its value, and where the value came from.
type settingRow struct {
	key, value, from string
}

// settingRows are the rows of keys for the project at root: what a file or
// a flag set, and for the rest the default.
func settingRows(l config.Loaded, root string, keys []string) []settingRow {
	rows := make([]settingRow, 0, len(keys))
	for _, k := range keys {
		value, from := l.Get(k), l.Sources[k]
		if from == "" {
			value, from = defaultSetting(k, l, root)
		}
		if k == config.KeyDataformLocation && value != "" && !strings.ContainsAny(value, "-(") {
			from += "; a multi-region, but API runs need a region (xf init)"
		}
		rows = append(rows, settingRow{key: k, value: value, from: from})
	}
	return rows
}

// defaultSetting is the value and source of a key no file or flag set.
func defaultSetting(key string, l config.Loaded, root string) (value, from string) {
	switch key {
	case config.KeyDataformBin:
		bin, err := resolveBin(l)
		switch {
		case err != nil:
			return "(not found)", err.Error()
		case os.Getenv(dataform.BinEnv) != "":
			return bin, "$" + dataform.BinEnv
		}
		return bin, "PATH"
	case config.KeyDbtBin:
		bin, err := resolveDbtBin(l, root)
		switch {
		case err != nil:
			return "(not found)", err.Error()
		case os.Getenv(dbt.BinEnv) != "":
			return bin, "$" + dbt.BinEnv
		case bin == dbt.VenvBinary(root):
			return bin, "the project's virtualenv"
		}
		return bin, "PATH"
	case config.KeyDbtTarget:
		if t := os.Getenv("DBT_TARGET"); t != "" {
			return t, "$DBT_TARGET"
		}
		return "(dbt's default)", "default: the target profiles.yml names"
	case config.KeyDbtCompileWithHooks:
		return "false", "default: dbt v2 projects with hooks are parsed, not compiled"
	case config.KeyRunMode:
		return "cli", "default"
	case config.KeyDiffStyle:
		return config.DiffInline, "default"
	case config.KeySqlfluffBin:
		if bin, err := format.FindBin("", root, exec.LookPath); err == nil {
			return bin, "the project's virtualenv or PATH"
		}
		return "(not found)", "uv tool install sqlfluff"
	case config.KeySqlfluffConfig:
		return "(the project's own, else xf's)", "default"
	case config.KeyPythonLanguageServer:
		return config.PythonPyrefly, "default"
	case config.KeyAutoHidePane2:
		return "true", "default: pane 2 hides beside a file with no action"
	case config.KeyDeferToProd:
		return "false", "default: upstream tables are read from dev"
	case config.KeyPane2Tab:
		return config.Pane2SQL, "default"
	case config.KeyWrapLines:
		return "false", "default: long lines scroll sideways"
	case config.KeyWebOpenProjectIn:
		return config.OpenSameTab, "default"
	case config.KeyWebBrowser:
		return config.BrowserDefault, "default: the system's browser"
	case config.KeyDefaultBranch:
		return config.DefaultBranch, "default"
	case config.KeyTheme:
		return config.ThemeAuto, "default: follows the terminal"
	case config.KeyWebTheme:
		return config.ThemeAuto, "default: follows the browser"
	case config.KeyWebThemeLight:
		return webtheme.DefaultLight, "default"
	case config.KeyWebThemeDark:
		return webtheme.DefaultDark, "default"
	case config.KeyWebAutoExitSeconds:
		return strconv.Itoa(config.DefaultWebAutoExitSeconds), "default (off with --allow-host)"
	case config.KeyForgePollSeconds:
		return strconv.Itoa(config.DefaultForgePollSeconds), "default"
	case config.KeyEditor:
		return defaultEditor(os.Getenv)
	case config.KeyVimFormat:
		return "(none)", "default: no key; :format and the palette format"
	case config.KeyVimNextHunk, config.KeyVimPrevHunk, config.KeyVimUndoHunk, config.KeyVimStageHunk, config.KeyVimNextError, config.KeyVimPrevError, config.KeyVimHover, config.KeyVimDefinition, config.KeyVimMenu,
		config.KeyVimLeader, config.KeyVimBufferPrev, config.KeyVimBufferNext, config.KeyVimBuffers, config.KeyVimBufferDelete, config.KeyVimFiles:
		return config.VimKeyDefaults[key], "default"
	case config.KeyTablePrefix, config.KeySchemaSuffix, config.KeyDatabaseSuffix:
		name := dataform.SettingsKey(key)
		if v := settingsValue(root, name); v != "" {
			return v, dataform.SettingsFile + " (" + name + ")"
		}
		return "(none)", "default: " + dataform.SettingsFile + " has no " + name
	case config.KeyDevCompilerOptions:
		return "(none)", "default: dev runs aren't folded into their actions"
	case config.KeyProdCompilerOptions:
		if p := settingsValue(root, "defaultProject"); p != "" {
			return "(none)", "default: column impact compares with the default targets, in " + p
		}
		return "(none)", "default: column impact compares with the default targets"
	case config.KeyDataformLocation:
		if loc := settingsValue(root, "defaultLocation"); loc != "" {
			return loc, dataform.SettingsFile + " (defaultLocation)"
		}
		return "(unset)", dataform.SettingsFile + " has no defaultLocation"
	case config.KeyDataformRepo:
		if root != "" {
			if g, err := dataformapi.ReadGit(context.Background(), root); err == nil {
				if name := dataformapi.RepoNameFromURL(g.Origin); name != "" {
					return name, "origin remote"
				}
			}
		}
		return "(unset)", "no origin remote"
	}
	return "", ""
}

// settingsValue reads a top-level key of workflow_settings.yaml, for
// display only: it is a flat file, so a line match does.
func settingsValue(root, key string) string {
	if root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, dataform.SettingsFile))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*["']?([^"'\s#]+)`)
	if m := re.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}
