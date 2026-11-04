package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// The picker's folder search is xf search in the page: a folder typed in
// the picker is searched for Dataform and dbt projects, which come back
// as the walk finds them, and those the user keeps are added to the
// projects the picker lists, not opened yet.

// foundProject is a project the folder search found.
type foundProject struct {
	Root string `json:"root"`
	// Path is Root as the picker shows it, the home directory as ~.
	Path string `json:"path"`
	// Rel is Root under the folder searched, with / between its folders.
	Rel     string `json:"rel"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	// Known is a project the picker lists already.
	Known bool `json:"known,omitempty"`
}

// findLine is a line of the folder search's answer: a project found, or
// the last line, saying it is done or why it stopped.
type findLine struct {
	Found *foundProject `json:"found,omitempty"`
	Done  bool          `json:"done,omitempty"`
	Error string        `json:"error,omitempty"`
}

// findProjects searches the folder a path query names, and the folders
// below it, for projects, answering a line of JSON as each is found and
// a last one when the walk ends. A page that stops listening (esc) stops
// the walk.
func (s *Server) findProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Add == nil {
		http.Error(w, "this xf web can't add projects", http.StatusNotFound)
		return
	}
	if !frontend.IsPathQuery(req.Path) {
		http.Error(w, "type a folder to search: ~/… or /…", http.StatusBadRequest)
		return
	}
	home, _ := os.UserHomeDir()
	dir := frontend.ExpandPath(home, req.Path)
	if info, err := os.Stat(dir); err != nil {
		http.Error(w, "no such folder: "+frontend.ShortPath(home, dir), http.StatusBadRequest)
		return
	} else if !info.IsDir() {
		http.Error(w, "not a folder: "+frontend.ShortPath(home, dir), http.StatusBadRequest)
		return
	}
	known := map[string]bool{}
	for _, pr := range s.projects() {
		known[project.RealPath(pr.Root)] = true
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(l findLine) {
		_ = enc.Encode(l)
		if flusher != nil {
			flusher.Flush()
		}
	}
	err := frontend.SearchProjects(r.Context(), home, []string{dir}, "", func(f backend.Found) {
		rel, err := filepath.Rel(dir, f.Root)
		if err != nil {
			rel = f.Root
		}
		send(findLine{Found: &foundProject{Root: f.Root, Path: frontend.ShortPath(home, f.Root), Rel: filepath.ToSlash(rel), Name: filepath.Base(f.Root),
			Backend: f.Backend, Known: known[project.RealPath(f.Root)]}})
	})
	switch {
	case r.Context().Err() != nil: // the page stopped listening
	case err != nil:
		send(findLine{Error: err.Error()})
	default:
		send(findLine{Done: true})
	}
}

// addProjects adds the projects the page kept from a folder search to
// those the picker lists, each checked to be its tool's project still.
func (s *Server) addProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Projects []struct {
			Root    string `json:"root"`
			Backend string `json:"backend"`
		} `json:"projects"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Add == nil {
		http.Error(w, "this xf web can't add projects", http.StatusNotFound)
		return
	}
	added := 0
	for _, p := range req.Projects {
		if !filepath.IsAbs(p.Root) {
			writeError(w, errors.New("not an absolute path: "+p.Root))
			return
		}
		if _, err := backend.Detect(p.Root, p.Backend); err != nil {
			writeError(w, err)
			return
		}
		ok, err := s.cfg.Add(filepath.Clean(p.Root), p.Backend)
		if err != nil {
			writeError(w, err)
			return
		}
		if ok {
			added++
		}
	}
	writeJSON(w, struct {
		Added int `json:"added"`
	}{added})
}
