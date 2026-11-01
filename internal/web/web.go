// Package web is xf web: the TUI's panes in a browser. A Server serves
// projects to the browser tabs on this machine, each tab one project,
// named in its URL (see projects.go). It is a client of each project's
// engine as the TUI is, given the same frontend.Deps,
// and it works out what the TUI works out (finding actions, lineage, run
// commands, reports) in Go, mostly through package ui's exported helpers:
// the page only draws what it is sent and keeps its own view state, such
// as the selection and the focused pane.
//
// The page reads one stream of events, a WebSocket (/api/events): the
// engine's state, runs and their log, the editor's active file and the
// progress of long checks. Everything else is a JSON request. Runs belong
// to the server, so every tab shows the one going, and a reload picks it
// up again; each tab has its own selection, panes and scroll.
//
// Only this machine's browsers can reach it: it listens on 127.0.0.1, and
// a request must name the server's own host and come from its own page (by
// the browser's Origin and Sec-Fetch-Site headers), which keeps other sites
// (and DNS rebinding) from driving runs. A token is needed only when other
// devices can reach it (--allow-host) or a proxy forwards the request.
package web

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/webtheme"
)

//go:embed assets
var assets embed.FS

// devAssetsEnv names a directory to serve the page from instead of the
// copy built into xf, for working on it.
const devAssetsEnv = "XF_WEB_ASSETS"

// cookieName holds the token once a tab has opened the URL with it, when
// the server needs one.
const cookieName = "xf_token"

// Config is what a Server serves.
type Config struct {
	// Project is the open project; nil serves the project picker
	// (Projects) until one is opened.
	Project *Project
	// Projects lists the remembered projects the picker shows, read
	// afresh each time, and Open opens one. With Open, q in a project
	// closes it and goes back to the picker; without it, q quits.
	Projects func() []session.Project
	Open     func(p session.Project) (*Project, error)
	// Token is the secret a request must carry when Hosts are given or a
	// proxy forwards it; "" makes one.
	Token string
	// Hosts are names besides localhost and loopback addresses a
	// request may be addressed to, such as a Tailscale name that proxies
	// to the server. With any, every request needs the token, as other
	// devices can reach the server.
	Hosts []string
	// Quit is called when a page asks xf web to quit, and when every
	// page has been gone for AutoExit.
	Quit func()
	// AutoExit is how long xf web waits, once every page that connected
	// has gone, before it quits; it is read as each countdown starts, so
	// a change applies to the next one. nil or 0 never quits on its own.
	AutoExit func() time.Duration
	// Log prints a line to the terminal xf web runs in, such as that it
	// is counting down to quit; nil prints nothing.
	Log func(line string)
	// Theme is xf web's saved colour theme, auto or a theme's id, with
	// the themes auto shows: the user's, or with a project's root also
	// what that project's config file sets over it. SetTheme saves one of
	// them (by its config key: webTheme, webThemeLight or webThemeDark)
	// to the user's config file, or with a root to that project's, where
	// an empty id takes the key out; nil keeps xf's own, and the page's
	// picker then changes them for this run only. It is not the TUI's:
	// the TUI follows the terminal, the page the browser.
	Theme    func(root string) webtheme.Choice
	SetTheme func(root, key, id string) error
	// Settings reads and writes the config files for the settings page;
	// nil hides it.
	Settings *Settings
	// Branch reads a project's checked-out branch for the picker
	// (gitbranch.Repo.Current); nil leaves the branches out.
	Branch func(ctx context.Context, root string) (string, error)
	// Landing gives emit the settings page's read-only rows outside any
	// project as they arrive: the tools on PATH and the Google accounts
	// (frontend.LandingInfoStream); nil shows none.
	Landing func(ctx context.Context, emit func([]frontend.InfoRow))
	// Auth looks up the sign-in problems the picker warns of, after it
	// shows (gcpauth.Alerts); nil warns of none.
	Auth func(ctx context.Context) []gcpauth.Alert
	// Credentials returns the Google credentials' generation
	// (gcpauth.Generation), which goes up at each new login; nil never
	// looks for one (credentials.go).
	Credentials func() uint64
	// Forge reaches GitLab and GitHub for the branch's merge request and
	// pipeline (ADR 0020); nil shows none.
	Forge *Forge
	// TermEnv is added to the environment of the terminal tab's shells,
	// read as each starts: the variables that let a neovim started there
	// load xf's plugin and language server. nil adds none.
	TermEnv func() []string
	// PageLog is the file the pages' debugging notes are appended to
	// (pagelog.go); "" keeps none.
	PageLog string
	// OpenURL opens a link in the system's browser, for a page in an app
	// window (openurl.go); nil leaves the page to open it.
	OpenURL func(url string) error
}

