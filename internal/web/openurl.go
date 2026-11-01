package web

import (
	"net/http"
	"net/url"
)

// openURL opens a link in the system's browser, as the TUI's u does. An
// app window asks for it: Chrome sends a link opened there to a new window
// rather than a tab of the last one, which the system's opener uses. A tab
// opens its links itself, since its browser may be on another device.
func (s *Server) openURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.OpenURL == nil {
		writeJSON(w, okView{Message: "xf can't open a browser here"})
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		writeJSON(w, okView{Message: "not a web link: " + req.URL})
		return
	}
	if err := s.cfg.OpenURL(u.String()); err != nil {
		writeJSON(w, okView{Message: "could not open the browser: " + err.Error()})
		return
	}
	writeJSON(w, okView{OK: true})
}
