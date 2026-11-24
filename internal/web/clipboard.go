package web

import "net/http"

// localClipboard reports whether this request is from a local browser and
// xf web can read this machine's clipboard directly, without a browser
// clipboard-read permission prompt. Disabled when --allow-host is set or
// the request came through a proxy, where the browser may be on another
// machine.
func (s *Server) localClipboard(r *http.Request) bool {
	return !s.remote() && !proxied(r) && s.cfg.ClipboardRead != nil
}

// clipboardRead answers GET /api/clipboard with this machine's clipboard
// text, for local browsers only.
func (s *Server) clipboardRead(w http.ResponseWriter, r *http.Request) {
	if s.remote() || proxied(r) {
		http.Error(w, "clipboard is available to local browsers only", http.StatusForbidden)
		return
	}
	if s.cfg.ClipboardRead == nil {
		http.Error(w, "clipboard is not configured", http.StatusNotImplemented)
		return
	}
	text, err := s.cfg.ClipboardRead()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		Text string `json:"text"`
	}{Text: text})
}

// clipboardWrite answers POST /api/clipboard by writing text to this
// machine's clipboard, for local browsers only.
func (s *Server) clipboardWrite(w http.ResponseWriter, r *http.Request) {
	if s.remote() || proxied(r) {
		http.Error(w, "clipboard is available to local browsers only", http.StatusForbidden)
		return
	}
	if s.cfg.ClipboardWrite == nil {
		http.Error(w, "clipboard is not configured", http.StatusNotImplemented)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.ClipboardWrite(req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, okView{OK: true})
}