// Project is an open project.
type Project struct {
	// Deps are the project's, as the TUI would get them.
	Deps frontend.Deps
	// SaveSession keeps what to restore when xf next opens the project;
	// nil keeps nothing.
	SaveSession func(session.Session)
	// Close lets go of the project's engine; nil has nothing to close.
	Close func()
	// Editor changes the editor o opens files with; nil can't.
	Editor *Editor
	// BigQuery makes the client scratch queries run with; nil hides the
	// Scratch view.
	BigQuery func(ctx context.Context) (*bigquery.Client, error)
	// ScratchWarnGB and ScratchConfirmGB are the sizes of a scratch's dry
	// run, in GiB, at which the page turns it amber, and red and asks
	// before running; 0 uses the defaults.
	ScratchWarnGB, ScratchConfirmGB float64
	// SourceAutosaveMs is how long the source pane waits after an edit
	// before saving; 0 is off.
	SourceAutosaveMs int
	// DiffStyle is how Source Control shows a difference to start with:
	// "side-by-side", or "" or "inline" for inline.
	DiffStyle string
	// PythonLanguageServer is what checks the source pane's Python files:
	// "off" for nothing, else pyrefly (ADR 0018).
	PythonLanguageServer string
	// SqlfluffBin is the sqlfluff that formats SQL ("" finds one) and
	// SqlfluffConfig the config file it formats with ("" uses the project's
	// own, else xf's) (ADR 0024).
	SqlfluffBin, SqlfluffConfig string
	// AutoHidePane2 hides pane 2 while the source pane shows a file with
	// no action.
	AutoHidePane2 bool
	// Pane2Tab is the tab pane 2 starts on in a new browser tab:
	// "columns", or "" or "sql" for sql.
	Pane2Tab string
	// WrapLines wraps long lines in the page's editors.
	WrapLines bool
	// VimKeys are the keys of the editor's own vim commands, by config
	// key (config.VimKeys); a key left out keeps its default.
	VimKeys map[string]string
	// ScratchDir is where scratches are kept, "" for .xf/tmp in the
	// project's root.
	ScratchDir string
	// ScratchProject and ScratchLocation are what a new scratch runs in
	// when its model says nothing (xf scratch --project, --location).
	ScratchProject, ScratchLocation string
	// Standalone says there is no project: the page is the Scratch view
	// and nothing else. StartScratch opens the Scratch view on a new empty
	// scratch when the page starts.
	Standalone, StartScratch bool
	// StartCheck opens that tab of the Checks page ("impact") when the
	// first page starts (xf impact --web).
	StartCheck string
	// InstallCLI is how to install the tool's CLI, for a project that
	// opened without it (a Dataform project with no dataform on PATH);
	// nil when it has one.
	InstallCLI *InstallCLI
}

// InstallCLI is the CLI a project opened without: pane 2's errors tab
// shows Command beside the compile's error and can run it in the
// terminal tab, and the project compiles again once Found.
type InstallCLI struct {
	// Name is the CLI's, such as "dataform"; Command installs it.
	Name, Command string
	// Found says whether the CLI can be found now.
	Found func() bool
}

