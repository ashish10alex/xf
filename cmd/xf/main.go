// Command xf is a lazygit-style terminal UI for Dataform projects.
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/compilecache"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/format"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/lsp"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/ui"
	"github.com/ashish10alex/xf/nvim"
)

func main() {
	args := os.Args[1:]
	run := func() error { return runTUI(args) }
	if helpRequested(args) {
		run = func() error { mainHelp(os.Stdout, tuiFlags()); return nil }
	} else if versionRequested(args) {
		run = func() error { fmt.Println(versionText()); return nil }
	} else if len(args) > 0 {
		switch args[0] {
		case "lsp":
			run = func() error { return runLSP(args[1:]) }
		case "serve":
			run = func() error { return runServe(args[1:]) }
		case "stop":
			run = func() error { return runStop(args[1:]) }
		case "init":
			run = func() error { return runInit(args[1:]) }
		case "config":
			run = func() error { return runConfig(args[1:]) }
		case "web":
			run = func() error { return runWeb(args[1:]) }
		case "scratch":
			run = func() error { return runScratch(args[1:]) }
		case "search":
			run = func() error { return runSearch(args[1:]) }
		case "impact":
			os.Exit(runImpact(args[1:]))
		case "cost":
			os.Exit(runCost(args[1:]))
		}
	}
	if err := run(); err != nil {
		if err != errQuiet {
			fmt.Fprintln(os.Stderr, "xf:", err)
		}
		os.Exit(1)
	}
}

// piped says f is a pipe or a file, as stdin is in "xf cost --json | xf", a
// typo for a JSON viewer: the TUI would take the terminal from the command
// before it. Windows is left out, as some of its terminals (mintty) give
// programs pipes.
func piped(f *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	st, err := f.Stat()
	return err == nil && (st.Mode()&os.ModeNamedPipe != 0 || st.Mode().IsRegular())
}

// errQuiet ends xf with status 1 and nothing more to say.
var errQuiet = errors.New("quiet")

// tuiFlagSet is xf's own flag set, with the project flags and the flags
// of every setting.
func tuiFlagSet() (*flag.FlagSet, projectFlags, *settingFlags, *bool) {
	fs := flag.NewFlagSet("xf", flag.ContinueOnError)
	inNvim := fs.Bool("nvim", false, "open neovim with the files and xf in a split beside it")
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.Keys...)
	fs.Usage = func() { mainHelp(fs.Output(), fs) }
	return fs, pf, sf, inNvim
}

// tuiFlags is xf's flag set, for the help.
func tuiFlags() *flag.FlagSet {
	fs, _, _, _ := tuiFlagSet()
	return fs
}

func runTUI(args []string) error {
	fs, pf, sf, inNvim := tuiFlagSet()
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 && !*inNvim {
		return fmt.Errorf("unexpected arguments: %v (see xf --help)", fs.Args())
	}
	if piped(os.Stdin) {
		return errors.New("the TUI opens on a terminal and reads nothing from a pipe; to view JSON from one, use jq or fx")
	}

	root, name, err := pf.find()
	if errors.Is(err, backend.ErrNoProject) && *pf.dir == "" && *pf.backend == "" {
		// Outside any project, offer the ones xf has opened before.
		if projects := rememberedProjects(); len(projects) > 0 && interactive() {
			p, ok, inEditor, err := pickProject(projects, sf)
			if err != nil {
				return err
			}
			if !ok {
				return errQuiet
			}
			return openPicked(fs, sf, *inNvim, inEditor, p)
		}
	}
	if err != nil {
		return err
	}
	return openProject(fs, sf, *inNvim, root, name)
}

