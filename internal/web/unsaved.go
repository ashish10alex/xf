package web

import (
	"net/http"
	"sort"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// Each tab tells xf web which files it has unsaved changes in, by project
// root: those of the project it shows, and those its workspace keeps of
// projects it showed before (ADR 0008), which no other tab could know of.
// A tab names itself, the same from page to page; one whose event
// streams have all been gone for tabGrace is forgotten (closed).

// tabGrace is how long a tab's unsaved files count once its streams have
// gone: a switch or a reload starts the tab's next page well within it.
const tabGrace = time.Minute

// tabRecord is what a tab said, and whether it is still there.
type tabRecord struct {
	unsaved map[string][]string
	streams int
	gone    time.Time
}

// tabLocked is the record of tab, made when new; s.mu is held.
func (s *Server) tabLocked(tab string) *tabRecord {
	if s.tabs == nil {
		s.tabs = map[string]*tabRecord{}
	}
	t := s.tabs[tab]
	if t == nil {
		t = &tabRecord{gone: time.Now()}
		s.tabs[tab] = t
	}
	return t
}

// tabConnected and tabGone count tab's event streams.
func (s *Server) tabConnected(tab string) {
	if tab == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tabLocked(tab).streams++
}

func (s *Server) tabGone(tab string) {
	if tab == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tabLocked(tab)
	t.streams--
	t.gone = time.Now()
}

// setUnsaved is a tab saying which files it has unsaved changes in.
func (s *Server) setUnsaved(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tab     string              `json:"tab"`
		Unsaved map[string][]string `json:"unsaved"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Tab == "" || len(req.Tab) > 64 || len(req.Unsaved) > 200 {
		http.Error(w, "bad request: tab and up to 200 projects", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	t := s.tabLocked(req.Tab)
	t.unsaved = req.Unsaved
	s.mu.Unlock()
	s.projectsChanged()
	writeJSON(w, okView{OK: true})
}

// unsavedOf are the files of the project at root that tabs other than
// except have unsaved changes in.
func (s *Server) unsavedOf(root, except string) []string {
	real := project.RealPath(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for id, t := range s.tabs {
		if t.streams <= 0 && time.Since(t.gone) > tabGrace {
			delete(s.tabs, id)
			continue
		}
		if id == except {
			continue
		}
		for r, files := range t.unsaved {
			if r != root && project.RealPath(r) != real {
				continue
			}
			for _, f := range files {
				seen[f] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// unsaved answers the files of the page's project that other tabs have
// unsaved changes in (?tab= names the page's own), for q's confirm.
func (s *Server) unsaved(w http.ResponseWriter, r *http.Request) {
	op := s.projectOf(r)
	files := []string{}
	if op != nil {
		files = s.unsavedOf(op.p.deps.Root, r.URL.Query().Get("tab"))
	}
	writeJSON(w, struct {
		Files []string `json:"files"`
	}{files})
}