// Editor is what the page needs to change the editor o opens files with,
// when no editor is connected: a terminal editor, such as one from
// $EDITOR, can't open from a browser.
type Editor struct {
	// Current is the editor command o uses now, where it came from, and
	// whether o can run it from the page (a window editor on PATH).
	Current func() (command, from string, usable bool)
	// Choices are the window editors xf knows, each on PATH or not.
	Choices func() []EditorChoice
	// Set makes command the editor o uses, saved to the user's config
	// file, or the project's with project set; it fails for a command o
	// can't run from the page. It answers with the file it saved to.
	Set func(command string, project bool) (file string, err error)
	// Reload reads the editor setting again, after the settings page
	// changed it; nil can't.
	Reload func()
}

// EditorChoice is a window editor xf knows.
type EditorChoice struct {
	Command string `json:"command"`
	Name    string `json:"name"`
	Found   bool   `json:"found"`
}

// Server is xf web's HTTP server.
type Server struct {
	cfg   Config
	token string
	hub   *hub
	exit  *autoExit

	mu sync.Mutex
	// open are the projects tabs show, by root.
	open map[string]*openProject
	// startKey is the key of the project xf web started in, "" for none.
	startKey string
	// tabs are what each tab said of its unsaved files (unsaved.go).
	tabs map[string]*tabRecord
	// summaryTimer sends the projects' summaries soon, and summarySent
	// are the last sent (summary.go).
	summaryTimer *time.Timer
	summarySent  []byte
	// opening serialises opening projects, so two tabs share one.
	opening sync.Mutex
	// settingsWrite serialises the JSON view's saves of a config file.
	settingsWrite sync.Mutex
	// picked is the theme picked this run when there is no config to
	// save it to (Config.Theme is nil).
	picked webtheme.Choice
	// pageLog keeps what the pages note for debugging (pagelog.go).
	pageLog pageLog
	// credsGen is the credentials' generation last seen (credentials.go).
	credsGen uint64
}

// New makes a server for cfg, starting to follow its project's engine.
func New(cfg Config) (*Server, error) {
	token := cfg.Token
	if token == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(b[:])
	}
	s := &Server{cfg: cfg, token: token, hub: newHub(), open: map[string]*openProject{}, pageLog: pageLog{path: cfg.PageLog}}
	s.exit = newAutoExit(cfg.AutoExit, s.busy, cfg.Quit, cfg.Log)
	if cfg.Credentials != nil {
		s.credsGen = cfg.Credentials()
	}
	if cfg.Project != nil {
		s.startKey = s.adopt(cfg.Project).key
	}
	return s, nil
}

// Token is the secret the URL carries when the server needs one.
func (s *Server) Token() string { return s.token }

// Close stops following the projects and saves their sessions.
func (s *Server) Close() {
	s.mu.Lock()
	open := make([]*openProject, 0, len(s.open))
	for _, op := range s.open {
		open = append(open, op)
	}
	s.mu.Unlock()
	for _, op := range open {
		s.drop(op)
	}
	s.exit.stop()
	s.hub.close()
}

// running reports whether any open project has a run going.
func (s *Server) running() bool {
	s.mu.Lock()
	open := make([]*projectState, 0, len(s.open))
	for _, op := range s.open {
		open = append(open, op.p)
	}
	s.mu.Unlock()
	for _, p := range open {
		if p.running() {
			return true
		}
	}
	return false
}

// busy is what holds off auto-exit: "the run" while one goes, "the
// terminal command" while a terminal tab runs one, "" for nothing.
func (s *Server) busy() string {
	if s.running() {
		return "the run"
	}
	s.mu.Lock()
	open := make([]*projectState, 0, len(s.open))
	for _, op := range s.open {
		open = append(open, op.p)
	}
	s.mu.Unlock()
	for _, p := range open {
		if p.termCommand() != "" {
			return "the terminal command"
		}
		p.mu.Lock()
		inspecting := p.inspectorRunningLocked()
		p.mu.Unlock()
		if inspecting {
			return "the dependency inspector's queries"
		}
	}
	return ""
}