// openProject opens the TUI for the project at root, or neovim with the
// TUI beside it, with the flags parsed into fs and sf.
func openProject(fs *flag.FlagSet, sf *settingFlags, inNvim bool, root, name string) error {
	settings, err := sf.load(root)
	if err != nil {
		return err
	}
	// besideNvim replaces xf with neovim and an xf for this project in a
	// split beside it. The split's xf gets the same flags as this one; the
	// language server only those it has.
	besideNvim := func(files []string, line int) error {
		var lspFlags []string
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "config" || slices.ContainsFunc(engineKeys, func(k string) bool { return config.Flags[k] == f.Name }) {
				lspFlags = append(lspFlags, "--"+f.Name, f.Value.String())
			}
		})
		return launchNvim(append(projectArgs(root, name), sf.given("nvim")...), lspFlags, files, line)
	}
	if inNvim {
		return besideNvim(fs.Args(), 0)
	}
	b, err := openBackend(name, root, settings)
	if err != nil {
		return err
	}
	client, err := connect(context.Background(), root, b, nil)
	if err != nil {
		return err
	}
	defer client.Close()

	deps := projectDeps(root, name, b, settings, client)
	// Nobody types within half a second of starting xf, while the
	// terminal answers its queries.
	deps.StartupQuiet = 500 * time.Millisecond
	var openInNvim func(file string, line int) error
	// An editor that runs xf beside itself, as VS Code's "xf: Open" does,
	// says so with $XF_BESIDE; neovim's split has $NVIM.
	deps.Beside = os.Getenv(besideEnv) != ""
	if server := os.Getenv(nvim.ServerEnv); server != "" {
		deps.Beside = true
		openInNvim = func(file string, line int) error { return callNvim(nvim.EditArgs(server, file, line)) }
		deps.FullScreen = func() error { return callNvim(nvim.FullScreenArgs(server)) }
	}
	deps.Open = editorOpener(client.ShowDocument, openInNvim, editorOf(settings), root)
	sessionFile := ""
	if dir := sessionDir(); dir != "" {
		sessionFile = session.Path(dir, root)
		// The visit is recorded now as well as on quitting, so the picker
		// lists a project whose xf was killed, or closed with its neovim.
		_ = session.Visit(sessionFile, root, name, time.Now())
		deps.Resume = session.Load(sessionFile)
	}
	final, err := tea.NewProgram(ui.New(deps), colorOptions()...).Run()
	if m, ok := final.(ui.Model); ok {
		if notice := m.ExitNotice(); notice != "" {
			fmt.Fprintln(os.Stderr, "xf:", notice)
		}
		if sessionFile != "" {
			s := m.Session()
			s.Backend, s.LastVisited = name, time.Now()
			if err := session.Save(sessionFile, s); err != nil {
				fmt.Fprintln(os.Stderr, "xf: could not save the session:", err)
			}
		}
		// o with neovim as the editor: neovim takes xf's place, in the
		// project so that its working directory is the project's.
		if file, line, ok := m.Leaving(); ok && err == nil {
			client.Close()
			if err := os.Chdir(root); err != nil {
				return err
			}
			return besideNvim([]string{file}, line+1)
		}
	}
	return err
}

// projectDeps are what the TUI, or xf web, needs of the project at root,
// compiled by b's engine through client: all but the editor's.
func projectDeps(root, name string, b backend.Backend, settings config.Loaded, client *remote.Client) frontend.Deps {
	deps := frontend.Deps{
		Backend:             name,
		Root:                root,
		Engine:              client,
		Editor:              client.Editor(),
		Copy:                clipboard.WriteAll,
		OpenURL:             browserOpener(),
		Notice:              strings.Join(settings.Warnings, "; "),
		Theme:               settings.Theme,
		SaveTheme:           saveTheme,
		Info:                projectInfo(root, name, b, settings),
		CommandUses:         commandUses(),
		DryRun:              client.DryRun,
		ForgetFailedDryRuns: client.ForgetFailedDryRuns,
		Table:               client.Table,
		Git:                 func(ctx context.Context) (dataformapi.GitState, error) { return dataformapi.ReadGit(ctx, root) },
		Branches:            gitbranch.Repo{Dir: root},
	}
	if d, ok := b.(*dbt.Backend); ok {
		deps.Target = d.Target
	}
	if r, ok := b.(backend.Runner); ok {
		deps.StartRun = func(ctx context.Context, opts project.RunOptions) (*project.Run, error) {
			return r.StartRun(ctx, root, opts)
		}
		deps.RunCommand = func(o project.RunOptions) string { return r.RunCommand(root, o) }
	}
	// API runs are Dataform's; without them the TUI hides their keys and
	// panes. Column impact in dbt projects is xf web's (see openWebProject).
	switch b := b.(type) {
	case dataform.Backend:
		addDataformDeps(&deps, root, b.Bin, settings, client)
		addRunJobs(&deps, root, b, client)
	case *dbt.Backend:
		addChangesDeps(&deps, root, settings, client)
		addDeferDeps(&deps, client)
		deps.DeferWriter = nil // dbt defers itself: no proxy views
		deps.DbtProdTarget = settings.DbtProdTarget
		deps.Unset = unsetDbtProdTarget(settings, os.Getenv)
	}
	return deps
}

