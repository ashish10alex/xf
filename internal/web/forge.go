package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gitscm"
)

// The branch's merge request and its last pipeline, on GitLab or GitHub
// (ADR 0020). The server asks the host, never the page: when a page opens
// the project or gets focus back, after a push, pull, fetch or branch
// switch, when the base changes, and on Refresh MR; while the pipeline
// runs it asks again every forgePollSeconds, and once the host can't be
// reached it backs off. A timed check finds out only for a project some
// page shows; the next page to show it asks. Every page hears the answer
// as a "forge" event.

// Forge is what xf web needs to reach GitLab and GitHub.
type Forge struct {
	// Hosts are the hosts xf knows, with their tokens.
	Hosts *forge.Store
	// Poll is how long to wait before asking again while a pipeline
	// runs, read as each wait starts; nil is config.DefaultForgePollSeconds.
	Poll func() time.Duration
	// HTTP calls the hosts; nil uses a client of forge's own.
	HTTP *http.Client
	// Client makes the client for a host; nil is forge.New. Tests give a
	// stand-in.
	Client func(kind forge.Kind, token string) forge.Client
}

func (f *Forge) client(kind forge.Kind, token string) forge.Client {
	if f.Client != nil {
		return f.Client(kind, token)
	}
	return forge.New(kind, token, f.HTTP)
}

func (f *Forge) poll() time.Duration {
	if f.Poll != nil {
		if d := f.Poll(); d > 0 {
			return d
		}
	}
	return 10 * time.Second
}