// Listen listens on 127.0.0.1:port, any free port for 0.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// URL is the address to open in a browser for a server on l: the
// project it started in, if any, with the token when the server needs it.
func (s *Server) URL(l net.Listener) string {
	q := url.Values{}
	if s.remote() {
		q.Set("token", s.token)
	}
	if s.startKey != "" {
		q.Set("project", s.startKey)
	}
	u := "http://" + l.Addr().String() + "/"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// remote reports whether other devices can reach the server
// (--allow-host), so every request needs the token.
func (s *Server) remote() bool { return len(s.cfg.Hosts) > 0 }

// Serve serves on l until ctx is done.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		s.exit.stop() // the pages going now is xf web stopping
		// The pages are told before their streams end, so they say xf
		// web stopped (ctrl+c in its terminal, say) rather than that
		// they lost it.
		s.hub.send("stopping", nil)
		s.hub.close() // ends the event streams, closing their WebSockets
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler is the server's routes, behind the host, origin and token checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// The system's mime table may not know the manifest, which Chrome
	// wants served as its own type to offer "Install page as app".
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	static, _ := fs.Sub(assets, "assets")
	tags := assetTags()
	if dir := os.Getenv(devAssetsEnv); dir != "" {
		static = os.DirFS(dir) // edit the page without rebuilding xf
		tags = nil
	}
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		assetTag(w, r, tags)
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/term/stream", s.termStream)
	mux.HandleFunc("GET /api/boot", s.boot)
	mux.HandleFunc("GET /themes.css", themesCSS)
	mux.HandleFunc("POST /api/theme", s.setTheme)
	mux.HandleFunc("POST /api/pick", s.pick)
	mux.HandleFunc("POST /api/forget", s.forget)
	mux.HandleFunc("GET /api/picker/branches", s.pickerBranches)
	mux.HandleFunc("GET /api/picker/info", s.pickerInfo)
	mux.HandleFunc("GET /api/picker/auth", s.pickerAuth)
	mux.HandleFunc("POST /api/credentials", s.checkCredentials)
	mux.HandleFunc("POST /api/quit", s.quit)
	mux.HandleFunc("POST /api/close", s.closeOpen)
	mux.HandleFunc("POST /api/unsaved", s.setUnsaved)
	mux.HandleFunc("GET /api/unsaved", s.unsaved)
	mux.HandleFunc("GET /api/projects", s.openProjects)
	mux.HandleFunc("GET /api/scratch/functions", s.scratchFunctions)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("POST /api/settings", s.setSetting)
	mux.HandleFunc("GET /api/settings/file", s.settingsFile)
	mux.HandleFunc("POST /api/settings/file", s.saveSettingsFile)
	mux.HandleFunc("POST /api/reopen", s.reopen)
	mux.HandleFunc("POST /api/pagelog", s.postPageLog)
	mux.HandleFunc("POST /api/open-url", s.openURL)
	s.forgeRoutes(mux)
	s.worktreeRoutes(mux)
	s.projectRoutes(mux)
	return s.guard(mux)
}