// openPicked opens the project picked in the picker: in the TUI, or with
// inEditor (o with neovim as the editor) in neovim with the TUI beside it,
// started in the project.
func openPicked(fs *flag.FlagSet, sf *settingFlags, inNvim, inEditor bool, p session.Project) error {
	if inEditor {
		if err := os.Chdir(p.Root); err != nil {
			return err
		}
	}
	return openProject(fs, sf, inNvim || inEditor, p.Root, p.Backend)
}

// editorOf is the command o opens files and projects with: the editor
// setting, else the default (see defaultEditor).
func editorOf(settings config.Loaded) string {
	return cmp.Or(settings.Editor, first(defaultEditor(os.Getenv)))
}

// besideEnv, when set, says xf runs beside an editor, which it follows.
const besideEnv = "XF_BESIDE"

// addDataformDeps gives the TUI what only Dataform projects have: runs
// through the Dataform API and column impact, and Run Changed.
func addDataformDeps(deps *frontend.Deps, root, bin string, settings config.Loaded, client *remote.Client) {
	deps.StartAPIRun = func(ctx context.Context, opts dataformapi.Options) (*project.Run, error) {
		if settings.DataformLocation != "" {
			opts.Repo.Location = settings.DataformLocation
		}
		opts.Repo.Name = settings.DataformRepo
		// So the API builds the tables the local compile names.
		opts.Compile = dataformapi.CompilationConfig{TablePrefix: settings.TablePrefix, SchemaSuffix: settings.SchemaSuffix, DatabaseSuffix: settings.DatabaseSuffix}
		g, err := dataformapi.ReadGit(ctx, root)
		if err != nil {
			return nil, err
		}
		if b := g.Blocker(); b != "" {
			return nil, errors.New(b) // before the API is called, or the run kept
		}
		opts.Git = g
		api, err := dataformapi.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		// The run's table says what its BigQuery jobs cost. Without a
		// BigQuery client it has their states and timing only.
		if jobs, err := bigquery.NewClient(ctx); err == nil {
			opts.Jobs = jobs
		}
		return dataformapi.Start(ctx, api, opts), nil
	}
	deps.WatchWorkflow = func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
		// A bare ID is in this project's repository; a URL names its own,
		// whatever it is.
		name := settings.DataformRepo
		if name == "" && ref.Repo == "" {
			if g, err := dataformapi.ReadGit(ctx, root); err == nil {
				name = dataformapi.RepoNameFromURL(g.Origin)
			}
		}
		// base.Location is where the project's BigQuery jobs run (its
		// defaultLocation), as for an API run; the Dataform repository's own
		// location, a region, is often not it (EU, say).
		jobLocation := base.Location
		base.Location = cmp.Or(settings.DataformLocation, base.Location)
		base.Name = name
		api, err := dataformapi.NewClient(ctx)
		if err != nil {
			return nil, nil, err
		}
		opts := dataformapi.WatchOptions{Repo: ref.Resolve(base), ID: ref.ID, JobLocation: jobLocation}
		if jobs, err := bigquery.NewClient(ctx); err == nil {
			opts.Jobs = jobs
		}
		return dataformapi.Watch(ctx, api, opts)
	}
	deps.RunAPI = settings.RunMode == "api"
	deps.APIGit = func(ctx context.Context) (dataformapi.GitState, error) { return dataformapi.ReadGit(ctx, root) }
	deps.ColumnImpact = client.ColumnImpact
	addDeferDeps(deps, client)
	deps.DevCompilerOptions = settings.DevCompilerOptions
	deps.ProdCompilerOptions = settings.ProdCompilerOptions
	deps.Unset = unsetProdOptions(settings)
	// Asked for only where the answer would be read back: not with
	// --config, which reads another file than the one it is saved to.
	if readsProjectFile(settings, root) {
		deps.SaveProdOptions = func(ctx context.Context, options string) error {
			return saveProdOptions(ctx, bin, root, options)
		}
	}
	addChangesDeps(deps, root, settings, client)
}

// readsProjectFile reports whether settings were read from root's
// .xf/config.json, or would have been had it existed.
func readsProjectFile(settings config.Loaded, root string) bool {
	want := filepath.Join(root, config.ProjectFile)
	return slices.ContainsFunc(settings.Files, func(f config.File) bool { return f.Abs == want })
}

