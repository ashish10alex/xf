package web

import (
	"cmp"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// themeKeys are the config keys of xf web's colour theme.
var themeKeys = []string{config.KeyWebTheme, config.KeyWebThemeLight, config.KeyWebThemeDark}

// themeEvent is what the "theme" event and the boot carry: the choice,
// the keys the project's own file sets (so the picker knows what it
// overrides), and for the user's choice, that the pages of a project that
// has its own ignore it.
type themeEvent struct {
	webtheme.Choice
	Own    []string `json:"own,omitempty"`
	Global bool     `json:"global,omitempty"`
}

// theme is the colour theme the pages of the project at root show (the
// projects' list, for ""), with the themes auto shows: the saved ones, else
// those picked this run, else xf's own.
func (s *Server) theme(root string) webtheme.Choice {
	if s.cfg.Theme != nil {
		return s.cfg.Theme(root).Resolved()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.picked.Resolved()
}

// themeOwn are the theme keys the config file of the project at root
// sets.
func themeOwn(root string) []string {
	if root == "" {
		return nil
	}
	keys, err := config.ReadKeys(filepath.Join(root, config.ProjectFile))
	if err != nil {
		return nil
	}
	var own []string
	for _, k := range themeKeys {
		if _, ok := keys[k]; ok {
			own = append(own, k)
		}
	}
	return own
}

// setTheme saves a colour theme, by its key (webTheme, the default, or
// webThemeLight or webThemeDark), and tells the pages it reaches. It goes
// to the user's config file, so every project without its own, or with
// "scope": "project" to the project the request names, where a theme of
// "" takes the key out so the user's applies. The TUI keeps its own.
func (s *Server) setTheme(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Theme string `json:"theme"`
		Scope string `json:"scope"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	root := ""
	var proj *projectState
	switch req.Scope {
	case "", "user":
	case "project":
		op := s.projectOf(r)
		if op == nil {
			http.Error(w, "bad request: no project to save the theme to", http.StatusBadRequest)
			return
		}
		if s.cfg.SetTheme == nil {
			http.Error(w, "bad request: no config to save the project's theme to", http.StatusBadRequest)
			return
		}
		proj, root = op.p, op.p.deps.Root
	default:
		http.Error(w, "bad request: scope must be user or project", http.StatusBadRequest)
		return
	}
	inherit := root != "" && req.Theme == ""
	th, found := webtheme.Find(req.Theme)
	next := s.theme(root)
	switch req.Key {
	case "", config.KeyWebTheme:
		req.Key = config.KeyWebTheme
		if !inherit && !found && req.Theme != webtheme.Auto {
			http.Error(w, "bad request: theme must be auto or one of "+webtheme.IDs(), http.StatusBadRequest)
			return
		}
		next.Theme = cmp.Or(th.ID, webtheme.Auto)
	case config.KeyWebThemeLight, config.KeyWebThemeDark:
		if !inherit && !found {
			http.Error(w, "bad request: theme must be one of "+webtheme.IDs(), http.StatusBadRequest)
			return
		}
		if req.Key == config.KeyWebThemeLight {
			next.Light = th.ID
		} else {
			next.Dark = th.ID
		}
	default:
		http.Error(w, "bad request: key must be webTheme, webThemeLight or webThemeDark", http.StatusBadRequest)
		return
	}
	var saveErr error
	if s.cfg.SetTheme != nil {
		id := next.Theme
		switch req.Key {
		case config.KeyWebThemeLight:
			id = next.Light
		case config.KeyWebThemeDark:
			id = next.Dark
		}
		if inherit {
			id = ""
		}
		saveErr = s.cfg.SetTheme(root, req.Key, id)
		if inherit && saveErr == nil {
			next = s.theme(root) // the user's, or the default
		}
	} else {
		s.mu.Lock()
		s.picked = next
		s.mu.Unlock()
	}
	if root != "" {
		s.projectThemeSent(proj)
	} else {
		s.userThemeSent(next)
	}
	if saveErr != nil {
		writeJSON(w, okView{OK: true, Message: "theme not saved: " + saveErr.Error()})
		return
	}
	writeJSON(w, okView{OK: true})
}

// projectThemeSent tells the pages of p its theme, the project's own over
// the user's.
func (s *Server) projectThemeSent(p *projectState) {
	root := p.deps.Root
	p.hub.send("theme", themeEvent{Choice: s.theme(root), Own: themeOwn(root)})
}

// userThemeSent tells every page the user's theme, next: those of the
// projects' list show it, and each open project's own theme goes to its
// pages, which may or may not be next.
func (s *Server) userThemeSent(next webtheme.Choice) {
	s.hub.send("theme", themeEvent{Choice: next, Global: true})
	s.mu.Lock()
	open := []*projectState{}
	for _, op := range s.open {
		open = append(open, op.p)
	}
	s.mu.Unlock()
	for _, p := range open {
		s.projectThemeSent(p)
	}
}

// themeChanged tells the pages after keys changed in a config file, the
// project p's, or with a nil p the user's.
func (s *Server) themeChanged(p *projectState, keys []string) {
	if !slices.ContainsFunc(keys, func(k string) bool { return slices.Contains(themeKeys, k) }) {
		return
	}
	if p != nil {
		s.projectThemeSent(p)
		return
	}
	s.userThemeSent(s.theme(""))
}