// guard lets a request through only when it names this server's host and
// comes from this server's page, or is the browser opening it. When other
// devices can reach the server, or a proxy forwards the request, it must
// carry the token too: in the query, which also sets the cookie (and
// redirects to the same page without it), or in the cookie. On loopback
// alone there is none to lose, so a reload, a restart on the same port and
// several servers at once all keep working.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) && !s.allowedHost(r.Host) {
			http.Error(w, "xf web answers on 127.0.0.1 and localhost only (see --allow-host)", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request", http.StatusForbidden)
				return
			}
		}
		if !sameSite(r) {
			http.Error(w, "cross-site request", http.StatusForbidden)
			return
		}
		if !s.remote() && !proxied(r) {
			next.ServeHTTP(w, r)
			return
		}
		if t := r.URL.Query().Get("token"); t != "" && s.validToken(t) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			q := r.URL.Query()
			q.Del("token")
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(cookieName); err != nil || !s.validToken(c.Value) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			msg := "xf web: open the URL xf printed, which carries this server's token."
			if !s.remote() {
				msg = "xf web: this request came through a proxy, which needs --allow-host and the URL xf then prints, with its token."
			}
			_, _ = fmt.Fprintln(w, msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameSite reports whether a request's Sec-Fetch-Site, which browsers
// send and pages can't set, says it came from this server's own page or
// from the user (typed, a bookmark, another app opening the URL). Another
// site may only open the page itself, as a link does: it can't read it,
// and the page's own requests are then same-origin. Without the header
// (not a browser, or an old one) the Origin check is what there is.
func sameSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
		r.Header.Get("Sec-Fetch-Dest") == "document" && !strings.HasPrefix(r.URL.Path, "/api/")
}