// unsetProdOptions is "prodCompilerOptions" when no file or flag sets it,
// not even to "", which says the default targets are prod: I and C then
// say they used the default targets.
func unsetProdOptions(settings config.Loaded) string {
	if settings.IsSet(config.KeyProdCompilerOptions) {
		return ""
	}
	return config.KeyProdCompilerOptions
}

// unsetDbtProdTarget is "dbtProdTarget" when nothing picks the target I
// compares with and C dry runs against: no dbtProdTarget, and no dbtTarget
// or $DBT_TARGET, so they use the profile's default target.
func unsetDbtProdTarget(settings config.Loaded, getenv func(string) string) string {
	if settings.IsSet(config.KeyDbtProdTarget) || settings.IsSet(config.KeyDbtTarget) || getenv("DBT_TARGET") != "" {
		return ""
	}
	return config.KeyDbtProdTarget
}

// saveProdOptions sets prodCompilerOptions in the project file of root,
// once the project compiles with them. "" says the default targets are
// prod, and is saved as it is.
func saveProdOptions(ctx context.Context, bin, root, options string) error {
	if options != "" {
		args, err := dataform.SplitOptions(options)
		if err != nil {
			return err
		}
		g, err := dataform.CompileWith(ctx, bin, root, args)
		if err != nil {
			return err
		}
		if g.HasErrors() {
			return errors.New(g.Errors[0].Message)
		}
	}
	_, err := config.SetProjectKey(root, config.KeyProdCompilerOptions, options)
	return err
}

// addChangesDeps gives the TUI Run Changed and the query cost of the
// changes, and the base they compare with, which b changes and saves.
func addChangesDeps(deps *frontend.Deps, root string, settings config.Loaded, client *remote.Client) {
	deps.ChangedActions = client.ChangedActions
	deps.QueryCost = client.QueryCost
	deps.DefaultBranch = cmp.Or(settings.DefaultBranch, config.DefaultBranch)
	// Saved only where it would be read back: not with --config.
	if readsProjectFile(settings, root) {
		deps.SaveBranch = func(branch string) error {
			_, err := config.SetProjectBranch(root, branch)
			return err
		}
	}
}

// launchNvim replaces xf with neovim, opening files beside a split that
// runs this xf with tuiFlags, and its language server with lspFlags. A
// positive line is the 1-based line of the first file to start on.
func launchNvim(tuiFlags, lspFlags, files []string, line int) error {
	if runtime.GOOS == "windows" {
		return errors.New("xf --nvim isn't supported on Windows: use xf lsp from neovim's own config instead (see the README)")
	}
	if os.Getenv(nvim.ServerEnv) != "" {
		return nvim.ErrNested
	}
	prog, err := exec.LookPath("nvim")
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	rt, err := nvim.Install(filepath.Join(cache, "xf"))
	if err != nil {
		return fmt.Errorf("installing the neovim plugin: %w", err)
	}
	args, err := nvim.Args(nvim.LaunchOptions{
		Runtime: rt,
		Xf:      append([]string{exe}, tuiFlags...),
		LSP:     append([]string{exe, "lsp"}, lspFlags...),
		Files:   files,
		Line:    line,
	})
	if err != nil {
		return err
	}
	return syscall.Exec(prog, append([]string{"nvim"}, args...), os.Environ())
}

// browserOpener opens URLs with the system's opener (open on macOS, the
// URL handler on Windows, xdg-open elsewhere), or is nil when there is
// none.
func browserOpener() func(url string) error {
	name, args := "xdg-open", []string(nil)
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	}
	prog, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	return func(url string) error {
		out, err := exec.Command(prog, append(args, url)...).CombinedOutput()
		if err != nil && len(bytes.TrimSpace(out)) > 0 {
			return errors.New(string(bytes.TrimSpace(out)))
		}
		return err
	}
}