// forgeView is the "forge" event, and GET /api/forge's answer.
type forgeView struct {
	// State is "off" (nothing to show: no repository, no remote, or a
	// host xf doesn't know), "connect" (no token for the host), "token"
	// (the host refused it), "ok" or "error".
	State string     `json:"state"`
	Kind  forge.Kind `json:"kind,omitempty"`
	Host  string     `json:"host,omitempty"`
	Path  string     `json:"path,omitempty"`
	// Web is the project's page.
	Web    string `json:"web,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Base is the branch R, I and C compare with, which a new merge
	// request targets.
	Base     string          `json:"base,omitempty"`
	MR       *forge.MR       `json:"mr"`
	Pipeline *forge.Pipeline `json:"pipeline"`
	// Stale says the host couldn't be reached: MR and Pipeline are what
	// it said last, at CheckedAt (Unix milliseconds).
	Stale     bool   `json:"stale,omitempty"`
	CheckedAt int64  `json:"checkedAt,omitempty"`
	Error     string `json:"error,omitempty"`
	Checking  bool   `json:"checking,omitempty"`
	// TokenPage is the host's page for making a token.
	TokenPage string `json:"tokenPage,omitempty"`
}

// forgeState is the checks of one project.
type forgeState struct {
	mu       sync.Mutex
	view     forgeView
	checking bool
	// again is a check asked for while one ran.
	again bool
	timer *time.Timer
	// fails are the checks in a row the host couldn't be reached in.
	fails int
	// missed says a timed check came with no page to show it to.
	missed bool
	// last is when the last check ended; branch the branch view sendBranch
	// sent last, to see it change.
	last   time.Time
	branch string
	closed bool
}

// forgeTimeout bounds one check, every call to the host in it.
const forgeTimeout = 45 * time.Second

// forgeSoftAge is how old a check may be before focus asks again.
const forgeSoftAge = 15 * time.Second

// backoff is how long to wait after fails checks in a row found the host
// unreachable: 30 s, doubling, at most 5 minutes.
func backoff(fails int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < fails && d < 5*time.Minute; i++ {
		d *= 2
	}
	return min(d, 5*time.Minute)
}

// forgeCheck asks the host again, soon; one already asking asks once
// more when it ends.
func (p *projectState) forgeCheck() {
	if p.forgeCfg == nil {
		return
	}
	f := &p.forge
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if f.checking {
		f.again = true
		f.mu.Unlock()
		return
	}
	f.checking = true
	f.missed = false
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.view.Checking = true
	v := f.view
	f.mu.Unlock()
	p.hub.send("forge", v)
	go p.runForgeCheck()
}

func (p *projectState) runForgeCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), forgeTimeout)
	defer cancel()
	f := &p.forge
	f.mu.Lock()
	was := f.view
	f.mu.Unlock()
	v, unreachable := p.lookForge(ctx, was)
	f.mu.Lock()
	f.checking = false
	f.last = time.Now()
	if unreachable {
		f.fails++
	} else {
		f.fails = 0
	}
	f.view = v
	again := f.again
	f.again = false
	var wait time.Duration
	switch {
	case unreachable:
		wait = backoff(f.fails)
	case v.Pipeline.Unfinished():
		wait = p.forgeCfg.poll()
	}
	if wait > 0 && !again && !f.closed {
		f.timer = time.AfterFunc(wait, p.forgeTimed)
	}
	f.mu.Unlock()
	p.hub.send("forge", v)
	if again {
		p.forgeCheck()
	}
}

// forgeTimed is a timed check: only for a project a page shows.
func (p *projectState) forgeTimed() {
	if p.hub.count() == 0 {
		p.forge.mu.Lock()
		p.forge.missed = true
		p.forge.timer = nil
		p.forge.mu.Unlock()
		return
	}
	p.forgeCheck()
}

// lookForge works out the branch's repository on its host and asks it;
// unreachable says the host didn't answer. was is the view before, whose
// merge request and pipeline stay, stale, while the host can't be
// reached.
func (p *projectState) lookForge(ctx context.Context, was forgeView) (v forgeView, unreachable bool) {
	off := forgeView{State: "off"}
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		return off, false
	}
	st, err := g.Status(ctx)
	if err != nil || st.Branch == "" {
		return off, false
	}
	remote := forge.PushRemote(ctx, g.Top, st.Branch)
	raw, err := forge.RemoteURL(ctx, g.Top, remote)
	if err != nil {
		return off, false
	}
	r, ok := forge.ParseRemote(raw)
	if !ok {
		return off, false
	}
	a, ok := forge.Match(ctx, p.forgeCfg.Hosts, r)
	if !ok {
		return off, false
	}
	repo := forge.RepoAt(a.Kind, a.Name, r.Path)
	v = forgeView{Kind: a.Kind, Host: repo.Host, Path: repo.Path, Web: repo.Web, Branch: st.Branch, Base: p.baseBranch(),
		TokenPage: forge.TokenPage(a.Kind, a.Name)}
	if a.Token == "" {
		v.State = "connect"
		return v, false
	}
	found, err := p.forgeCfg.client(a.Kind, a.Token).Find(ctx, repo, st.Branch, v.Base)
	same := was.Branch == v.Branch && was.Host == v.Host && was.Path == v.Path
	switch {
	case err == nil:
		v.State, v.MR, v.Pipeline, v.CheckedAt = "ok", found.MR, found.Pipeline, time.Now().UnixMilli()
		return v, false
	case errors.Is(err, forge.ErrToken):
		v.State = "token"
		return v, false
	case forge.Unreachable(err):
		v.State, v.Stale, v.Error = "ok", true, err.Error()
		if same {
			v.MR, v.Pipeline, v.CheckedAt = was.MR, was.Pipeline, was.CheckedAt
		}
		return v, true
	}
	v.State, v.Error = "error", err.Error()
	return v, false
}

// forgeBranchSeen is told the branch view sendBranch sent: a change of
// branch, or of how it stands with its upstream (a push), asks again.
func (p *projectState) forgeBranchSeen(key string) {
	p.forge.mu.Lock()
	changed := p.forge.branch != "" && p.forge.branch != key
	p.forge.branch = key
	p.forge.mu.Unlock()
	if changed {
		p.forgeCheck()
	}
}

func (p *projectState) forgeClose() {
	p.forge.mu.Lock()
	p.forge.closed = true
	if p.forge.timer != nil {
		p.forge.timer.Stop()
		p.forge.timer = nil
	}
	p.forge.mu.Unlock()
}

// ---- routes ----

func forgeRoutes(route routeFunc) {
	route("GET /api/forge", (*projectState).forgeGet)
	route("POST /api/forge/check", (*projectState).forgePost)
	route("POST /api/forge/create", (*projectState).forgeCreate)
}

// forgeGet answers with what is known, and asks the host when nothing
// was asked yet or a timed check went without a page.
func (p *projectState) forgeGet(w http.ResponseWriter, r *http.Request) {
	if p.forgeCfg == nil {
		writeJSON(w, forgeView{State: "off"})
		return
	}
	f := &p.forge
	f.mu.Lock()
	ask := f.last.IsZero() && !f.checking || f.missed
	v := f.view
	f.mu.Unlock()
	if ask {
		p.forgeCheck()
		v.Checking = true
	}
	if v.State == "" {
		v.State = "off"
	}
	writeJSON(w, v)
}

// forgePost asks the host again: soft (a page got focus back) only when
// the last answer is old, or the host couldn't be reached.
func (p *projectState) forgePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Soft bool `json:"soft"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.forgeCfg == nil {
		writeJSON(w, okView{Message: "merge requests aren't available here"})
		return
	}
	f := &p.forge
	f.mu.Lock()
	ask := !req.Soft || f.missed || f.view.Stale || time.Since(f.last) >= forgeSoftAge
	f.mu.Unlock()
	if ask {
		p.forgeCheck()
	}
	writeJSON(w, okView{OK: true})
}