// proxied reports whether a request carries the headers a reverse proxy
// adds, such as one that rewrites Host to 127.0.0.1 and so would pass for
// this machine's browser. A page can't add them to another site's request.
func proxied(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func (s *Server) validToken(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// allowedHost reports whether a request's Host is one of cfg.Hosts, with
// or without a port.
func (s *Server) allowedHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	for _, allowed := range s.cfg.Hosts {
		if strings.EqualFold(h, strings.TrimSuffix(allowed, ".")) {
			return true
		}
	}
	return false
}

// localHost reports whether a request's Host is a loopback name or
// address, with or without a port.
func localHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// bootView is what a page starts from.
type bootView struct {
	// Picker lists the remembered projects when the page names none:
	// those ?q matches.
	Picker  []pickerProject `json:"picker"`
	Project *projectBoot    `json:"project,omitempty"`
	// Key is the project's key, which the page names it by.
	Key string `json:"key,omitempty"`
	// Missing says why the project the page named didn't open; the
	// picker then shows.
	Missing string `json:"missing,omitempty"`
	// OpenIn is where a project picked from a project's page opens:
	// same-tab or new-tab (webOpenProjectIn).
	OpenIn string `json:"openIn"`
	// Open sums up the open projects, the last shown first.
	Open []projectSummary `json:"open"`
	// Paths are the rows of a ?q that is a path (see frontend.PathRows), which
	// adds a project xf has not opened before; Total is how many
	// projects are remembered.
	Paths []frontend.PathRow `json:"paths,omitempty"`
	Total int                `json:"total"`
	// Theme is the colour theme to start with, and Themes every one the
	// picker lists.
	Theme  webtheme.Choice  `json:"theme"`
	Themes []webtheme.Theme `json:"themes"`
	// ThemeOwn are the theme keys the project's own config file sets.
	ThemeOwn []string `json:"themeOwn,omitempty"`
}

type pickerProject struct {
	Root string `json:"root"`
	// Path is Root as the picker shows it, the home directory as ~.
	Path    string `json:"path"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	When    string `json:"when"`
	// Key is what the page's URL names it by; Open says a tab shows it.
	Key  string `json:"key"`
	Open bool   `json:"open,omitempty"`
	// Unsaved are its files tabs have unsaved changes in.
	Unsaved []string `json:"unsaved,omitempty"`
	// worktreeOf puts a worktree's project under its main checkout's.
	worktreeOf
}

func (s *Server) boot(w http.ResponseWriter, r *http.Request) {
	theme := s.theme("")
	openIn := s.openIn()
	missing := ""
	if key := r.URL.Query().Get("project"); key != "" {
		op, err := s.ensure(key)
		if err == nil {
			s.touch(op)
			writeJSON(w, bootView{Project: op.p.boot(), Key: op.key, Theme: s.theme(op.p.deps.Root), ThemeOwn: themeOwn(op.p.deps.Root), Themes: webtheme.All(), OpenIn: openIn, Open: s.summaries()})
			return
		}
		missing = err.Error()
	}
	projects := s.projects()
	s.mu.Lock()
	open := s.openRootsLocked()
	s.mu.Unlock()
	// ?q filters the projects as the TUI's picker does, or is a path.
	q := r.URL.Query().Get("q")
	out := []pickerProject{}
	now := time.Now()
	home, _ := os.UserHomeDir()
	if frontend.IsPathQuery(q) {
		writeJSON(w, bootView{Picker: out, Paths: frontend.PathRows(home, q, projects), Total: len(projects), Missing: missing, Theme: theme, Themes: webtheme.All(), OpenIn: openIn, Open: s.summaries()})
		return
	}
	isOpen := map[string]bool{}
	for _, root := range open {
		isOpen[project.RealPath(root)] = true
	}
	roots := make([]string, len(projects))
	for i, pr := range projects {
		roots[i] = pr.Root
	}
	for _, pr := range projects {
		if !frontend.ProjectMatches(q, pr) {
			continue
		}
		wv := worktreeView(pr.Root)
		wv.Main = mainAs(wv.Main, roots)
		out = append(out, pickerProject{Root: pr.Root, Path: frontend.ShortPath(home, pr.Root), Name: projectName(pr.Root), Backend: cmp.Or(pr.Backend, "dataform"),
			When: frontend.RelTime(now, pr.LastVisited), Key: s.keyOf(pr.Root, projects, open), Open: isOpen[project.RealPath(pr.Root)],
			Unsaved: s.unsavedOf(pr.Root, ""), worktreeOf: wv})
	}
	writeJSON(w, bootView{Picker: out, Total: len(projects), Missing: missing, Theme: theme, Themes: webtheme.All(), OpenIn: openIn, Open: s.summaries()})
}

// openIn is where a project picked from a project's page opens:
// webOpenProjectIn's value, same-tab unless it says new-tab.
func (s *Server) openIn() string {
	if s.cfg.Settings != nil && valueOf(s.cfg.Settings.View("", ""), config.KeyWebOpenProjectIn) == config.OpenNewTab {
		return config.OpenNewTab
	}
	return config.OpenSameTab
}

// pick opens a project, unless it is open, and answers with its key,
// which the tab that picked it (or a new one) then names in its URL. It
// is a remembered project, or with backend that tool's project at root,
// which xf then remembers: one it has opened before through another path
// opens as it was.
func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root    string `json:"root"`
		Backend string `json:"backend"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Open == nil {
		http.Error(w, "no such project", http.StatusNotFound)
		return
	}
	pr, ok := s.toOpen(req.Root, req.Backend)
	if !ok {
		http.Error(w, "no such project", http.StatusNotFound)
		return
	}
	if req.Backend != "" {
		if _, err := backend.Detect(pr.Root, req.Backend); err != nil {
			writeError(w, err)
			return
		}
	}
	op, err := s.openRoot(pr)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, struct {
		OK  bool   `json:"ok"`
		Key string `json:"key"`
	}{true, op.key})
}

// toOpen is the project pick opens for root and backend: the remembered
// project at root, through any path to it, or with backend a new one at
// root, which must be absolute.
func (s *Server) toOpen(root, tool string) (session.Project, bool) {
	real := project.RealPath(root)
	for _, pr := range s.projects() {
		if pr.Root == root || tool != "" && project.RealPath(pr.Root) == real {
			pr.Backend = cmp.Or(tool, pr.Backend)
			return pr, true
		}
	}
	if tool == "" || !filepath.IsAbs(root) {
		return session.Project{}, false
	}
	return session.Project{Root: filepath.Clean(root), Backend: tool}, true
}

// forget drops a remembered project from the picker, as the TUI's
// picker's x does.
func (s *Server) forget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	for _, pr := range s.projects() {
		if pr.Root == req.Root {
			if err := session.Forget(pr); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, okView{OK: true})
			return
		}
	}
	http.Error(w, "no such project", http.StatusNotFound)
}