// callNvim runs nvim with args, which ask the neovim xf runs inside to do
// something, and returns what went wrong.
func callNvim(args []string) error {
	out, err := exec.Command("nvim", args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// connect attaches to the project's xf serve, starting one that compiles
// with b when none is running.
func connect(ctx context.Context, root string, b backend.Backend, logger *log.Logger) (*remote.Client, error) {
	path := remote.SocketPath(root, backend.EngineName(b))
	return remote.Connect(ctx, remote.ClientConfig{Dial: remote.Dialer(path, startServe(root, b)), Parse: b.Parse, Build: thisBuild, Log: logger})
}

// serveArgs are the arguments that start xf serve for root with b.
func serveArgs(root string, b backend.Backend) []string {
	args := append([]string{"serve"}, projectArgs(root, b.Name())...)
	switch b := b.(type) {
	case dataform.Backend:
		if b.Bin == "" {
			// Looked up at each use, so serve's engine name stays this
			// one's once the CLI is installed.
			args = append(args, "--dataform-on-path")
		} else {
			args = append(args, "--dataform-bin", b.Bin)
		}
		// Always given, even when empty, so serve can't read other names
		// from a config file and listen where we don't dial.
		args = append(args, "--table-prefix", b.Names.TablePrefix, "--schema-suffix", b.Names.SchemaSuffix, "--database-suffix", b.Names.DatabaseSuffix)
	case *dbt.Backend:
		// Always given, even when empty, so serve can't read another
		// target from a config file and listen where we don't dial.
		args = append(args, "--dbt-bin", b.Bin, "--dbt-target", b.Target, "--dbt-compile-with-hooks", strconv.FormatBool(b.CompileWithHooks))
	}
	return args
}

// startServe starts xf serve for root in the background, detached so it
// outlives this process (see startDetached), logging to remote.LogPath.
func startServe(root string, b backend.Backend) func() error {
	return func() error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, serveArgs(root, b)...)
		cmd.Dir = root
		logf, err := os.OpenFile(remote.LogPath(root, backend.EngineName(b)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		cmd, err = startDetached(cmd)
		if err != nil {
			if logf != nil {
				_ = logf.Close()
			}
			return err
		}
		go func() {
			_ = cmd.Wait() // reap it if it stops while we run
			if logf != nil {
				_ = logf.Close()
			}
		}()
		return nil
	}
}

// serveFlagSet is xf serve's flag set: the project flags and the settings
// serveArgs passes.
func serveFlagSet() (*flag.FlagSet, projectFlags, *settingFlags, *bool) {
	fs := flag.NewFlagSet("xf serve", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, engineKeys...)
	onPath := fs.Bool("dataform-on-path", false, "look dataform up on PATH each time it is needed, as xf web does for a Dataform project opened before the CLI is installed")
	commandUsage(fs, "xf serve [flags]", "Runs the engine (compiles, watches files, dry-runs) that xf and xf lsp\nshare for a project. They start it when they need it; it stops a few seconds\nafter the last of them leaves, and logs beside its socket.")
	return fs, pf, sf, onPath
}

// serveBackend is the backend xf serve compiles with: with onPath, for a
// Dataform project, one that looks dataform up at each use.
func serveBackend(name, root string, settings config.Loaded, onPath bool) (backend.Backend, error) {
	if onPath && name == backend.Dataform {
		if err := project.CheckRoot(root); err != nil {
			return nil, err
		}
		return dataform.Backend{Names: dataformNames(settings)}, nil
	}
	return openBackend(name, root, settings)
}

// runServe runs the shared engine for a project until it has had no
// clients for a few seconds.
func runServe(args []string) error {
	fs, pf, sf, onPath := serveFlagSet()
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf serve --help)", fs.Args())
	}
	logger := log.New(os.Stderr, "xf serve: ", log.LstdFlags)
	if serveFromCopy(append([]string{"serve"}, args...), logger) {
		return nil
	}
	root, name, err := pf.find()
	if err != nil {
		return err
	}
	settings, err := sf.load(root)
	if err != nil {
		return err
	}
	b, err := serveBackend(name, root, settings, *onPath)
	if err != nil {
		return err
	}
	prepareServe()
	for _, w := range settings.Warnings {
		logger.Print(w)
	}
	socket := remote.SocketPath(root, backend.EngineName(b))
	ln, err := remote.Listen(socket)
	if errors.Is(err, remote.ErrRunning) {
		logger.Print(err)
		return nil
	}
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := engine.Config{
		Compile: func(ctx context.Context) (*project.Graph, error) { return b.Compile(ctx, root) },
		Watch: func(ctx context.Context) (<-chan struct{}, error) {
			return b.Watch(ctx, root, 300*time.Millisecond)
		},
	}
	bq := lazyClient()
	cfg.DryRun = dryRunner(bq)
	cfg.Credentials = gcpauth.Generation
	var eng *engine.Engine
	// Impact and cost checks dry-run through the engine, once it is made,
	// to share its cache.
	engineDryRun := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		if eng == nil {
			return nil, errNoEngine
		}
		return eng.DryRun(ctx, project, location, sql)
	}
	var startDefer func()
	switch b := b.(type) {
	case dataform.Backend:
		dd := newDataformDeferrer(b.Bin, root, bq, func() (config.Loaded, error) { return sf.load(root) }, logger)
		addDataformEngine(&cfg, b, root, bq, logger, engineDryRun, dd.lookups)
		addDefer(&cfg, dd, root, settings)
	case *dbt.Backend:
		take := func(now time.Time) (compilecache.Fingerprint, error) { return compilecache.TakeDbt(root, b.Bin, now) }
		cfg.Compile, cfg.Saved = savedCompiles(root, b, backend.EngineName(b), take, logger)
		addStages(&cfg, b, root, logger)
		cfg.Changes = dbtChangesSource(root, b, logger)
		targets := newDbtTargets(b, root, logger)
		cfg.Impact = dbtImpact(bq, engineDryRun, targets)
		cfg.Cost = dbtCost(bq, engineDryRun, targets)
		dd := newDbtDeferrer(b, root, func() (config.Loaded, error) { return sf.load(root) }, logger)
		dd.recompile = func() {
			if eng != nil {
				eng.Recompile("defer to prod")
			}
		}
		addDefer(&cfg, dd, root, settings)
		cfg.OnDefer = dd.switched
		if cfg.DeferOn {
			startDefer = func() { dd.switched(true) }
		}
	}
	eng = engine.New(cfg)
	if startDefer != nil {
		startDefer() // once there is an engine to compile again
	}
	if err := eng.Start(ctx); err != nil {
		logger.Printf("not watching files: %v", err)
	}
	logger.Printf("serving %s %s project on %s", root, name, socket)
	return remote.Serve(ctx, ln, remote.ServerConfig{Root: root, Engine: eng, Table: lazyTables(bq), Build: thisBuild, Log: logger})
}