// createView is Create MR's answer: the page to open, or what to ask
// first.
type createView struct {
	URL string `json:"url,omitempty"`
	// Exists says URL is the open merge request's, not a new one's.
	Exists bool `json:"exists,omitempty"`
	// Push asks to push first: the branch has no upstream (NoUpstream),
	// or Ahead commits its upstream doesn't.
	Push       bool   `json:"push,omitempty"`
	NoUpstream bool   `json:"noUpstream,omitempty"`
	Ahead      int    `json:"ahead,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Message    string `json:"message,omitempty"`
}

// forgeCreate is Create MR: the host's page for a new merge request from
// the branch into the base, once the branch is pushed (skip opens it
// anyway).
func (p *projectState) forgeCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Skip bool `json:"skip"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.forgeCfg == nil {
		writeJSON(w, createView{Message: "merge requests aren't available here"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeJSON(w, createView{Message: "not a git repository"})
		return
	}
	st, err := g.Status(ctx)
	if err != nil {
		writeJSON(w, createView{Message: err.Error()})
		return
	}
	base := p.baseBranch()
	switch {
	case st.Branch == "":
		writeJSON(w, createView{Message: "HEAD is detached: switch to a branch first"})
		return
	case st.Branch == base:
		writeJSON(w, createView{Message: st.Branch + " is the base: switch to the branch to merge (B)"})
		return
	}
	p.forge.mu.Lock()
	v := p.forge.view
	p.forge.mu.Unlock()
	word := mrWord(v.Kind)
	if v.State == "ok" && v.Branch == st.Branch && v.MR != nil && v.MR.State == "open" {
		writeJSON(w, createView{URL: v.MR.URL, Exists: true, Message: mrSigil(v.Kind) + strconv.Itoa(v.MR.Number) + " is open: opening it"})
		return
	}
	remote := forge.PushRemote(ctx, g.Top, st.Branch)
	if !req.Skip && (st.Upstream == "" || st.Ahead > 0) {
		writeJSON(w, createView{Push: true, NoUpstream: st.Upstream == "", Ahead: st.Ahead, Remote: remote})
		return
	}
	raw, err := forge.RemoteURL(ctx, g.Top, remote)
	if err != nil {
		writeJSON(w, createView{Message: err.Error()})
		return
	}
	rm, ok := forge.ParseRemote(raw)
	if !ok {
		writeJSON(w, createView{Message: remote + "'s URL isn't one xf can read: " + raw})
		return
	}
	a, ok := forge.Match(ctx, p.forgeCfg.Hosts, rm)
	if !ok {
		writeJSON(w, createView{Message: rm.Host + " isn't a host xf knows: add it in Settings › Git hosting"})
		return
	}
	title, _ := g.HeadMessage(ctx)
	title, _, _ = strings.Cut(title, "\n")
	repo := forge.RepoAt(a.Kind, a.Name, rm.Path)
	writeJSON(w, createView{URL: forge.NewMRURL(repo, st.Branch, base, strings.TrimSpace(title)),
		Message: "opening " + repo.Host + "'s new " + word + " page"})
}

// mrWord and mrSigil are how a host names a merge request.
func mrWord(k forge.Kind) string {
	if k == forge.GitHub {
		return "pull request"
	}
	return "merge request"
}

func mrSigil(k forge.Kind) string {
	if k == forge.GitHub {
		return "#"
	}
	return "!"
}

// ---- hosts (Settings › Git hosting) ----

func (s *Server) forgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/forge/hosts", s.forgeHosts)
	mux.HandleFunc("POST /api/forge/hosts", s.forgeSetHost)
	mux.HandleFunc("POST /api/forge/hosts/delete", s.forgeDeleteHost)
	mux.HandleFunc("POST /api/forge/hosts/test", s.forgeTestHost)
}

// hostView is a host as Settings › Git hosting lists it: its token
// masked, and where the token comes from ("file", an environment
// variable's name, or "" for none).
type hostView struct {
	Host      string     `json:"host"`
	Kind      forge.Kind `json:"kind"`
	Token     string     `json:"token"`
	From      string     `json:"from"`
	Builtin   bool       `json:"builtin,omitempty"`
	Entry     bool       `json:"entry,omitempty"`
	TokenPage string     `json:"tokenPage"`
}

type hostsView struct {
	// Path is tokens.json's.
	Path  string     `json:"path"`
	Hosts []hostView `json:"hosts"`
	Error string     `json:"error,omitempty"`
}

func (s *Server) hostsView() hostsView {
	f := s.cfg.Forge
	if f == nil || f.Hosts == nil {
		return hostsView{Hosts: []hostView{}, Error: "Git hosting isn't available here"}
	}
	v := hostsView{Path: f.Hosts.Path, Hosts: []hostView{}}
	all, err := f.Hosts.Hosts()
	if err != nil {
		v.Error = err.Error()
	}
	for _, a := range all {
		v.Hosts = append(v.Hosts, hostView{Host: a.Name, Kind: a.Kind, Token: forge.Mask(a.Token), From: a.From,
			Builtin: a.Builtin, Entry: a.Entry, TokenPage: forge.TokenPage(a.Kind, a.Name)})
	}
	return v
}

func (s *Server) forgeHosts(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.hostsView()) }

