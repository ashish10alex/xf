package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/web"
	"github.com/ashish10alex/xf/nvim"
)

// runWeb is xf web: the TUI in a browser (see package web), for the
// project in the working directory or, outside any, the projects xf has
// opened before. It runs until ctrl+c, ctrl+c in the projects page, or
// every page has been closed for webAutoExitSeconds.
func runWeb(args []string) error { return serveWeb(false, args) }

// runScratch is xf scratch: xf web opened on a new empty scratch query, in
// the project in the working directory or, outside any, on its own.
func runScratch(args []string) error { return serveWeb(true, args) }

// serveWeb is xf web, or with scratch set xf scratch.
func serveWeb(scratch bool, args []string) error {
	cmdName := "xf web"
	if scratch {
		cmdName = "xf scratch"
	}
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.Keys...)
	port := fs.Int("port", 0, "the `port` to listen on, on 127.0.0.1 (default: any free port)")
	noOpen := fs.Bool("no-open", false, "print the URL without opening a browser")
	app := fs.Bool("app", false, "open the page in an app window of Chrome, Edge or Chromium, where ctrl+n moves as in the TUI instead of opening a browser window: --web-browser app for this run")
	allowHost := fs.String("allow-host", "", "also answer requests to these comma-separated `names`, such as a tailscale serve's; every request then needs the token the printed URL carries")
	var project, location *string
	if scratch {
		project = fs.String("project", "", "the BigQuery `project` a new scratch is billed to (default: the project's default, else your credentials' quota project)")
		location = fs.String("location", "", "the `location` a new scratch runs in (default: the project's default, else BigQuery picks it from the tables)")
		commandUsage(fs, "xf scratch [flags]",
			"Open xf in a browser on a new empty scratch query: a SQL editor that runs in\n"+
				"BigQuery, with completion and documentation for tables and columns. In a\n"+
				"Dataform or dbt project (or --dir) it keeps the scratch in .xf/tmp and knows the\n"+
				"project's models; anywhere else it is only the editor, and keeps scratches in\n"+
				"xf's user config folder, in tmp. It listens on 127.0.0.1 only and answers only\n"+
				"its own page (with --allow-host, the URL it prints carries a token every request\n"+
				"needs). ctrl+c here quits, and so does closing every browser window for 45s\n"+
				"(--web-auto-exit-seconds; 0 is off, as it is by default with --allow-host). A\n"+
				"run going holds that off until it ends.")
	} else {
		commandUsage(fs, "xf web [flags]",
			"Open xf in a browser: the TUI's panes and keys, for the project in the working\n"+
				"directory (or --dir), or outside any project, the projects xf has opened or\n"+
				"xf search found.\n"+
				"It listens on 127.0.0.1 only and answers only its own page (with --allow-host,\n"+
				"the URL it prints carries a token every request needs). Runs belong to xf web,\n"+
				"not to a tab. q in a project goes back to the projects, where typing a path\n"+
				"(~/… or /…) opens a project xf has not opened yet.\n"+
				"ctrl+c here, or in the projects page, quits, and so does closing every browser\n"+
				"window for 45s (--web-auto-exit-seconds; 0 is off, as it is by default with\n"+
				"--allow-host). A run going holds that off until it ends.")
	}
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see %s --help)", fs.Args(), cmdName)
	}
	if v := *sf.values[config.KeyWebAutoExitSeconds]; v != "" {
		if err := config.Check(config.KeyWebAutoExitSeconds, v); err != nil {
			return fmt.Errorf("--%s: %w", config.Flags[config.KeyWebAutoExitSeconds], err)
		}
	}
	o := webStart{cmdName: cmdName, port: *port, noOpen: *noOpen, app: *app, scratch: scratch}
	if scratch {
		o.project, o.location = *project, *location
	}
	for _, h := range strings.Split(*allowHost, ",") {
		if h = strings.TrimSpace(h); h != "" {
			o.hosts = append(o.hosts, h)
		}
	}
	return startWeb(pf, sf, o)
}

// webStart is how xf web starts: as xf web, xf scratch or xf impact --web.
type webStart struct {
	cmdName     string
	port        int
	noOpen, app bool
	hosts       []string
	// scratch opens a new empty scratch query, in project and location.
	scratch           bool
	project, location string
	// check opens the Checks page on that tab ("impact").
	check string
}