// addDataformEngine gives the engine what only Dataform projects have:
// saved compiles and column impact, and Dataform's Changed Actions and
// query cost. Impact and cost look tables up with bq and dry-run with
// dryRun; cost finds the tables built in dev with defer to prod's lookups.
func addDataformEngine(cfg *engine.Config, b dataform.Backend, root string, bq func(context.Context) (*bigquery.Client, error), logger *log.Logger, dryRun dryRunFunc, lookups *deferral.Lookups) {
	take := func(now time.Time) (compilecache.Fingerprint, error) { return compilecache.Take(root, b.Bin, now) }
	compile, saved := savedCompiles(root, b, namesKind(b.Names), take, logger)
	source := changesSource(root, b, b.Bin+namesKind(b.Names), logger)
	cfg.Compile = compile
	cfg.Saved = saved
	cfg.Changes = source
	cfg.Impact = impactChecker(bq, dryRun, b.Bin, root, source)
	cfg.Cost = dataformCost(bq, dryRun, b, root, source, lookups, logger)
}

// namesKind keeps apart the saved compiles and base snapshots of a
// Dataform project compiled to other names; "" for none, as before there
// were any. It names a directory, so it is a hash.
func namesKind(n dataform.Names) string {
	if n.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(n.Args(), "\x00")))
	return "names-" + hex.EncodeToString(sum[:])[:8]
}

// addStages has the engine show the project before its compile is done,
// with a backend that can (see engine.Config.Stage).
func addStages(cfg *engine.Config, b backend.Stager, root string, logger *log.Logger) {
	cfg.Stage = func(ctx context.Context, prev *project.Graph) (*project.Graph, bool, error) {
		return b.Stage(ctx, root, prev)
	}
	cfg.Focus = func(ctx context.Context, base *project.Graph, id project.ActionID) (*project.Graph, error) {
		start := time.Now()
		g, err := b.Focus(ctx, root, base, id)
		if err == nil && g != nil {
			logger.Printf("compiled %s ahead of the project in %s", id, time.Since(start).Round(time.Millisecond))
		}
		return g, err
	}
	cfg.Suggest = func(ctx context.Context, g *project.Graph) project.ActionID { return b.Suggest(ctx, root, g) }
	cfg.FocusDebounce = 300 * time.Millisecond
	cfg.Logf = logger.Printf
}