// hostReq is a host to add, change, take out or test. An empty Token
// keeps the one saved, unless Clear.
type hostReq struct {
	Host  string     `json:"host"`
	Kind  forge.Kind `json:"kind"`
	Token string     `json:"token"`
	Clear bool       `json:"clear"`
}

func (s *Server) forgeSetHost(w http.ResponseWriter, r *http.Request) {
	var req hostReq
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Forge == nil || s.cfg.Forge.Hosts == nil {
		writeError(w, errors.New("Git hosting isn't available here"))
		return
	}
	if err := s.cfg.Forge.Hosts.Set(forge.Host{Name: req.Host, Kind: req.Kind, Token: req.Token}, req.Clear); err != nil {
		writeError(w, err)
		return
	}
	s.forgeAll()
	writeJSON(w, s.hostsView())
}

func (s *Server) forgeDeleteHost(w http.ResponseWriter, r *http.Request) {
	var req hostReq
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Forge == nil || s.cfg.Forge.Hosts == nil {
		writeError(w, errors.New("Git hosting isn't available here"))
		return
	}
	if err := s.cfg.Forge.Hosts.Delete(req.Host); err != nil {
		writeError(w, err)
		return
	}
	s.forgeAll()
	writeJSON(w, s.hostsView())
}

// forgeTestHost says who a host's token signs in as: the token given, or
// with none the one xf has.
func (s *Server) forgeTestHost(w http.ResponseWriter, r *http.Request) {
	var req hostReq
	if !readJSON(w, r, &req) {
		return
	}
	f := s.cfg.Forge
	if f == nil || f.Hosts == nil {
		writeJSON(w, okView{Message: "Git hosting isn't available here"})
		return
	}
	name := forge.CleanName(req.Host)
	kind, token := req.Kind, strings.TrimSpace(req.Token)
	if a, ok := f.Hosts.Lookup(name); ok {
		if !kind.Valid() {
			kind = a.Kind
		}
		if token == "" {
			token = a.Token
		}
	}
	switch {
	case name == "":
		writeJSON(w, okView{Message: "no host"})
		return
	case !kind.Valid():
		writeJSON(w, okView{Message: "pick what " + name + " runs: GitLab or GitHub"})
		return
	case token == "":
		writeJSON(w, okView{Message: "no token for " + name})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	who, err := f.client(kind, token).Whoami(ctx, forge.RepoAt(kind, name, ""))
	switch {
	case errors.Is(err, forge.ErrToken):
		writeJSON(w, okView{Message: name + " refused the token"})
	case err != nil:
		writeJSON(w, okView{Message: err.Error()})
	default:
		writeJSON(w, okView{OK: true, Message: "signed in to " + forge.RepoAt(kind, name, "").Host + " as " + who})
	}
}

// forgeAll asks every open project's host again: the hosts changed.
func (s *Server) forgeAll() {
	s.mu.Lock()
	var ps []*projectState
	for _, op := range s.open {
		ps = append(ps, op.p)
	}
	s.mu.Unlock()
	for _, p := range ps {
		p.forgeCheck()
	}
}