// projects are the remembered projects, as they are now.
func (s *Server) projects() []session.Project {
	if s.cfg.Projects == nil {
		return nil
	}
	return s.cfg.Projects()
}

// closeOpen closes a project for every page on it: q in it, or del on it
// in the projects list, which names its key. A local run of it is
// cancelled, a run in GCP carries on there, and its session is saved.
// It answers with the projects still open, the last shown first, for the
// closing page to go to. Without a way to open another project, it quits
// instead.
func (s *Server) closeOpen(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Open == nil {
		s.quit(w, r)
		return
	}
	var req struct {
		// Key names the project, else the page's own (projectHeader).
		Key string `json:"key"`
		// By is the browser window closing it, as the page words it.
		By string `json:"by"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var op *openProject
	if req.Key != "" {
		s.mu.Lock()
		op = s.openByKeyLocked(req.Key)
		s.mu.Unlock()
	} else {
		op = s.projectOf(r)
	}
	if op == nil {
		writeJSON(w, struct {
			OK   bool     `json:"ok"`
			Open []string `json:"open"`
		}{true, s.openKeys(nil)}) // closed already
		return
	}
	writeJSON(w, struct {
		OK   bool     `json:"ok"`
		Open []string `json:"open"`
	}{true, s.closeProject(op, req.By)})
}

// quit stops xf web: a local run is cancelled, and a run in GCP carries
// on there.
func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
	s.exit.stop()
	s.hub.send("quit", nil)
	if s.cfg.Quit != nil {
		go func() {
			time.Sleep(200 * time.Millisecond) // let the answer and the event go out
			s.cfg.Quit()
		}()
	}
}

func lastElem(path string) string {
	path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// errorView is a failed request's answer: the error's text, and a kind
// the page tells some errors apart by.
type errorView struct {
	Error string `json:"error"`
	Kind  string `json:"kind,omitempty"`
}

func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	v := errorView{Error: err.Error()}
	switch {
	case errors.Is(err, engine.ErrStale) || err.Error() == engine.ErrStale.Error():
		v.Kind = "stale"
	case strings.Contains(err.Error(), "could not compile"):
		v.Kind = "compile"
	case errors.Is(err, bigquery.ErrAuth):
		v.Kind = "auth"
	}
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes the request's body into v, answering 400 when it
// can't.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// actionRef is an action as lists show it: its ID, "dataset.name", kind
// and file.
type actionRef struct {
	ID       project.ActionID `json:"id"`
	Label    string           `json:"label"`
	Name     string           `json:"name"`
	Kind     string           `json:"kind"`
	Short    string           `json:"short"`
	File     string           `json:"file,omitempty"`
	Disabled bool             `json:"disabled,omitempty"`
	Runnable bool             `json:"runnable,omitempty"`
	// External: defined outside the project (a declaration or source),
	// Unknown: not defined at all.
	External bool `json:"external,omitempty"`
	Unknown  bool `json:"unknown,omitempty"`
	Changed  bool `json:"changed,omitempty"`
	// ConsoleURL is its table's page in the BigQuery console, "" when it
	// has none. An operation with hasOutput has one.
	ConsoleURL string `json:"consoleUrl,omitempty"`
}

func refOf(a *project.Action) actionRef {
	url := ""
	if hasOutputTable(a) {
		url = a.Target.ConsoleURL()
	}
	return actionRef{ConsoleURL: url, ID: a.ID, Label: strings.TrimPrefix(a.Target.Schema+"."+a.Target.Name, "."), Name: a.Target.Name,
		Kind: a.Kind.String(), Short: a.Kind.Short(), File: a.FileName, Disabled: a.Disabled, Runnable: a.Runnable(),
		External: a.Kind.External(), Unknown: a.Kind == project.KindUnknown}
}