// savedCompiles compiles the project with b, saving every clean compile
// under the user cache directory, and loads the one an earlier server
// saved, so the TUI shows it without waiting for the CLI (see package
// compilecache). take fingerprints the project. Compiles are kept apart by
// kind, what else picks a compile's output (dbt's target); "" for none.
func savedCompiles(root string, b backend.Backend, kind string, take func(now time.Time) (compilecache.Fingerprint, error), logger *log.Logger) (compile func(context.Context) (*project.Graph, error), saved func() *engine.Saved) {
	compile = func(ctx context.Context) (*project.Graph, error) {
		return b.Compile(ctx, root)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		logger.Printf("compiles are not saved: %v", err)
		return compile, nil
	}
	store := &compilecache.Store{Dir: filepath.Join(dir, "xf", "compiled", kind)}
	compile = func(ctx context.Context) (*project.Graph, error) {
		fp, fpErr := take(time.Now())
		start := time.Now()
		g, err := b.Compile(ctx, root)
		if err != nil || g.HasErrors() || fpErr != nil {
			return g, err
		}
		e := compilecache.Entry{Fingerprint: fp, CompiledAt: time.Now(), Took: time.Since(start), JSON: g.JSON}
		go func() {
			if err := store.Save(root, e); err != nil {
				logger.Printf("saving the compile: %v", err)
			}
		}()
		return g, nil
	}
	saved = func() *engine.Saved {
		start := time.Now()
		e, err := store.Load(root)
		if err != nil {
			logger.Printf("loading the saved compile: %v", err)
		}
		if e == nil {
			return nil
		}
		g, err := b.Parse(e.JSON)
		if err != nil {
			logger.Printf("parsing the saved compile: %v", err)
			return nil
		}
		fp, err := take(time.Now())
		outdated := compilecache.Outdated(e.Fingerprint, fp)
		if err != nil {
			outdated = err.Error()
		}
		if outdated == "" {
			logger.Printf("loaded the compile saved at %s in %s; it is current", e.CompiledAt.Format(time.TimeOnly), time.Since(start).Round(time.Millisecond))
		} else {
			logger.Printf("loaded the compile saved at %s in %s; recompiling, as %s", e.CompiledAt.Format(time.TimeOnly), time.Since(start).Round(time.Millisecond), outdated)
		}
		return &engine.Saved{Graph: g, CompiledAt: e.CompiledAt, Took: e.Took, Outdated: outdated}
	}
	return compile, saved
}

// changesSource works out the project's Changed Actions, compiling bases
// with b and caching them under the user cache directory. bin, the tool's
// binary, is part of the cache key.
func changesSource(root string, b backend.Backend, bin string, logger *log.Logger) *changes.Source {
	return &changes.Source{
		Root:     root,
		Key:      bin,
		CacheDir: changesCacheDir(logger),
		Compile: func(ctx context.Context, dir string) (*project.Graph, error) {
			logger.Printf("compiling a base snapshot in %s", dir)
			return b.Compile(ctx, dir)
		},
	}
}

// dbtChangesSource works out a dbt project's Changed Actions: bases are
// parsed manifests of the merge-base, cached under the user cache
// directory by the backend's variant, and dbt's state:modified compares
// the working tree with them.
func dbtChangesSource(root string, b *dbt.Backend, logger *log.Logger) *changes.Source {
	return &changes.Source{
		Root:     root,
		Key:      backend.EngineName(b),
		CacheDir: changesCacheDir(logger),
		Mirror:   dbt.Mirror,
		Base: func(ctx context.Context, dir string) ([]byte, error) {
			logger.Printf("parsing a base snapshot in %s", dir)
			return b.Base(ctx, root, dir)
		},
		Diff: func(ctx context.Context, base, _ []byte) (changes.Diff, error) {
			return b.Changed(ctx, root, base)
		},
	}
}

// changesCacheDir is where compiled bases are cached; "" keeps them in
// memory.
func changesCacheDir(logger *log.Logger) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		logger.Printf("compiled bases are not cached on disk: %v", err)
		return ""
	}
	return filepath.Join(dir, "xf", "changed")
}

// engineKeys are the settings that pick a project's xf serve (see
// serveArgs): xf serve takes them, and so does xf lsp, to reach the same
// one as the xf it runs beside.
var engineKeys = []string{config.KeyDataformBin, config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtCompileWithHooks, config.KeyTablePrefix, config.KeySchemaSuffix, config.KeyDatabaseSuffix}