// startWeb serves xf web for the project pf finds, or outside any, the
// projects xf has opened, until ctrl+c.
func startWeb(pf projectFlags, sf *settingFlags, o webStart) error {
	cmdName := o.cmdName
	// q in a project goes back to the projects xf has opened, so they are
	// listed even when xf web starts in one. Outside any project, the
	// picker opens one by its path when none is remembered yet.
	cfg := web.Config{
		TermEnv:  nvimTermEnv,
		Projects: rememberedProjects,
		Open:     func(p session.Project) (*web.Project, error) { return openWebProject(sf, p.Root, p.Backend) },
		Branch: func(ctx context.Context, root string) (string, error) {
			return gitbranch.Repo{Dir: root}.Current(ctx)
		},
		Landing: func(ctx context.Context, emit func([]frontend.InfoRow)) {
			frontend.LandingInfoStream(ctx, landingInfo(), emit)
		},
		Auth:        webAuthAlerts,
		Credentials: gcpauth.Generation,
	}
	cfg.Theme, cfg.SetTheme = webTheme(sf)
	cfg.Settings = webSettings(sf)
	cfg.Hosts = o.hosts
	sf.remote = len(cfg.Hosts) > 0
	cfg.AutoExit = webAutoExit(sf)
	cfg.Log = func(line string) { fmt.Fprintln(os.Stderr, cmdName+":", line) }
	cfg.Forge = webForge(sf)
	cfg.OpenURL = browserOpener()
	if dir := sessionDir(); dir != "" {
		// Beside the sessions: ~/.local/state/xf/web-page.log.
		cfg.PageLog = filepath.Join(filepath.Dir(dir), "web-page.log")
	}
	root, name, err := pf.find()
	switch {
	case errors.Is(err, backend.ErrNoProject) && *pf.dir == "" && *pf.backend == "" && o.check == "":
		if o.scratch { // no project: the editor alone
			p, err := standaloneScratch(sf, o.project, o.location)
			if err != nil {
				return err
			}
			cfg.Project = p
		}
	case err != nil:
		return err
	default:
		p, err := openWebProject(sf, root, name)
		if err != nil {
			return err
		}
		if o.scratch {
			p.StartScratch = true
			p.ScratchProject, p.ScratchLocation = o.project, o.location
		}
		p.StartCheck = o.check
		cfg.Project = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg.Quit = stop
	srv, err := web.New(cfg)
	if err != nil {
		return err
	}
	defer srv.Close()
	l, err := web.Listen(o.port)
	if err != nil {
		return err
	}
	url := srv.URL(l)
	fmt.Fprintln(os.Stderr, cmdName+":", url)
	fmt.Fprintln(os.Stderr, cmdName+": ctrl+c quits")
	open, note := pageOpener(webBrowser(sf, o.app))
	if note != "" && !o.noOpen {
		fmt.Fprintln(os.Stderr, cmdName+":", note)
	}
	if open != nil && !o.noOpen {
		go func() {
			if err := open(url); err != nil {
				fmt.Fprintln(os.Stderr, cmdName+":", err)
			}
		}()
	}
	return srv.Serve(ctx, l)
}

// webAutoExit is how long xf web waits, once every browser window has
// closed, before it stops: --web-auto-exit-seconds, else the user's config
// file, read afresh so the settings page changes the next countdown, else
// the default, which is off when other devices reach xf web
// (--allow-host), as a phone's browser drops the stream when you switch
// apps.
func webAutoExit(sf *settingFlags) func() time.Duration {
	return func() time.Duration {
		l, err := sf.load("")
		if err != nil {
			l = config.Loaded{Sources: map[string]string{}}
			sf.apply(&l)
		}
		if !l.IsSet(config.KeyWebAutoExitSeconds) {
			if sf.remote {
				return 0
			}
			return config.DefaultWebAutoExitSeconds * time.Second
		}
		n, _ := strconv.Atoi(l.WebAutoExitSeconds)
		return time.Duration(max(n, 0)) * time.Second
	}
}

// webForge is how xf web reaches GitLab and GitHub for the branch's merge
// request and pipeline (ADR 0020): the tokens in tokens.json beside the
// user's config, and forgePollSeconds, read afresh before each wait so
// the settings page changes the next one.
func webForge(sf *settingFlags) *web.Forge {
	home, _ := os.UserHomeDir()
	return &web.Forge{Hosts: forge.NewStore(config.UserDir(os.Getenv, home)), Poll: func() time.Duration {
		n := config.DefaultForgePollSeconds
		if l, err := sf.load(""); err == nil && l.ForgePollSeconds != "" {
			if v, err := strconv.Atoi(l.ForgePollSeconds); err == nil {
				n = v
			}
		}
		return time.Duration(max(n, config.MinForgePollSeconds)) * time.Second
	}}
}

// webBrowser is what xf web opens its page in: --web-browser, else the
// user's config file, else the system's browser. --app makes the default
// an app window; a browser the setting names is kept.
func webBrowser(sf *settingFlags, app bool) string {
	l, err := sf.load("")
	if err != nil {
		l = config.Loaded{Sources: map[string]string{}}
		sf.apply(&l)
	}
	b := l.WebBrowser
	if app && (b == "" || b == config.BrowserDefault) {
		b = config.BrowserApp
	}
	return b
}

// openWebProject connects to the engine of the project at root and makes
// what xf web needs of it, as openProject does for the TUI: the same
// deps, but for the neovim it would run beside, and the session that
// remembers the selection.
func openWebProject(sf *settingFlags, root, name string) (*web.Project, error) {
	settings, err := sf.load(root)
	if err != nil {
		return nil, err
	}
	b, err := openWebBackend(name, root, settings)
	if err != nil {
		return nil, err
	}
	client, err := connect(context.Background(), root, b, nil)
	if err != nil {
		return nil, err
	}
	deps := projectDeps(root, name, b, settings, client)
	if _, ok := b.(*dbt.Backend); ok {
		deps.ColumnImpact = client.ColumnImpact
	}
	// The page can change the editor o opens files with, since the one
	// in $EDITOR is often a terminal editor it can't run.
	ed := newWebEditor(root, settings)
	ed.load = func() (config.Loaded, error) { return sf.load(root) }
	deps.Open = func(file string, line int) (frontend.Opening, error) {
		return editorOpener(client.ShowDocument, nil, ed.get(), root)(file, line)
	}
	p := &web.Project{Deps: deps, Close: client.Close, Editor: ed.hook(), BigQuery: lazyClient()}
	if d, ok := b.(dataform.Backend); ok && d.Bin == "" {
		// Opened without the CLI (openWebBackend): the page says how to
		// install it, and compiles again once it is on PATH.
		p.InstallCLI = &web.InstallCLI{Name: "dataform", Command: dataform.InstallCommand, Found: func() bool {
			_, err := resolveBin(settings)
			return err == nil
		}}
	}
	p.ScratchWarnGB, _ = strconv.ParseFloat(settings.ScratchWarnGB, 64)
	p.ScratchConfirmGB, _ = strconv.ParseFloat(settings.ScratchConfirmGB, 64)
	p.SourceAutosaveMs, _ = strconv.Atoi(settings.SourceAutosaveMs)
	p.DiffStyle = settings.DiffStyle
	p.PythonLanguageServer = settings.PythonLanguageServer
	p.SqlfluffBin, p.SqlfluffConfig = settings.SqlfluffBin, settings.SqlfluffConfig
	p.AutoHidePane2 = settings.AutoHidePane2 != "false"
	p.Pane2Tab = settings.Pane2Tab
	p.WrapLines = settings.WrapLines == "true"
	p.VimMode = settings.VimMode == "true"
	p.VimKeys = vimKeys(settings.Settings)
	if dir := sessionDir(); dir != "" {
		file := session.Path(dir, root)
		_ = session.Visit(file, root, name, time.Now())
		p.Deps.Resume = session.Load(file)
		p.SaveSession = func(s session.Session) {
			s.Root, s.Backend, s.LastVisited = root, name, time.Now()
			// xf serve keeps defer to prod's switch here: leave it as it is.
			s.DeferToProd = session.Load(file).DeferToProd
			if err := session.Save(file, s); err != nil {
				fmt.Fprintln(os.Stderr, "xf web: could not save the session:", err)
			}
		}
	}
	return p, nil
}

// nvimTermEnv is what xf web's terminal tab adds to its shells'
// environment, so a neovim started there loads xf's plugin and language
// server as one `xf --nvim` starts does. The plugin is installed once;
// when it can't be, the shells get nothing added.
var nvimTermEnv = sync.OnceValue(func() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	rt, err := nvim.Install(filepath.Join(cache, "xf"))
	if err != nil {
		return nil
	}
	env, err := nvim.TermEnv(rt, []string{exe}, []string{exe, "lsp"})
	if err != nil {
		return nil
	}
	return env
})