// runLSP serves an editor over stdin and stdout. It attaches to the
// project's xf serve, Dataform's or dbt's, and when it can't, runs an
// engine of its own; without the tool's binary that engine can't compile,
// and the editor is told ref() jumps are off, because the other jumps don't
// need a compile.
func runLSP(args []string) error {
	fs := flag.NewFlagSet("xf lsp", flag.ContinueOnError)
	// Clients such as VS Code's pass --stdio, the only transport: taken
	// and ignored, so left out of the help.
	fs.Bool("stdio", true, "")
	sf := addSettingFlags(fs, engineKeys...)
	commandUsage(fs, "xf lsp [flags]", "A language server for editors, speaking LSP on stdin and stdout, in Dataform\nand dbt projects. It reads the same config files as xf (only the binaries\nand the dbt settings matter to it).")
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf lsp --help)", fs.Args())
	}

	logger := log.New(os.Stderr, "xf lsp: ", log.LstdFlags)
	// The editor's root is known only once it connects.
	settingsFor := func(root string) config.Loaded {
		l, err := sf.load(root)
		if err != nil {
			// An editor can't show why its server exited: log it and
			// carry on with the flags alone.
			logger.Printf("ignoring config: %v", err)
			l = config.Loaded{Sources: map[string]string{}}
			sf.apply(&l)
			return l
		}
		for _, w := range l.Warnings {
			logger.Print(w)
		}
		return l
	}
	// The editor's root is the project's; a directory that is both a
	// Dataform and a dbt project is served as neither.
	openRoot := func(root string) (backend.Backend, error) {
		name, err := backend.Detect(root, "")
		if err != nil {
			return nil, err
		}
		return openBackend(name, root, settingsFor(root))
	}
	bq := lazyClient()
	cfg := lsp.Config{
		Connect: func(ctx context.Context, root string) (lsp.Engine, error) {
			b, err := openRoot(root)
			if err != nil {
				return nil, err
			}
			return connect(ctx, root, b, logger)
		},
		Compile: func(ctx context.Context, root string) (*project.Graph, error) {
			b, err := openRoot(root)
			if err != nil {
				return nil, err
			}
			return b.Compile(ctx, root)
		},
		NoCompile: func(root string) error {
			_, err := openRoot(root)
			return err
		},
		Watch: func(ctx context.Context, root string) (<-chan struct{}, error) {
			b, err := openRoot(root)
			if err != nil {
				return nil, err
			}
			return b.Watch(ctx, root, 300*time.Millisecond)
		},
		Tables:      lazyTables(bq),
		DryRun:      dryRunner(bq),
		Credentials: gcpauth.Generation,
		Log:         os.Stderr,
		Dbt: func(root string) bool {
			name, err := backend.Detect(root, "")
			return err == nil && name == backend.DBT
		},
		Format: func(ctx context.Context, root, path string, text []byte) ([]byte, error) {
			l := settingsFor(root)
			bin, _ := format.FindBin(l.SqlfluffBin, root, nil) // none: the config block still formats
			return format.SQLX(ctx, text, format.Options{Bin: bin, Dir: filepath.Dir(path), Root: root, Config: l.SqlfluffConfig})
		},
	}
	return lsp.Serve(context.Background(), os.Stdin, os.Stdout, cfg)
}

// lazyClient makes a BigQuery client on first use. A failed attempt (no
// credentials yet, say) is retried on the next call, so logging in takes
// effect without restarting; once made, the client follows later logins.
func lazyClient() func(ctx context.Context) (*bigquery.Client, error) {
	var mu sync.Mutex
	var client *bigquery.Client
	return func(ctx context.Context) (*bigquery.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if client == nil {
			c, err := bigquery.NewClient(ctx)
			if err != nil {
				return nil, err
			}
			client = c
		}
		return client, nil
	}
}

// dryRunner dry-runs with the client client makes.
func dryRunner(client func(context.Context) (*bigquery.Client, error)) func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
	return func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		c, err := client(ctx)
		if err != nil {
			return nil, err
		}
		return c.DryRun(ctx, project, location, sql)
	}
}

// lazyTables looks up BigQuery table metadata with the client client makes.
func lazyTables(client func(context.Context) (*bigquery.Client, error)) func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error) {
	return func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error) {
		c, err := client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Table(ctx, project, dataset, table)
	}
}
